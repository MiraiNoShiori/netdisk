package repository

import (
	"context"
	"github.com/MiraiNoShiori/netdisk/internal/model"
	"github.com/google/uuid"
)

// CompleteUploadNode 将节点发布与会话完成放入同一事务，防止崩溃留下半完成状态。
func (s *Store) CompleteUploadNode(ctx context.Context, owner, id, blob uuid.UUID, size int64) (model.Node, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Node{}, err
	}
	defer tx.Rollback(ctx)
	sess, err := scanUploadSession(tx.QueryRow(ctx, `UPDATE upload_sessions SET status='completed' WHERE id=$1 AND owner_id=$2 AND status='pending' AND expires_at>now() RETURNING `+uploadSessionColumns, id, owner))
	if err != nil {
		return model.Node{}, err
	}
	node, err := scanNode(tx.QueryRow(ctx, `INSERT INTO nodes(owner_id,parent_id,name,is_dir,size,blob_id) VALUES($1,$2,$3,false,$4,$5) RETURNING `+nodeColumns, owner, sess.ParentID, sess.Name, size, blob))
	if err != nil {
		return model.Node{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return model.Node{}, err
	}
	return node, nil
}
