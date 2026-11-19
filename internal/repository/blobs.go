package repository

import (
	"context"

	"github.com/Miraitowa-kawayi/netdisk/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const blobColumns = `id, content_hash, size, backend, storage_key, download_count, last_accessed_at, created_at`

func scanBlob(row pgx.Row) (model.Blob, error) {
	var b model.Blob
	err := row.Scan(&b.ID, &b.ContentHash, &b.Size, &b.Backend, &b.StorageKey, &b.DownloadCount, &b.LastAccessedAt, &b.CreatedAt)
	return b, translate(err)
}

// GetBlobByHash 按内容哈希查询，查不到返回 ErrNotFound。
func (s *Store) GetBlobByHash(ctx context.Context, hash string) (model.Blob, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+blobColumns+` FROM blobs WHERE content_hash = $1`, hash)
	return scanBlob(row)
}

// GetBlobByID 按 id 查询内容（下载时由 node.blob_id 反查）。
func (s *Store) GetBlobByID(ctx context.Context, id uuid.UUID) (model.Blob, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+blobColumns+` FROM blobs WHERE id = $1`, id)
	return scanBlob(row)
}

// UpsertBlob 登记一份内容：同 hash 的行已存在则复用，否则插入。
// recorded 表示本次插入了新行；false 时调用方应删掉刚写出的副本。
// 并发上传同一内容时只有一个赢家，输的一方拿到赢家的行而非唯一键错误。
func (s *Store) UpsertBlob(ctx context.Context, hash string, size int64, backend, storageKey string) (blob model.Blob, recorded bool, err error) {
	const q = `INSERT INTO blobs(content_hash,size,backend,storage_key) VALUES($1,$2,$3,$4)
 ON CONFLICT(content_hash) DO UPDATE SET content_hash=EXCLUDED.content_hash
 RETURNING ` + blobColumns + `, (xmax=0) AS recorded`

	row := s.pool.QueryRow(ctx, q, hash, size, backend, storageKey)
	err = row.Scan(&blob.ID, &blob.ContentHash, &blob.Size, &blob.Backend, &blob.StorageKey, &blob.DownloadCount, &blob.LastAccessedAt, &blob.CreatedAt, &recorded)
	return blob, recorded, translate(err)
}

// RecordBlobDownload 累加内容块访问热度与最近访问时间，供热点文件智能分层策略使用。
func (s *Store) RecordBlobDownload(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
UPDATE blobs
   SET download_count = download_count + 1,
       last_accessed_at = now()
 WHERE id = $1`, id)
	return translate(err)
}

// ListHotLocalBlobs 查找满足下载阈值且仍在 local 后端的内容块（按访问频次降序）。
func (s *Store) ListHotLocalBlobs(ctx context.Context, threshold, minSize int64, limit int) ([]model.Blob, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
SELECT `+blobColumns+`
  FROM blobs
 WHERE backend = 'local'
   AND download_count >= $1
   AND size >= $2
 ORDER BY download_count DESC, size DESC
 LIMIT $3`, threshold, minSize, limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	out := make([]model.Blob, 0, limit)
	for rows.Next() {
		b, err := scanBlob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, translate(rows.Err())
}

// DeleteBlobIfUnreferenced 在没有任何节点（含回收站）引用时删除 blob 行，返回是否删除。
// 用 NOT EXISTS 现算而非维护 ref_count，避免计数漂移导致内容永远删不掉。
func (s *Store) DeleteBlobIfUnreferenced(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
DELETE FROM blobs
 WHERE id = $1
   AND NOT EXISTS (SELECT 1 FROM nodes WHERE blob_id = $1)`, id)
	if err != nil {
		return false, translate(err)
	}
	return tag.RowsAffected() > 0, nil
}

// ReclaimOrphanBlobs 回收一次删除所波及的内容，返回可从磁盘删除的 storage_key。
// 沿 parent_id 展开被软删的子树，收集其中的 blob，只删已无任何存活节点引用的那些
// （存活判定是全局的，不带 owner_id）。无内容可回收时返回空切片 + nil。
// 节点侧的 blob_id 指针由外键 ON DELETE SET NULL 清掉（见 migrations/0002）。
type BlobLocation struct{ Backend, Key string }

func (s *Store) ReclaimOrphanLocations(ctx context.Context, ownerID, rootID uuid.UUID) ([]BlobLocation, error) {
	query := `
	WITH RECURSIVE subtree AS (
	    SELECT id, blob_id
		FROM nodes
		WHERE id = $1 AND owner_id = $2
	    UNION ALL
	    SELECT n.id, n.blob_id
		FROM nodes n
		JOIN subtree s ON n.parent_id = s.id
	),
	victims AS (
	    SELECT DISTINCT blob_id
		FROM subtree
		WHERE blob_id IS NOT NULL
	)
	DELETE FROM blobs
	WHERE id IN (SELECT blob_id FROM victims)
	  AND NOT EXISTS (
	  SELECT 1
	  FROM nodes
	  WHERE nodes.blob_id = blobs.id AND nodes.deleted_at IS NULL
	)
	RETURNING backend, storage_key`
	keys := make([]BlobLocation, 0)
	rows, err := s.pool.Query(ctx, query, rootID, ownerID)
	if err != nil && err != pgx.ErrNoRows {
		return nil, translate(err)
	}
	defer rows.Close()
	for rows.Next() {
		var key BlobLocation
		if err := rows.Scan(&key.Backend, &key.Key); err != nil {
			return nil, translate(err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err)
	}
	return keys, nil
}
