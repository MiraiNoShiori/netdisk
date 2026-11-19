package repository

import (
	"context"
	"github.com/google/uuid"
)

func (s *Store) QueueObject(ctx context.Context, backend, key string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO object_gc(backend,storage_key)VALUES($1,$2) ON CONFLICT DO NOTHING`, backend, key)
	return err
}
func (s *Store) Garbage(ctx context.Context) ([]BlobLocation, error) {
	rows, err := s.pool.Query(ctx, `SELECT backend,storage_key FROM object_gc ORDER BY created_at LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BlobLocation{}
	for rows.Next() {
		var l BlobLocation
		if err = rows.Scan(&l.Backend, &l.Key); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
func (s *Store) ForgetGarbage(ctx context.Context, backend, key string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM object_gc WHERE backend=$1 AND storage_key=$2`, backend, key)
	return err
}

// DeleteAccountAndQueue 与账号删除同一事务登记孤儿内容和未完成分片。
func (s *Store) DeleteAccountAndQueue(ctx context.Context, id uuid.UUID, passwordHash string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND password_hash=$2)`, id, passwordHash).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrNotFound
	}
	_, err = tx.Exec(ctx, `INSERT INTO object_gc(backend,storage_key) SELECT u.backend,p.storage_key FROM upload_sessions u JOIN upload_parts p ON p.session_id=u.id WHERE u.owner_id=$1 ON CONFLICT DO NOTHING`, id)
	if err != nil {
		return err
	}
	// 在删除节点前记录候选内容；被其他用户引用的仍保留。
	rows, err := tx.Query(ctx, `SELECT DISTINCT blob_id FROM nodes WHERE owner_id=$1 AND blob_id IS NOT NULL`, id)
	if err != nil {
		return err
	}
	ids := []uuid.UUID{}
	for rows.Next() {
		var b uuid.UUID
		if err = rows.Scan(&b); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM users WHERE id=$1 AND password_hash=$2`, id, passwordHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	_, err = tx.Exec(ctx, `WITH gone AS (DELETE FROM blobs WHERE id=ANY($1::uuid[]) AND NOT EXISTS(SELECT 1 FROM nodes WHERE blob_id=blobs.id) RETURNING backend,storage_key) INSERT INTO object_gc(backend,storage_key) SELECT backend,storage_key FROM gone ON CONFLICT DO NOTHING`, ids)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
