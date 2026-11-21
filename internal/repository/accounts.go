package repository

import (
	"context"
	"time"

	"github.com/MiraiNoShiori/netdisk/internal/model"
	"github.com/google/uuid"
)

func (s *Store) CreateAuthSession(ctx context.Context, hash string, user uuid.UUID, expires time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO auth_sessions(token_hash,user_id,expires_at)VALUES($1,$2,$3)`, hash, user, expires)
	return translate(err)
}

// CreateAuthSessionWithRefresh 记录 access token 与 refresh token 摘要，支持后续无感轮换。
func (s *Store) CreateAuthSessionWithRefresh(ctx context.Context, hash string, user uuid.UUID, expires time.Time, refreshHash string, refreshExpires time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO auth_sessions(token_hash,user_id,expires_at,refresh_token_hash,refresh_expires_at) VALUES($1,$2,$3,$4,$5)`,
		hash, user, expires, refreshHash, refreshExpires)
	return translate(err)
}

// LookupRefreshUser 根据 refresh_token_hash 查询未过期会话的 user_id。
func (s *Store) LookupRefreshUser(ctx context.Context, refreshHash string) (uuid.UUID, error) {
	var userID uuid.UUID
	err := s.pool.QueryRow(ctx,
		`SELECT user_id FROM auth_sessions WHERE refresh_token_hash = $1 AND refresh_expires_at > now()`,
		refreshHash).Scan(&userID)
	return userID, translate(err)
}

// RotateRefreshSession 原子消费旧 refresh token 并签发新会话（单次使用防重放）。
func (s *Store) RotateRefreshSession(ctx context.Context, oldRefreshHash, newTokenHash string, newExpires time.Time, newRefreshHash string, newRefreshExpires time.Time) (uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)

	var userID uuid.UUID
	err = tx.QueryRow(ctx,
		`DELETE FROM auth_sessions
		  WHERE refresh_token_hash = $1 AND refresh_expires_at > now()
		  RETURNING user_id`, oldRefreshHash).Scan(&userID)
	if err != nil {
		return uuid.Nil, translate(err)
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO auth_sessions(token_hash,user_id,expires_at,refresh_token_hash,refresh_expires_at) VALUES($1,$2,$3,$4,$5)`,
		newTokenHash, userID, newExpires, newRefreshHash, newRefreshExpires); err != nil {
		return uuid.Nil, translate(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return userID, nil
}

func (s *Store) SessionActive(ctx context.Context, hash string, user uuid.UUID) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>now())`, hash, user).Scan(&ok)
	return ok, translate(err)
}

func (s *Store) RevokeSession(ctx context.Context, hash string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM auth_sessions WHERE token_hash=$1`, hash)
	return translate(err)
}

// UpdateProfile 更新昵称、简介与可选邮箱；不传 email 时保留原值。
func (s *Store) UpdateProfile(ctx context.Context, id uuid.UUID, nickname, bio string, email ...string) (model.User, error) {
	if len(email) > 0 {
		return scanUser(s.pool.QueryRow(ctx,
			`UPDATE users SET nickname=$2, bio=$3, email=$4, updated_at=now() WHERE id=$1 RETURNING `+userColumns,
			id, nickname, bio, email[0]))
	}
	return scanUser(s.pool.QueryRow(ctx,
		`UPDATE users SET nickname=$2, bio=$3, updated_at=now() WHERE id=$1 RETURNING `+userColumns,
		id, nickname, bio))
}

func (s *Store) ChangePassword(ctx context.Context, id uuid.UUID, oldHash, newHash string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE users SET password_hash=$3,updated_at=now() WHERE id=$1 AND password_hash=$2`, id, oldHash, newHash)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	if _, err = tx.Exec(ctx, `DELETE FROM auth_sessions WHERE user_id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteAccount(ctx context.Context, id uuid.UUID, passwordHash string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM users WHERE id=$1 AND password_hash=$2`, id, passwordHash)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// GetUserUsageAndQuota 返回用户当前逻辑占用字节数（含回收站）与配额上限。
func (s *Store) GetUserUsageAndQuota(ctx context.Context, id uuid.UUID) (used int64, quota int64, err error) {
	err = s.pool.QueryRow(ctx, `
SELECT COALESCE((SELECT SUM(size) FROM nodes WHERE owner_id = $1 AND NOT is_dir), 0),
       u.quota_bytes
  FROM users u
 WHERE u.id = $1`, id).Scan(&used, &quota)
	return used, quota, translate(err)
}

// GetUserStats 汇总用户文件统计、回收站占用、各后端分布以及内容去重节省的空间。
func (s *Store) GetUserStats(ctx context.Context, id uuid.UUID) (model.UserStats, error) {
	var st model.UserStats
	var physicalUnique int64
	err := s.pool.QueryRow(ctx, `
SELECT
    u.quota_bytes,
    COALESCE((SELECT SUM(size) FROM nodes WHERE owner_id = $1 AND NOT is_dir AND deleted_at IS NULL), 0) AS live_bytes,
    COALESCE((SELECT SUM(size) FROM nodes WHERE owner_id = $1 AND NOT is_dir AND deleted_at IS NOT NULL), 0) AS trash_bytes,
    COALESCE((SELECT COUNT(*) FROM nodes WHERE owner_id = $1 AND NOT is_dir AND deleted_at IS NULL), 0) AS file_count,
    COALESCE((SELECT COUNT(*) FROM nodes WHERE owner_id = $1 AND is_dir AND deleted_at IS NULL), 0) AS dir_count,
    COALESCE((SELECT COUNT(*) FROM nodes WHERE owner_id = $1 AND deleted_at IS NOT NULL AND (deleted_root_id = id OR deleted_root_id IS NULL)), 0) AS trash_count,
    COALESCE((SELECT COUNT(*) FROM shares WHERE created_by = $1), 0) AS share_count,
    COALESCE((SELECT SUM(n.size) FROM nodes n JOIN blobs b ON b.id = n.blob_id WHERE n.owner_id = $1 AND NOT n.is_dir AND n.deleted_at IS NULL AND b.backend = 'local'), 0) AS local_bytes,
    COALESCE((SELECT SUM(n.size) FROM nodes n JOIN blobs b ON b.id = n.blob_id WHERE n.owner_id = $1 AND NOT n.is_dir AND n.deleted_at IS NULL AND b.backend = 's3'), 0) AS s3_bytes,
    COALESCE((SELECT SUM(b.size) FROM blobs b WHERE b.id IN (SELECT DISTINCT blob_id FROM nodes WHERE owner_id = $1 AND blob_id IS NOT NULL)), 0) AS unique_physical
  FROM users u
 WHERE u.id = $1`, id).Scan(
		&st.QuotaBytes,
		&st.LiveBytes,
		&st.TrashBytes,
		&st.FileCount,
		&st.DirCount,
		&st.TrashCount,
		&st.ShareCount,
		&st.LocalBytes,
		&st.S3Bytes,
		&physicalUnique,
	)
	if err != nil {
		return model.UserStats{}, translate(err)
	}
	st.UsedBytes = st.LiveBytes + st.TrashBytes
	if st.UsedBytes > physicalUnique {
		st.DedupSavedBytes = st.UsedBytes - physicalUnique
	}
	return st, nil
}
