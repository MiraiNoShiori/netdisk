package repository

import (
	"context"

	"github.com/MiraiNoShiori/netdisk/internal/model"
	"github.com/google/uuid"
)

// CompleteUploadWithBlob 在同一事务中完成会话关闭、blob 登记与节点发布，
// 既消除 blob 登记到建节点之间的并发 GC 竞态窗口，又确保冲突回滚时不残留无引用 blob 行。
func (s *Store) CompleteUploadWithBlob(ctx context.Context, owner, sessionID uuid.UUID, hash string, size int64, backend, storageKey string) (node model.Node, blob model.Blob, recorded bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Node{}, model.Blob{}, false, err
	}
	defer tx.Rollback(ctx)

	sess, err := scanUploadSession(tx.QueryRow(ctx, `UPDATE upload_sessions SET status='completed' WHERE id=$1 AND owner_id=$2 AND status='pending' AND expires_at>now() RETURNING `+uploadSessionColumns, sessionID, owner))
	if err != nil {
		return model.Node{}, model.Blob{}, false, err
	}

	const upsertQ = `INSERT INTO blobs(content_hash,size,backend,storage_key) VALUES($1,$2,$3,$4)
 ON CONFLICT(content_hash) DO UPDATE SET content_hash=EXCLUDED.content_hash
 RETURNING ` + blobColumns + `, (xmax=0) AS recorded`

	if err = tx.QueryRow(ctx, upsertQ, hash, size, backend, storageKey).Scan(
		&blob.ID, &blob.ContentHash, &blob.Size, &blob.Backend, &blob.StorageKey,
		&blob.DownloadCount, &blob.LastAccessedAt, &blob.CreatedAt, &recorded,
	); err != nil {
		return model.Node{}, model.Blob{}, false, translate(err)
	}

	node, err = scanNode(tx.QueryRow(ctx, `INSERT INTO nodes(owner_id,parent_id,name,is_dir,size,blob_id) VALUES($1,$2,$3,false,$4,$5) RETURNING `+nodeColumns, owner, sess.ParentID, sess.Name, blob.Size, blob.ID))
	if err != nil {
		return model.Node{}, model.Blob{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return model.Node{}, model.Blob{}, false, err
	}
	node.Backend = blob.Backend
	return node, blob, recorded, nil
}
