package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/Miraitowa-kawayi/netdisk/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const nodeColumns = `id, owner_id, parent_id, name, is_dir, size, blob_id, created_at, updated_at`

func scanNode(row pgx.Row) (model.Node, error) {
	var n model.Node
	err := row.Scan(&n.ID, &n.OwnerID, &n.ParentID, &n.Name, &n.IsDir, &n.Size, &n.BlobID, &n.CreatedAt, &n.UpdatedAt)
	return n, translate(err)
}

func scanNodeWithBlob(row pgx.Row) (model.Node, error) {
	var n model.Node
	err := row.Scan(&n.ID, &n.OwnerID, &n.ParentID, &n.Name, &n.IsDir, &n.Size, &n.BlobID, &n.CreatedAt, &n.UpdatedAt, &n.Backend, &n.DownloadCount)
	return n, translate(err)
}

// CreateNode 建一个文件或文件夹节点；同层同名返回 ErrUniqueViolation。
func (s *Store) CreateNode(ctx context.Context, ownerID uuid.UUID, parentID *uuid.UUID, name string, isDir bool, size int64, blobID *uuid.UUID) (model.Node, error) {
	row := s.pool.QueryRow(ctx, `
INSERT INTO nodes (owner_id, parent_id, name, is_dir, size, blob_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING `+nodeColumns,
		ownerID, parentID, name, isDir, size, blobID)
	return scanNode(row)
}

// GetNode 取节点；owner_id 一并进 WHERE，别人的节点返回 ErrNotFound。
func (s *Store) GetNode(ctx context.Context, ownerID, id uuid.UUID) (model.Node, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+nodeColumns+` FROM nodes WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL`,
		id, ownerID)
	return scanNode(row)
}

