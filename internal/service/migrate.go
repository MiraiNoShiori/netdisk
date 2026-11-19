package service

import (
	"context"
	"fmt"
	"sort"

	"github.com/Miraitowa-kawayi/netdisk/internal/model"
	"github.com/Miraitowa-kawayi/netdisk/internal/storage"
	"github.com/google/uuid"
)

// StoragePolicyInfo 描述当前服务端可用的存储后端与热点分层策略配置。
type StoragePolicyInfo struct {
	DefaultBackend    string   `json:"default_backend"`
	AvailableBackends []string `json:"available_backends"`
	S3Configured      bool     `json:"s3_configured"`
	AutoTierThreshold int64    `json:"auto_tier_threshold"`
	AutoTierMinSize   int64    `json:"auto_tier_min_size"`
}

// StoragePolicy 返回服务端已装配的存储后端与热点自动迁移策略参数。
func (f *Files) StoragePolicy(autoTierThreshold, autoTierMinSize int64) StoragePolicyInfo {
	info := StoragePolicyInfo{
		DefaultBackend:    f.backend,
		AvailableBackends: []string{f.backend},
		AutoTierThreshold: autoTierThreshold,
		AutoTierMinSize:   autoTierMinSize,
	}
	if b, ok := f.storage.(*storage.Backends); ok {
		names := make([]string, 0, len(b.Stores))
		for k := range b.Stores {
			names = append(names, k)
		}
		sort.Strings(names)
		info.AvailableBackends = names
		_, info.S3Configured = b.Stores["s3"]
	}
	return info
}

// Migrate 将有权限访问的内容复制到目标后端，校验后切换指针。相同内容的所有引用一起迁移。
func (f *Files) Migrate(ctx context.Context, owner, id uuid.UUID, target string) error {
	node, err := f.store.GetNode(ctx, owner, id)
	if err != nil {
		return mapNotFound(err)
	}
	if node.BlobID == nil {
		return ErrNotAFile
	}
	blob, err := f.store.GetBlobByID(ctx, *node.BlobID)
	if err != nil {
		return err
	}
	return f.MigrateBlob(ctx, blob, target)
}

// MigrateBlob 将单个内容块流式复制到目标后端、校验 SHA-256 与长度、CAS 切换指针并回收源对象。
func (f *Files) MigrateBlob(ctx context.Context, blob model.Blob, target string) error {
	if blob.Backend == target {
		return nil
	}
	backends, ok := f.storage.(*storage.Backends)
	if !ok {
		return invalid("未配置多存储后端")
	}
	dst, err := backends.For(target)
	if err != nil {
		return invalid("目标存储后端未配置")
	}
	src, err := backends.For(blob.Backend)
	if err != nil {
		return err
	}
	rc, _, err := src.Open(ctx, blob.StorageKey)
	if err != nil {
		return err
	}
	defer rc.Close()
	key := "blobs/" + uuid.NewString()
	counted := &countingReader{r: rc}
	hash, err := dst.Put(ctx, key, counted, blob.Size)
	if err != nil {
		return err
	}
	if hash != blob.ContentHash || counted.n != blob.Size {
		_ = dst.Delete(ctx, key)
		return fmt.Errorf("迁移内容校验失败")
	}
	switched, err := f.store.SwitchBlobBackend(ctx, blob.ID, blob.Backend, blob.StorageKey, target, key)
	if err != nil || !switched {
		_ = dst.Delete(ctx, key)
		if err != nil {
			return err
		}
		return ErrConflict
	}
	_ = rc.Close()
	if err := src.Delete(ctx, blob.StorageKey); err != nil {
		_ = f.store.QueueObject(ctx, blob.Backend, blob.StorageKey)
		f.logger.Warn("delete migrated source", "key", blob.StorageKey, "error", err)
	}
	return nil
}

// AutoTierHotBlobs 扫描高访问频次的 local 内容块并自动迁移至 S3（若 S3 未配置则返回 0）。
func (f *Files) AutoTierHotBlobs(ctx context.Context, threshold, minSize int64, limit int) (int, error) {
	if threshold <= 0 {
		return 0, nil
	}
	backends, ok := f.storage.(*storage.Backends)
	if !ok {
		return 0, nil
	}
	if _, err := backends.For("s3"); err != nil {
		return 0, nil
	}
	blobs, err := f.store.ListHotLocalBlobs(ctx, threshold, minSize, limit)
	if err != nil {
		return 0, err
	}
	migrated := 0
	for _, b := range blobs {
		if err := ctx.Err(); err != nil {
			return migrated, err
		}
		if err := f.MigrateBlob(ctx, b, "s3"); err != nil {
			f.logger.Warn("auto-tier blob to s3 failed", "blob_id", b.ID, "downloads", b.DownloadCount, "error", err)
			continue
		}
		migrated++
	}
	return migrated, nil
}

func (f *Files) DirectURL(ctx context.Context, owner, id uuid.UUID) (string, error) {
	node, err := f.store.GetNode(ctx, owner, id)
	if err != nil {
		return "", mapNotFound(err)
	}
	if node.BlobID == nil {
		return "", ErrNotAFile
	}
	blob, err := f.store.GetBlobByID(ctx, *node.BlobID)
	if err != nil {
		return "", err
	}
	st, err := storage.For(f.storage, blob.Backend)
	if err != nil {
		return "", err
	}
	signer, ok := st.(interface {
		SignedURL(context.Context, string, string) (string, error)
	})
	if !ok {
		return "", invalid("本地内容请使用 download 接口")
	}
	// 直链签发也计入下载热度。
	if err := f.store.RecordBlobDownload(ctx, blob.ID); err != nil {
		f.logger.Warn("record blob download", "blob_id", blob.ID, "error", err)
	}
	return signer.SignedURL(ctx, blob.StorageKey, node.Name)
}
