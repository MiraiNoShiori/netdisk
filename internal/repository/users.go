package repository

import (
	"context"

	"github.com/Miraitowa-kawayi/netdisk/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const userColumns = `id, username, password_hash, nickname, bio, email, quota_bytes, created_at, updated_at`

func scanUser(row pgx.Row) (model.User, error) {
	var u model.User
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Nickname, &u.Bio, &u.Email, &u.QuotaBytes, &u.CreatedAt, &u.UpdatedAt)
	return u, translate(err)
}

// CreateUser 插入新用户。用户名被占用时返回 ErrUniqueViolation。
func (s *Store) CreateUser(ctx context.Context, username, passwordHash, nickname string) (model.User, error) {
	row := s.pool.QueryRow(ctx,
		`INSERT INTO users (username, password_hash, nickname)
		 VALUES ($1, $2, $3)
		 RETURNING `+userColumns,
		username, passwordHash, nickname)
	return scanUser(row)
}

// CreateUserWithQuota 插入新用户并指定邮箱与配额上限。
func (s *Store) CreateUserWithQuota(ctx context.Context, username, passwordHash, nickname, email string, quotaBytes int64) (model.User, error) {
	row := s.pool.QueryRow(ctx,
		`INSERT INTO users (username, password_hash, nickname, email, quota_bytes)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING `+userColumns,
		username, passwordHash, nickname, email, quotaBytes)
	return scanUser(row)
}

// GetUserByUsername 按用户名查询（登录用），查不到返回 ErrNotFound。
func (s *Store) GetUserByUsername(ctx context.Context, username string) (model.User, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE username = $1`, username)
	return scanUser(row)
}

// GetUserByID 按 id 查询，查不到返回 ErrNotFound。
func (s *Store) GetUserByID(ctx context.Context, id uuid.UUID) (model.User, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id)
	return scanUser(row)
}
