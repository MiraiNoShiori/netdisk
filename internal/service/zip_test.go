package service

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiraiNoShiori/netdisk/internal/model"
	"github.com/MiraiNoShiori/netdisk/internal/repository"
	"github.com/MiraiNoShiori/netdisk/internal/storage"
	"github.com/MiraiNoShiori/netdisk/internal/testutil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// zip 打包下载的测试打真库（先跑 `make up`），库不可达时跳过。

type zipFixture struct {
	files   *Files
	store   *repository.Store
	cleanDB *pgxpool.Pool
	ctx     context.Context
	owner   uuid.UUID
}

func newZipFixture(t *testing.T) *zipFixture {
	t.Helper()
	dsn := testutil.Database(t)
	ctx := context.Background()

	st, err := repository.New(ctx, dsn)
	if err != nil {
		t.Fatalf("跳过：建连接池失败（%v）", err)
	}
	if err := st.Ping(ctx); err != nil {
		st.Close()
		t.Fatalf("跳过：数据库不可达（%v）；先跑 `make up`", err)
	}
	t.Cleanup(st.Close)

	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("跳过：清理用连接池建不起来（%v）", err)
	}
	t.Cleanup(db.Close)

	loc, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("建本地存储: %v", err)
	}
	f := &zipFixture{
		files:   NewFiles(st, loc, "local", slog.New(slog.NewTextHandler(io.Discard, nil))),
		store:   st,
		cleanDB: db,
		ctx:     ctx,
	}
	f.owner = f.newUser(t)
	return f
}

func (f *zipFixture) newUser(t *testing.T) uuid.UUID {
	t.Helper()
	u, err := f.store.CreateUser(f.ctx, "test-"+uuid.NewString(), "not-a-real-bcrypt-hash", "")
	if err != nil {
		t.Fatalf("建测试用户: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.cleanDB.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID)
	})
	return u.ID
}

func (f *zipFixture) upload(t *testing.T, parent *uuid.UUID, name, content string) model.Node {
	t.Helper()
	node, err := f.files.Upload(f.ctx, UploadInput{
		OwnerID:  f.owner,
		ParentID: parent,
		Name:     name,
		SizeHint: int64(len(content)),
		Body:     strings.NewReader(content),
	})
	if err != nil {
		t.Fatalf("上传 %s: %v", name, err)
	}
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])
	t.Cleanup(func() {
		_, _ = f.cleanDB.Exec(context.Background(), `DELETE FROM blobs WHERE content_hash = $1`, hash)
	})
	return node
}

func (f *zipFixture) mkdir(t *testing.T, parent *uuid.UUID, name string) model.Node {
	t.Helper()
	dir, err := f.files.CreateDir(f.ctx, f.owner, parent, name)
	if err != nil {
		t.Fatalf("建目录 %s: %v", name, err)
	}
	return dir
}

