package repository

import (
	"context"
	"github.com/MiraiNoShiori/netdisk/internal/model"
	"github.com/google/uuid"
)

func (s *Store) FindChild(ctx context.Context, owner uuid.UUID, parent *uuid.UUID, name string) (model.Node, error) {
	return scanNode(s.pool.QueryRow(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE owner_id=$1 AND parent_id IS NOT DISTINCT FROM $2::uuid AND name=$3 AND deleted_at IS NULL`, owner, parent, name))
}

// ReplaceContent 以旧 blob 为条件，避免 NFS 写回覆盖并发 API 更新。
func (s *Store) ReplaceContent(ctx context.Context, owner, id uuid.UUID, old *uuid.UUID, blob uuid.UUID, size int64) (model.Node, error) {
	return scanNode(s.pool.QueryRow(ctx, `UPDATE nodes SET blob_id=$4,size=$5,updated_at=now() WHERE owner_id=$1 AND id=$2 AND blob_id IS NOT DISTINCT FROM $3::uuid AND NOT is_dir AND deleted_at IS NULL RETURNING `+nodeColumns, owner, id, old, blob, size))
}
