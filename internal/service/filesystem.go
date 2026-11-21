package service

import (
	"context"
	"github.com/MiraiNoShiori/netdisk/internal/model"
	"github.com/google/uuid"
	"io"
	"path"
	"strings"
)

// LookupPath 只沿当前用户的存活节点走，不把磁盘路径暴露给协议层。
func (f *Files) LookupPath(ctx context.Context, owner uuid.UUID, name string) (model.Node, error) {
	name = path.Clean("/" + name)
	if name == "/" {
		return model.Node{OwnerID: owner, Name: "/", IsDir: true}, nil
	}
	var parent *uuid.UUID
	var node model.Node
	parts := strings.Split(strings.TrimPrefix(name, "/"), "/")
	for i, part := range parts {
		var err error
		node, err = f.store.FindChild(ctx, owner, parent, part)
		if err != nil {
			return model.Node{}, mapNotFound(err)
		}
		if i < len(parts)-1 && !node.IsDir {
			return model.Node{}, ErrNotADirectory
		}
		id := node.ID
		parent = &id
	}
	return node, nil
}
func (f *Files) Replace(ctx context.Context, owner uuid.UUID, node model.Node, body io.Reader) (model.Node, error) {
	key := "blobs/" + uuid.NewString()
	count := &countingReader{r: body}
	hash, err := f.storage.Put(ctx, key, count, -1)
	if err != nil {
		return model.Node{}, err
	}
	blob, recorded, err := f.store.UpsertBlob(ctx, hash, count.n, f.backend, key)
	if err != nil {
		_ = f.storage.Delete(ctx, key)
		return model.Node{}, err
	}
	if !recorded {
		_ = f.storage.Delete(ctx, key)
	}
	if delta := blob.Size - node.Size; delta > 0 {
		if err := checkQuota(ctx, f.store, owner, delta); err != nil {
			if recorded {
				f.cleanupBlob(ctx, blob.ID, key)
			}
			return model.Node{}, err
		}
	}
	updated, err := f.store.ReplaceContent(ctx, owner, node.ID, node.BlobID, blob.ID, blob.Size)
	if err != nil {
		if recorded {
			f.cleanupBlob(ctx, blob.ID, key)
		}
		return model.Node{}, mapNotFound(err)
	}
	if node.BlobID != nil && *node.BlobID != blob.ID {
		old, err := f.store.GetBlobByID(ctx, *node.BlobID)
		if err == nil {
			deleted, err := f.store.DeleteBlobIfUnreferenced(ctx, old.ID)
			if err == nil && deleted {
				_ = f.store.QueueObject(ctx, old.Backend, old.StorageKey)
			}
		}
	}
	// 新节点发布后再回收旧内容。
	return updated, nil
}
