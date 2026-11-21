package repository

import (
	"context"
	"github.com/MiraiNoShiori/netdisk/internal/model"
	"github.com/google/uuid"
)

// LockUpload 跨进程串行化同一会话的写片、完成、放弃和回收。
func (s *Store) LockUpload(ctx context.Context, id uuid.UUID) (func(), error) {
	conn, err := s.locks.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	_, err = conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, id.String())
	if err != nil {
		conn.Release()
		return nil, err
	}
	return func() {
		conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1,0))`, id.String())
		conn.Release()
	}, nil
}
func (s *Store) ExpiredUploads(ctx context.Context) ([]model.UploadSession, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+uploadSessionColumns+` FROM upload_sessions WHERE expires_at<=now() OR status<>'pending' ORDER BY expires_at LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.UploadSession{}
	for rows.Next() {
		v, err := scanUploadSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) DeleteUploadSession(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM upload_sessions WHERE id=$1`, id)
	return err
}
func (s *Store) CleanAuthSessions(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM auth_sessions WHERE expires_at<=now() AND (refresh_expires_at IS NULL OR refresh_expires_at<=now())`)
	return err
}