// openZip 调 OpenZip 并把 zip 流解成文件名到内容的映射（目录条目内容为空串）。
func (f *zipFixture) openZip(t *testing.T, id uuid.UUID) (string, map[string]string) {
	t.Helper()
	z, err := f.files.OpenZip(f.ctx, f.owner, id)
	if err != nil {
		t.Fatalf("OpenZip: %v", err)
	}
	defer z.Content.Close()

	raw, err := io.ReadAll(z.Content)
	if err != nil {
		t.Fatalf("读 zip 流: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zip 流不是合法压缩包（%d 字节）: %v", len(raw), err)
	}

	entries := make(map[string]string, len(zr.File))
	for _, file := range zr.File {
		if file.FileInfo().IsDir() {
			entries[file.Name] = ""
			continue
		}
		rc, err := file.Open()
		if err != nil {
			t.Fatalf("打开条目 %q: %v", file.Name, err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("读条目 %q: %v", file.Name, err)
		}
		entries[file.Name] = string(body)
	}
	return z.Name, entries
}

func TestZipFolderTree(t *testing.T) {
	f := newZipFixture(t)
	root := f.mkdir(t, nil, "docs")
	f.upload(t, &root.ID, "a.txt", "AAA")
	sub := f.mkdir(t, &root.ID, "sub")
	f.upload(t, &sub.ID, "b.txt", "BBB")
	f.mkdir(t, &root.ID, "empty")
	f.upload(t, &root.ID, "中文.txt", "chinese")

	name, entries := f.openZip(t, root.ID)
	if name != "docs.zip" {
		t.Errorf("建议文件名 = %q, want docs.zip", name)
	}

	want := map[string]string{
		"docs/a.txt":     "AAA",
		"docs/sub/b.txt": "BBB",
		"docs/中文.txt":    "chinese",
	}
	for entry, body := range want {
		got, ok := entries[entry]
		if !ok {
			t.Errorf("zip 里缺条目 %q（实得 %v）", entry, sortedKeys(entries))
			continue
		}
		if got != body {
			t.Errorf("条目 %q 内容 = %q, want %q", entry, got, body)
		}
	}

	// 目录条目：sub 与 empty 都要在，且名字以 '/' 结尾。
	for _, dir := range []string{"docs/sub/", "docs/empty/"} {
		body, ok := entries[dir]
		if !ok {
			t.Errorf("缺目录条目 %q（实得 %v）", dir, sortedKeys(entries))
			continue
		}
		if body != "" {
			t.Errorf("目录条目 %q 不该有内容，实得 %q", dir, body)
		}
	}

	// 条目名必须以 docs/ 开头，且不带前导 '/'。
	for entry := range entries {
		if strings.HasPrefix(entry, "/") {
			t.Errorf("条目名不能以 '/' 开头（Windows 资源管理器会打不开）：%q", entry)
		}
		if !strings.HasPrefix(entry, "docs/") {
			t.Errorf("条目 %q 不在 docs/ 前缀下", entry)
		}
	}
}

func TestZipEmptyFolder(t *testing.T) {
	f := newZipFixture(t)
	root := f.mkdir(t, nil, "blank")

	_, entries := f.openZip(t, root.ID)
	if len(entries) != 1 {
		t.Fatalf("空目录的 zip 应该只有一条目录项，实得 %v", sortedKeys(entries))
	}
	if _, ok := entries["blank/"]; !ok {
		t.Errorf("空目录缺 %q 这条（实得 %v）", "blank/", sortedKeys(entries))
	}
}

func TestZipRejectsFile(t *testing.T) {
	f := newZipFixture(t)
	file := f.upload(t, nil, "solo.txt", "X")

	if _, err := f.files.OpenZip(f.ctx, f.owner, file.ID); !errors.Is(err, ErrNotADirectory) {
		t.Fatalf("对文件调 OpenZip：err = %v, want ErrNotADirectory", err)
	}
}

func TestZipMissingIsNotFound(t *testing.T) {
	f := newZipFixture(t)

	if _, err := f.files.OpenZip(f.ctx, f.owner, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的 id：err = %v, want ErrNotFound", err)
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type zipTestStorage struct {
	openFn func(ctx context.Context, key string) (io.ReadSeekCloser, storage.ObjectInfo, error)
}

func (s *zipTestStorage) Put(context.Context, string, io.Reader, int64) (string, error) {
	return "", errors.New("not implemented")
}

func (s *zipTestStorage) Open(ctx context.Context, key string) (io.ReadSeekCloser, storage.ObjectInfo, error) {
	return s.openFn(ctx, key)
}

func (s *zipTestStorage) Concat(context.Context, string, []string) (string, int64, error) {
	return "", 0, errors.New("not implemented")
}

func (s *zipTestStorage) Delete(context.Context, string) error {
	return errors.New("not implemented")
}

func (s *zipTestStorage) Stat(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, errors.New("not implemented")
}

type chunkedTestReader struct {
	totalChunks int
	chunkSize   int
	fillByte    byte
	reads       atomic.Int32
	closed      atomic.Bool
	onRead      func(readNum int32)
}

func (r *chunkedTestReader) Read(p []byte) (int, error) {
	n := r.reads.Add(1)
	if int(n) > r.totalChunks {
		return 0, io.EOF
	}
	size := r.chunkSize
	if size > len(p) {
		size = len(p)
	}
	for i := 0; i < size; i++ {
		p[i] = r.fillByte
	}
	if r.onRead != nil {
		r.onRead(n)
	}
	return size, nil
}

func (r *chunkedTestReader) Seek(int64, int) (int64, error) {
	return 0, errors.New("not implemented")
}

func (r *chunkedTestReader) Close() error {
	r.closed.Store(true)
	return nil
}

func TestZipEarlyCloseStopsProducerAndReleasesSlot(t *testing.T) {
	const totalChunks = 64
	firstReader := &chunkedTestReader{totalChunks: totalChunks, chunkSize: zipCopyBufSize, fillByte: 'A'}
	var secondOpened atomic.Bool

	st := &zipTestStorage{
		openFn: func(_ context.Context, key string) (io.ReadSeekCloser, storage.ObjectInfo, error) {
			switch key {
			case "k1":
				return firstReader, storage.ObjectInfo{Key: key, Size: int64(totalChunks * zipCopyBufSize)}, nil
			case "k2":
				secondOpened.Store(true)
				return &chunkedTestReader{totalChunks: 1, chunkSize: 16, fillByte: 'B'}, storage.ObjectInfo{Key: key, Size: 16}, nil
			default:
				return nil, storage.ObjectInfo{}, storage.ErrNotFound
			}
		},
	}

	files := NewFiles(nil, st, "local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	files.SetMaxConcurrentZips(1)

	now := time.Now()
	entries := []model.ZipEntry{
		{RelPath: "docs", IsDir: true, UpdatedAt: now},
		{RelPath: "docs/big.jpg", IsDir: false, UpdatedAt: now, Backend: "local", StorageKey: "k1"},
		{RelPath: "docs/second.txt", IsDir: false, UpdatedAt: now, Backend: "local", StorageKey: "k2"},
	}

	z, err := files.openZipEntries(context.Background(), "docs.zip", entries)
	if err != nil {
		t.Fatalf("openZipEntries: %v", err)
	}

	// 只读一小段即关闭读端，模拟客户端断连后 handler 提前返回。
	head := make([]byte, 16)
	if _, err := io.ReadFull(z.Content, head); err != nil {
		t.Fatalf("读 zip 头部: %v", err)
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- z.Content.Close()
	}()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close 返回错误: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close 超时：生产端可能阻塞在 pipe 写入或发生 goroutine 泄漏")
	}

	if !firstReader.closed.Load() {
		t.Error("底层存储 reader 未关闭")
	}
	if got := firstReader.reads.Load(); got >= totalChunks {
		t.Errorf("生产端未在写错误后尽快停止：reads=%d, totalChunks=%d", got, totalChunks)
	}
	if secondOpened.Load() {
		t.Error("写错误后仍继续打开了下一个条目，未将写错误视为终止条件")
	}

	// 并发槽位应已释放，能够立即开启新的打包。
	z2, err := files.openZipEntries(context.Background(), "empty.zip", []model.ZipEntry{{RelPath: "empty", IsDir: true, UpdatedAt: now}})
	if err != nil {
		t.Fatalf("关闭后槽位未释放: %v", err)
	}
	defer z2.Content.Close()
	if _, err := io.ReadAll(z2.Content); err != nil {
		t.Fatalf("读取第二次 zip: %v", err)
	}
}

func TestZipContextCancelStopsBetweenChunks(t *testing.T) {
	const totalChunks = 128
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reader := &chunkedTestReader{
		totalChunks: totalChunks,
		chunkSize:   zipCopyBufSize,
		fillByte:    0, // 高度可压缩数据，确保 flate 缓冲期间也能靠分块间 ctx 检查及时停止
		onRead: func(readNum int32) {
			if readNum == 2 {
				cancel()
			}
		},
	}

	st := &zipTestStorage{
		openFn: func(_ context.Context, key string) (io.ReadSeekCloser, storage.ObjectInfo, error) {
			return reader, storage.ObjectInfo{Key: key, Size: int64(totalChunks * zipCopyBufSize)}, nil
		},
	}

	files := NewFiles(nil, st, "local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	files.SetMaxConcurrentZips(1)

	now := time.Now()
	entries := []model.ZipEntry{
		{RelPath: "docs", IsDir: true, UpdatedAt: now},
		{RelPath: "docs/zeros.txt", IsDir: false, UpdatedAt: now, Backend: "local", StorageKey: "zeros"},
	}

	z, err := files.openZipEntries(ctx, "docs.zip", entries)
	if err != nil {
		t.Fatalf("openZipEntries: %v", err)
	}

	_, readErr := io.ReadAll(z.Content)
	if !errors.Is(readErr, context.Canceled) {
		t.Fatalf("期望返回 context.Canceled，实得 %v", readErr)
	}
	if err := z.Content.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := reader.reads.Load(); got > 2 {
		t.Errorf("ctx 取消后仍继续读取了后续分块：reads=%d, want <= 2", got)
	}
	if !reader.closed.Load() {
		t.Error("ctx 取消后底层存储 reader 未关闭")
	}

	// 槽位应已归还。
	z2, err := files.openZipEntries(context.Background(), "ok.zip", []model.ZipEntry{{RelPath: "ok", IsDir: true, UpdatedAt: now}})
	if err != nil {
		t.Fatalf("取消后槽位未归还: %v", err)
	}
	defer z2.Content.Close()
	if _, err := io.ReadAll(z2.Content); err != nil {
		t.Fatalf("读取新 zip: %v", err)
	}
}

func TestZipBoundedConcurrencySemaphore(t *testing.T) {
	st := &zipTestStorage{
		openFn: func(_ context.Context, key string) (io.ReadSeekCloser, storage.ObjectInfo, error) {
			return &chunkedTestReader{totalChunks: 4, chunkSize: zipCopyBufSize, fillByte: 'Z'}, storage.ObjectInfo{Key: key, Size: int64(4 * zipCopyBufSize)}, nil
		},
	}

	files := NewFiles(nil, st, "local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	files.SetMaxConcurrentZips(2)

	now := time.Now()
	entries := []model.ZipEntry{
		{RelPath: "dir", IsDir: true, UpdatedAt: now},
		{RelPath: "dir/file.jpg", IsDir: false, UpdatedAt: now, Backend: "local", StorageKey: "k"},
	}

	z1, err := files.openZipEntries(context.Background(), "1.zip", entries)
	if err != nil {
		t.Fatalf("第 1 个 zip 应成功: %v", err)
	}
	defer z1.Content.Close()

	z2, err := files.openZipEntries(context.Background(), "2.zip", entries)
	if err != nil {
		t.Fatalf("第 2 个 zip 应成功: %v", err)
	}
	defer z2.Content.Close()

	if _, err := files.openZipEntries(context.Background(), "3.zip", entries); !errors.Is(err, ErrTooManyZips) {
		t.Fatalf("超出并发上限时应返回 ErrTooManyZips，实得 %v", err)
	}

	// 释放第 1 个流后，第 3 个应立刻允许进入并完整产出合法 zip。
	if err := z1.Content.Close(); err != nil {
		t.Fatalf("关闭 z1: %v", err)
	}

	z3, err := files.openZipEntries(context.Background(), "3.zip", entries)
	if err != nil {
		t.Fatalf("释放槽位后第 3 个 zip 应成功: %v", err)
	}
	defer z3.Content.Close()

	raw, err := io.ReadAll(z3.Content)
	if err != nil {
		t.Fatalf("读取 z3 失败: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("z3 不是合法压缩包: %v", err)
	}
	if len(zr.File) != 2 {
		t.Fatalf("z3 条目数 = %d, want 2", len(zr.File))
	}
}