// ListChildren 列某目录的直接子项，parentID 为 nil 表示根目录。
// IS NOT DISTINCT FROM 使 parent_id IS NULL 与 parent_id = $2 共用一条语句。
func (s *Store) ListChildren(ctx context.Context, ownerID uuid.UUID, parentID *uuid.UUID) ([]model.Node, error) {
	rows, err := s.pool.Query(ctx, `
SELECT n.id, n.owner_id, n.parent_id, n.name, n.is_dir, n.size, n.blob_id, n.created_at, n.updated_at,
       COALESCE(b.backend, ''), COALESCE(b.download_count, 0)
  FROM nodes n
  LEFT JOIN blobs b ON b.id = n.blob_id
 WHERE n.owner_id = $1
   AND n.deleted_at IS NULL
   AND n.parent_id IS NOT DISTINCT FROM $2::uuid
 ORDER BY n.is_dir DESC, n.name`, ownerID, parentID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	nodes := make([]model.Node, 0, 16)
	for rows.Next() {
		n, err := scanNodeWithBlob(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, translate(rows.Err())
}

// ListFilesOptions 描述文件列表的分页、排序与搜索参数。
type ListFilesOptions struct {
	ParentID    *uuid.UUID
	FilterByDir bool   // true 时限制在 ParentID 目录内；搜索模式下默认全树搜索
	Query       string // 非空时按名称不区分大小写模糊搜索
	Type        string // all | file | dir
	Sort        string // name | size | updated_at | created_at
	Order       string // asc | desc
	Limit       int    // <= 0 表示不限制（兼容旧客户端）
	Offset      int
}

// QueryNodes 支持分页、排序、类型过滤和全树名称搜索，并顺带返回存储后端与下载热度。
func (s *Store) QueryNodes(ctx context.Context, ownerID uuid.UUID, opts ListFilesOptions) ([]model.Node, int64, error) {
	conds := []string{"n.owner_id = $1", "n.deleted_at IS NULL"}
	args := []any{ownerID}
	argIdx := 2

	if opts.FilterByDir {
		conds = append(conds, fmt.Sprintf("n.parent_id IS NOT DISTINCT FROM $%d::uuid", argIdx))
		args = append(args, opts.ParentID)
		argIdx++
	}
	if q := strings.TrimSpace(opts.Query); q != "" {
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
		conds = append(conds, fmt.Sprintf(`n.name ILIKE $%d ESCAPE '\'`, argIdx))
		args = append(args, "%"+escaped+"%")
		argIdx++
	}
	switch opts.Type {
	case "file":
		conds = append(conds, "NOT n.is_dir")
	case "dir":
		conds = append(conds, "n.is_dir")
	}

	where := strings.Join(conds, " AND ")

	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM nodes n WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, translate(err)
	}

	sortCol := "n.name"
	switch opts.Sort {
	case "size":
		sortCol = "n.size"
	case "updated_at":
		sortCol = "n.updated_at"
	case "created_at":
		sortCol = "n.created_at"
	}
	orderDir := "ASC"
	if strings.EqualFold(opts.Order, "desc") {
		orderDir = "DESC"
	}

	query := fmt.Sprintf(`
SELECT n.id, n.owner_id, n.parent_id, n.name, n.is_dir, n.size, n.blob_id, n.created_at, n.updated_at,
       COALESCE(b.backend, ''), COALESCE(b.download_count, 0)
  FROM nodes n
  LEFT JOIN blobs b ON b.id = n.blob_id
 WHERE %s
 ORDER BY n.is_dir DESC, %s %s, n.name ASC`, where, sortCol, orderDir)

	if opts.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
		args = append(args, opts.Limit, opts.Offset)
	} else if opts.Offset > 0 {
		query += fmt.Sprintf(" OFFSET $%d", argIdx)
		args = append(args, opts.Offset)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, translate(err)
	}
	defer rows.Close()

	nodes := make([]model.Node, 0, 16)
	for rows.Next() {
		n, err := scanNodeWithBlob(rows)
		if err != nil {
			return nil, 0, err
		}
		nodes = append(nodes, n)
	}
	return nodes, total, translate(rows.Err())
}

// ListAllFolders 返回用户所有存活文件夹，供移动对话框构建目录树选择器。
func (s *Store) ListAllFolders(ctx context.Context, ownerID uuid.UUID) ([]model.Node, error) {
	rows, err := s.pool.Query(ctx, `
SELECT `+nodeColumns+`
  FROM nodes
 WHERE owner_id = $1 AND is_dir AND deleted_at IS NULL
 ORDER BY name`, ownerID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	out := make([]model.Node, 0, 16)
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, translate(rows.Err())
}

// ListNodeAncestors 返回从根目录到目标节点的祖先链（按自顶向下顺序），供面包屑导航使用。
func (s *Store) ListNodeAncestors(ctx context.Context, ownerID, id uuid.UUID) ([]model.Node, error) {
	rows, err := s.pool.Query(ctx, `
WITH RECURSIVE chain AS (
    SELECT `+nodeColumns+`, 0 AS depth
      FROM nodes
     WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL
    UNION ALL
    SELECT n.id, n.owner_id, n.parent_id, n.name, n.is_dir, n.size, n.blob_id, n.created_at, n.updated_at, c.depth + 1
      FROM nodes n
      JOIN chain c ON n.id = c.parent_id
     WHERE n.owner_id = $2 AND n.deleted_at IS NULL
) CYCLE id SET is_cycle USING path
SELECT `+nodeColumns+` FROM chain ORDER BY depth DESC`, id, ownerID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	out := make([]model.Node, 0, 8)
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, translate(rows.Err())
}

// ListSubtreeWithBlobs 用单条递归 CTE 一次取出整棵目录树和对应 blob 位置，消除 ZIP 打包的 N+1 查询。
func (s *Store) ListSubtreeWithBlobs(ctx context.Context, ownerID, rootID uuid.UUID) ([]model.ZipEntry, error) {
	rows, err := s.pool.Query(ctx, `
WITH RECURSIVE tree AS (
    SELECT id, name::text AS rel_path, is_dir, updated_at, blob_id
      FROM nodes
     WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL
    UNION ALL
    SELECT n.id, t.rel_path || '/' || n.name, n.is_dir, n.updated_at, n.blob_id
      FROM nodes n
      JOIN tree t ON n.parent_id = t.id
     WHERE n.owner_id = $2 AND n.deleted_at IS NULL
) CYCLE id SET is_cycle USING path
SELECT t.rel_path, t.is_dir, t.updated_at, COALESCE(b.backend, ''), COALESCE(b.storage_key, '')
  FROM tree t
  LEFT JOIN blobs b ON b.id = t.blob_id
 ORDER BY t.rel_path`, rootID, ownerID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	entries := make([]model.ZipEntry, 0, 16)
	for rows.Next() {
		var e model.ZipEntry
		if err := rows.Scan(&e.RelPath, &e.IsDir, &e.UpdatedAt, &e.Backend, &e.StorageKey); err != nil {
			return nil, translate(err)
		}
		entries = append(entries, e)
	}
	return entries, translate(rows.Err())
}

// UpdateNode 改名和/或移动：name 为 nil 表示不改名，setParent 为 false 表示不动父目录。
// setParent 用于区分"移到根目录（parentID = nil）"与"不修改父目录"。
// 同层重名返回 ErrUniqueViolation，节点不存在返回 ErrNotFound。
func (s *Store) UpdateNode(ctx context.Context, ownerID, id uuid.UUID, name *string, parentID *uuid.UUID, setParent bool) (model.Node, error) {
	row := s.pool.QueryRow(ctx, `
UPDATE nodes SET name       = COALESCE($3, name),
                 parent_id  = CASE WHEN $4 THEN $5::uuid ELSE parent_id END,
                 updated_at = now()
 WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL
RETURNING `+nodeColumns,
		id, ownerID, name, setParent, parentID)
	return scanNode(row)
}

// SoftDeleteSubtree 软删一个节点及其整棵子树（递归 CTE 一条语句完成），并记录 deleted_root_id 供回收站恢复。
// 唯一索引带 WHERE deleted_at IS NULL，删除后可立即重建同名。
// 节点不存在或已被删返回 ErrNotFound。
func (s *Store) SoftDeleteSubtree(ctx context.Context, ownerID, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
WITH RECURSIVE subtree AS (
    SELECT id FROM nodes WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL
    UNION ALL
    SELECT n.id FROM nodes n JOIN subtree ON n.parent_id = subtree.id
     WHERE n.owner_id = $2 AND n.deleted_at IS NULL
)
UPDATE nodes SET deleted_at = now(), updated_at = now(), deleted_root_id = $1
 WHERE id IN (SELECT id FROM subtree)`, id, ownerID)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// IsSelfOrDescendant 判断 candidate 是 ancestor 自身还是其后代。
// 任一节点不存在时返回 (false, nil)。
func (s *Store) IsSelfOrDescendant(ctx context.Context, ownerID, candidateID, ancestorID uuid.UUID) (bool, error) {
	const query = `
	WITH RECURSIVE ancestors AS (
		SELECT id, parent_id
		FROM nodes
		WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL
		UNION ALL
		SELECT n.id, n.parent_id
		FROM nodes n
		JOIN ancestors a ON n.id = a.parent_id
		WHERE n.owner_id = $2 AND n.deleted_at IS NULL
	) CYCLE id SET is_cycle USING path
	SELECT EXISTS (
		SELECT 1 FROM ancestors WHERE id = $3
	)`

	var isDescendant bool
	err := s.pool.QueryRow(ctx, query, candidateID, ownerID, ancestorID).Scan(&isDescendant)
	if err != nil {
		return false, translate(err)
	}

	return isDescendant, nil
}
