package service

import (
	"context"
	"errors"
	"time"

	"github.com/MiraiNoShiori/netdisk/internal/storage"
)

// MaintenanceOptions 配置后台定期维护任务（回收站自动清理与热点文件智能分层）。
type MaintenanceOptions struct {
	Files             *Files
	TrashRetention    time.Duration
	AutoTierThreshold int64
	AutoTierMinSize   int64
}

// CleanupExpired 保留清理失败的会话，下一轮重试，避免对象删除失败后丢失追踪信息。
func (u *Uploads) CleanupExpired(ctx context.Context) error {
	sessions, err := u.store.ExpiredUploads(ctx)
	if err != nil {
		return err
	}
	for _, old := range sessions {
		err := func() error {
			unlock, err := u.store.LockUpload(ctx, old.ID)
			if err != nil {
				return err
			}
			defer unlock()
			sess, err := u.store.GetUploadSession(ctx, old.OwnerID, old.ID)
			if err != nil {
				return err
			}
			if sess.Status == "pending" && sess.ExpiresAt.After(time.Now()) {
				return nil
			}
			st, err := storage.For(u.storage, sess.Backend)
			if err != nil {
				return err
			}
			parts, err := u.store.ListUploadParts(ctx, sess.ID)
			if err != nil {
				return err
			}
			for _, part := range parts {
				if err := st.Delete(ctx, part.StorageKey); err != nil && !errors.Is(err, storage.ErrNotFound) {
					return err
				}
			}
			return u.store.DeleteUploadSession(ctx, sess.ID)
		}()
		if err != nil {
			u.logger.Warn("cleanup upload", "id", old.ID, "error", err)
		}
	}
	if err := u.store.CleanAuthSessions(ctx); err != nil {
		return err
	}
	locations, err := u.store.Garbage(ctx)
	if err != nil {
		return err
	}
	for _, loc := range locations {
		st, err := storage.For(u.storage, loc.Backend)
		if err != nil {
			u.logger.Warn("gc backend missing", "error", err)
			continue
		}
		if err = st.Delete(ctx, loc.Key); err != nil && !errors.Is(err, storage.ErrNotFound) {
			u.logger.Warn("gc retry", "key", loc.Key, "error", err)
			continue
		}
		if err = u.store.ForgetGarbage(ctx, loc.Backend, loc.Key); err != nil {
			return err
		}
	}
	return nil
}

// RunCleanup 定期清理过期上传、过期会话与物理对象回收队列，并可选执行回收站过期清理与热点分层。
func (u *Uploads) RunCleanup(ctx context.Context, opts ...MaintenanceOptions) {
	var opt MaintenanceOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := u.CleanupExpired(ctx); err != nil && ctx.Err() == nil {
			u.logger.Warn("cleanup failed", "error", err)
		}
		if opt.Files != nil && opt.TrashRetention > 0 {
			if err := opt.Files.PurgeExpiredTrash(ctx, opt.TrashRetention); err != nil && ctx.Err() == nil {
				u.logger.Warn("purge expired trash failed", "error", err)
			}
		}
		if opt.Files != nil && opt.AutoTierThreshold > 0 {
			if n, err := opt.Files.AutoTierHotBlobs(ctx, opt.AutoTierThreshold, opt.AutoTierMinSize, 10); err != nil && ctx.Err() == nil {
				u.logger.Warn("auto-tier hot blobs failed", "error", err)
			} else if n > 0 {
				u.logger.Info("auto-tiered hot blobs to s3", "count", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
