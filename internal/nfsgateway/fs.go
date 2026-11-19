// Package nfsgateway 将 NFSv3 文件操作桥接到网盘业务层。一实例仅导出一个用户。
package nfsgateway

import (
	"context"
	"errors"
	"github.com/Miraitowa-kawayi/netdisk/internal/model"
	"github.com/Miraitowa-kawayi/netdisk/internal/service"
	"github.com/go-git/go-billy/v5"
	"github.com/google/uuid"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

type FS struct {
	ctx   context.Context
	files *service.Files
	owner uuid.UUID
	temp  string
	mu    sync.Mutex
}

func NewFS(ctx context.Context, files *service.Files, owner uuid.UUID, temp string) *FS {
	return &FS{ctx: ctx, files: files, owner: owner, temp: temp}
}
func fsErr(err error) error {
	switch {
	case errors.Is(err, service.ErrNotFound):
		return os.ErrNotExist
	case errors.Is(err, service.ErrNameConflict):
		return os.ErrExist
	}
	return err
}
func (f *FS) lookup(name string) (model.Node, error) {
	if strings.ContainsRune(name, 0) {
		return model.Node{}, os.ErrInvalid
	}
	n, err := f.files.LookupPath(f.ctx, f.owner, name)
	return n, fsErr(err)
}
func (f *FS) parent(name string) (*uuid.UUID, string, error) {
	name = path.Clean("/" + name)
	if name == "/" {
		return nil, "", os.ErrPermission
	}
	n, err := f.lookup(path.Dir(name))
	if err != nil {
		return nil, "", err
	}
	if !n.IsDir {
		return nil, "", os.ErrInvalid
	}
	var id *uuid.UUID
	if n.ID != uuid.Nil {
		id = &n.ID
	}
	return id, path.Base(name), nil
}
func (f *FS) Create(name string) (billy.File, error) {
	return f.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)
}
func (f *FS) Open(name string) (billy.File, error) { return f.OpenFile(name, os.O_RDONLY, 0) }
func (f *FS) OpenFile(name string, flag int, perm os.FileMode) (billy.File, error) {
	writable := flag&(os.O_RDWR|os.O_WRONLY|os.O_CREATE|os.O_TRUNC) != 0
	node, err := f.lookup(name)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if !exists && flag&os.O_CREATE == 0 {
		return nil, os.ErrNotExist
	}
	if exists && flag&os.O_EXCL != 0 {
		return nil, os.ErrExist
	}
	if exists && node.IsDir {
		return nil, os.ErrInvalid
	}
	if !writable {
		dl, err := f.files.OpenDownload(f.ctx, f.owner, node.ID)
		if err != nil {
			return nil, fsErr(err)
		}
		return &readFile{ReadSeekCloser: dl.Content, name: name}, nil
	}
	parent, base, err := f.parent(name)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(f.temp, "netdisk-nfs-*")
	if err != nil {
		return nil, err
	}
	cleanup := func() { tmp.Close(); os.Remove(tmp.Name()) }
	if exists && flag&os.O_TRUNC == 0 {
		dl, err := f.files.OpenDownload(f.ctx, f.owner, node.ID)
		if err != nil {
			cleanup()
			return nil, fsErr(err)
		}
		_, err = io.Copy(tmp, dl.Content)
		dl.Content.Close()
		if err != nil {
			cleanup()
			return nil, err
		}
	}
	if flag&os.O_APPEND == 0 {
		if _, err = tmp.Seek(0, io.SeekStart); err != nil {
			cleanup()
			return nil, err
		}
	}
	return &writeFile{File: tmp, name: name, finish: func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if exists {
			_, err = f.files.Replace(f.ctx, f.owner, node, tmp)
		} else {
			_, err = f.files.Upload(f.ctx, service.UploadInput{OwnerID: f.owner, ParentID: parent, Name: base, SizeHint: -1, Body: tmp})
		}
		return fsErr(err)
	}}, nil
}
func (f *FS) Stat(name string) (os.FileInfo, error) {
	n, err := f.lookup(name)
	if err != nil {
		return nil, err
	}
	return info{n}, nil
}
func (f *FS) Lstat(name string) (os.FileInfo, error) { return f.Stat(name) }
func (f *FS) ReadDir(name string) ([]os.FileInfo, error) {
	n, err := f.lookup(name)
	if err != nil {
		return nil, err
	}
	if !n.IsDir {
		return nil, os.ErrInvalid
	}
	var parent *uuid.UUID
	if n.ID != uuid.Nil {
		parent = &n.ID
	}
	nodes, err := f.files.List(f.ctx, f.owner, parent)
	if err != nil {
		return nil, fsErr(err)
	}
	out := make([]os.FileInfo, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, info{n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}
func (f *FS) MkdirAll(name string, perm os.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	current := ""
	for _, part := range strings.Split(strings.Trim(path.Clean("/"+name), "/"), "/") {
		if part == "" {
			continue
		}
		current = path.Join(current, part)
		n, err := f.lookup(current)
		if err == nil {
			if !n.IsDir {
				return os.ErrExist
			}
			continue
		}
		if !os.IsNotExist(err) {
			return err
		}
		parent, base, err := f.parent(current)
		if err != nil {
			return err
		}
		if _, err = f.files.CreateDir(f.ctx, f.owner, parent, base); err != nil {
			return fsErr(err)
		}
	}
	return nil
}
func (f *FS) Rename(old, new string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.lookup(old)
	if err != nil {
		return err
	}
	if n.ID == uuid.Nil {
		return os.ErrPermission
	}
	parent, base, err := f.parent(new)
	if err != nil {
		return err
	}
	_, err = f.files.Update(f.ctx, f.owner, n.ID, service.UpdateInput{Name: &base, SetParent: true, ParentID: parent})
	return fsErr(err)
}
func (f *FS) Remove(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.lookup(name)
	if err != nil {
		return err
	}
	if n.ID == uuid.Nil {
		return os.ErrPermission
	}
	if n.IsDir {
		children, err := f.ReadDir(name)
		if err != nil {
			return err
		}
		if len(children) > 0 {
			return errors.New("directory not empty")
		}
	}
	return fsErr(f.files.Delete(f.ctx, f.owner, n.ID))
}
func (f *FS) Join(parts ...string) string { return path.Join(parts...) }
func (f *FS) Root() string                { return "/" }
func (f *FS) Chroot(name string) (billy.Filesystem, error) {
	if path.Clean(name) == "/" || name == "." {
		return f, nil
	}
	return nil, billy.ErrNotSupported
}
func (f *FS) TempFile(dir, prefix string) (billy.File, error) {
	return f.OpenFile(path.Join(dir, prefix+uuid.NewString()), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
}
func (f *FS) Symlink(target, link string) error    { return billy.ErrNotSupported }
func (f *FS) Readlink(link string) (string, error) { return "", billy.ErrNotSupported }

// 单用户导出固定权限；NFS 客户端的 UID/GID 不参与网盘鉴权。
func (f *FS) Chmod(name string, mode os.FileMode) error         { _, err := f.Stat(name); return err }
func (f *FS) Chown(name string, uid, gid int) error             { _, err := f.Stat(name); return err }
func (f *FS) Lchown(name string, uid, gid int) error            { return f.Chown(name, uid, gid) }
func (f *FS) Chtimes(name string, atime, mtime time.Time) error { _, err := f.Stat(name); return err }
func (f *FS) Capabilities() billy.Capability {
	return billy.ReadCapability | billy.WriteCapability | billy.ReadAndWriteCapability | billy.SeekCapability | billy.TruncateCapability
}

type info struct{ node model.Node }

func (i info) Name() string { return i.node.Name }
func (i info) Size() int64  { return i.node.Size }
func (i info) Mode() os.FileMode {
	if i.node.IsDir {
		return os.ModeDir | 0700
	}
	return 0600
}
func (i info) ModTime() time.Time { return i.node.UpdatedAt }
func (i info) IsDir() bool        { return i.node.IsDir }
func (i info) Sys() any           { return nil }

type readFile struct {
	io.ReadSeekCloser
	name string
	mu   sync.Mutex
}

func (f *readFile) Name() string              { return f.name }
func (f *readFile) Write([]byte) (int, error) { return 0, os.ErrPermission }
func (f *readFile) Truncate(int64) error      { return os.ErrPermission }
func (f *readFile) Lock() error               { return billy.ErrNotSupported }
func (f *readFile) Unlock() error             { return billy.ErrNotSupported }
func (f *readFile) ReadAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if _, err = f.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	n, err := io.ReadFull(f, p)
	_, restore := f.Seek(pos, io.SeekStart)
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	if err == nil {
		err = restore
	}
	return n, err
}

type writeFile struct {
	*os.File
	name   string
	finish func() error
	once   sync.Once
	err    error
}

func (f *writeFile) Name() string  { return f.name }
func (f *writeFile) Close() error  { f.once.Do(func() { f.err = f.finish() }); return f.err }
func (f *writeFile) Lock() error   { return billy.ErrNotSupported }
func (f *writeFile) Unlock() error { return billy.ErrNotSupported }
