package repository

import (
	"context"
	"time"

	"github.com/Miraitowa-kawayi/netdisk/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ListTrash 列出用户回收站中的顶层删除项；文件夹展示整棵被删子树的累计大小。
func (s *Store) ListTrash(ctx context.Context, ownerID uuid.UUID) ([]model.Node, error) {
	rows, err := s.pool.Query(ctx, `
SELECT r.id, r.owner_id, r.parent_id, r.name, r.is_dir,
       CASE WHEN r.is_dir THEN COALESCE((
           SELECT SUM(c.size) FROM nodes c
            WHERE c.owner_id = $1 AND c.deleted_root_id = r.id AND NOT c.is_dir
       ), 0) ELSE r.size END AS total_size,
       r.blob_id, r.created_at, r.updated_at, r.deleted_at, r.deleted_root_id
  FROM nodes r
 WHERE r.owner_id = $1
   AND r.deleted_at IS NOT NULL
   AND (r.deleted_root_id = r.id OR r.deleted_root_id IS NULL)
 ORDER BY r.deleted_at DESC`, ownerID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	items := make([]model.Node, 0, 16)
	for rows.Next() {
		var n model.Node
		if err := rows.Scan(
			&n.ID, &n.OwnerID, &n.ParentID, &n.Name, &n.IsDir, &n.Size,
			&n.BlobID, &n.CreatedAt, &n.UpdatedAt, &n.DeletedAt, &n.DeletedRootID,
		); err != nil {
			return nil, translate(err)
		}
		items = append(items, n)
	}
	return items, translate(rows.Err())
}

// RestoreTrashSubtree 恢复回收站中的一个顶层项及其整棵子树。
// 若原父目录已不存在或已被删，则自动恢复到根目录（或调用方指定的 targetParentID）。
func (s *Store) RestoreTrashSubtree(ctx context.Context, ownerID, id uuid.UUID, targetParentID *uuid.UUID, overrideParent bool) (model.Node, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Node{}, err
	}
	defer tx.Rollback(ctx)

	var root model.Node
	err = tx.QueryRow(ctx, `
SELECT `+nodeColumns+`, deleted_at, deleted_root_id
  FROM nodes
 WHERE id = $1 AND owner_id = $2 AND deleted_at IS NOT NULL
   AND (deleted_root_id = id OR deleted_root_id IS NULL)
 FOR UPDATE`, id, ownerID).Scan(
		&root.ID, &root.OwnerID, &root.ParentID, &root.Name, &root.IsDir, &root.Size,
		&root.BlobID, &root.CreatedAt, &root.UpdatedAt, &root.DeletedAt, &root.DeletedRootID,
	)
	if err != nil {
		return model.Node{}, translate(err)
	}

	destParent := root.ParentID
	if overrideParent {
		destParent = targetParentID
	} else if destParent != nil {
		// 检查原父目录是否仍然存活且是目录；否则退回根目录，避免恢复成孤悬节点。
		var live bool
		err = tx.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM nodes
     WHERE id = $1 AND owner_id = $2 AND is_dir AND deleted_at IS NULL
)`, *destParent, ownerID).Scan(&live)
		if err != nil {
			return model.Node{}, translate(err)
		}
		if !live {
			destParent = nil
		}
	}

	restored, err := scanNode(tx.QueryRow(ctx, `
UPDATE nodes
   SET parent_id = $3::uuid,
       deleted_at = NULL,
       deleted_root_id = NULL,
       updated_at = now()
 WHERE id = $1 AND owner_id = $2
RETURNING `+nodeColumns, id, ownerID, destParent))
	if err != nil {
		return model.Node{}, err
	}

	// 同一批软删的子树节点一并恢复。
	if _, err := tx.Exec(ctx, `
UPDATE nodes
   SET deleted_at = NULL,
       deleted_root_id = NULL,
       updated_at = now()
 WHERE owner_id = $1 AND deleted_root_id = $2 AND id <> $2`, ownerID, id); err != nil {
		return model.Node{}, translate(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return model.Node{}, err
	}
	return restored, nil
}

// PurgeTrashSubtree 彻底删除回收站中的某项及其子树，并回收已无任何节点（含存活与回收站）引用的内容块。
func (s *Store) PurgeTrashSubtree(ctx context.Context, ownerID, id uuid.UUID) ([]BlobLocation, error) {
	return s.purgeSubtree(ctx, ownerID, id, true)
}

// PurgeLiveSubtreeAndReclaim 立即彻底删除存活节点及其子树（用于 ?permanent=true），并回收无引用内容块。
func (s *Store) PurgeLiveSubtreeAndReclaim(ctx context.Context, ownerID, id uuid.UUID) ([]BlobLocation, error) {
	return s.purgeSubtree(ctx, ownerID, id, false)
}

func (s *Store) purgeSubtree(ctx context.Context, ownerID, id uuid.UUID, requireDeleted bool) ([]BlobLocation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	cond := "deleted_at IS NULL"
	if requireDeleted {
		cond = "deleted_at IS NOT NULL"
	}

	rows, err := tx.Query(ctx, `
WITH RECURSIVE subtree AS (
    SELECT id, blob_id FROM nodes WHERE id = $1 AND owner_id = $2 AND `+cond+`
    UNION ALL
    SELECT n.id, n.blob_id FROM nodes n JOIN subtree s ON n.parent_id = s.id
     WHERE n.owner_id = $2
)
SELECT id, blob_id FROM subtree`, id, ownerID)
	if err != nil {
		return nil, translate(err)
	}
	var nodeIDs []uuid.UUID
	blobSet := map[uuid.UUID]struct{}{}
	for rows.Next() {
		var nid uuid.UUID
		var bid *uuid.UUID
		if err := rows.Scan(&nid, &bid); err != nil {
			rows.Close()
			return nil, translate(err)
		}
		nodeIDs = append(nodeIDs, nid)
		if bid != nil {
			blobSet[*bid] = struct{}{}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, translate(err)
	}
	if len(nodeIDs) == 0 {
		return nil, ErrNotFound
	}

	// 硬删除子树节点（shares 靠外键 ON DELETE CASCADE 自动清除）。
	if _, err := tx.Exec(ctx, `DELETE FROM nodes WHERE owner_id = $1 AND id = ANY($2::uuid[])`, ownerID, nodeIDs); err != nil {
		return nil, translate(err)
	}

	locs, err := reclaimBlobsInTx(ctx, tx, blobSet)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return locs, nil
}

// PurgeAllTrash 清空某用户整个回收站并回收无引用内容块。
func (s *Store) PurgeAllTrash(ctx context.Context, ownerID uuid.UUID) ([]BlobLocation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
DELETE FROM nodes
 WHERE owner_id = $1 AND deleted_at IS NOT NULL
RETURNING blob_id`, ownerID)
	if err != nil {
		return nil, translate(err)
	}
	blobSet := map[uuid.UUID]struct{}{}
	for rows.Next() {
		var bid *uuid.UUID
		if err := rows.Scan(&bid); err != nil {
			rows.Close()
			return nil, translate(err)
		}
		if bid != nil {
			blobSet[*bid] = struct{}{}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, translate(err)
	}

	locs, err := reclaimBlobsInTx(ctx, tx, blobSet)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return locs, nil
}

// PurgeExpiredTrash 后台清理超过保留期限的回收站记录，并返回待删物理对象位置。
func (s *Store) PurgeExpiredTrash(ctx context.Context, cutoff time.Time, limit int) ([]BlobLocation, error) {
	if limit <= 0 {
		limit = 200
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
WITH expired AS (
    SELECT id FROM nodes
     WHERE deleted_at IS NOT NULL AND deleted_at <= $1
     ORDER BY deleted_at ASC
     LIMIT $2
)
DELETE FROM nodes
 WHERE id IN (SELECT id FROM expired)
RETURNING blob_id`, cutoff, limit)
	if err != nil {
		return nil, translate(err)
	}
	blobSet := map[uuid.UUID]struct{}{}
	for rows.Next() {
		var bid *uuid.UUID
		if err := rows.Scan(&bid); err != nil {
			rows.Close()
			return nil, translate(err)
		}
		if bid != nil {
			blobSet[*bid] = struct{}{}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, translate(err)
	}

	locs, err := reclaimBlobsInTx(ctx, tx, blobSet)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return locs, nil
}

func reclaimBlobsInTx(ctx context.Context, tx pgx.Tx, blobSet map[uuid.UUID]struct{}) ([]BlobLocation, error) {
	if len(blobSet) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, len(blobSet))
	for id := range blobSet {
		ids = append(ids, id)
	}
	rows, err := tx.Query(ctx, `
DELETE FROM blobs
 WHERE id = ANY($1::uuid[])
   AND NOT EXISTS (SELECT 1 FROM nodes WHERE nodes.blob_id = blobs.id)
RETURNING backend, storage_key`, ids)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	locs := make([]BlobLocation, 0)
	for rows.Next() {
		var l BlobLocation
		if err := rows.Scan(&l.Backend, &l.Key); err != nil {
			return nil, translate(err)
		}
		locs = append(locs, l)
	}
	return locs, translate(rows.Err())
}
