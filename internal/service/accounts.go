package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/Miraitowa-kawayi/netdisk/internal/model"
	"github.com/Miraitowa-kawayi/netdisk/internal/repository"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func tokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (a *Auth) lookupRefreshUser(ctx context.Context, hash string) (uuid.UUID, error) {
	id, err := a.store.LookupRefreshUser(ctx, hash)
	if errors.Is(err, repository.ErrNotFound) {
		return uuid.Nil, ErrInvalidCredentials
	}
	return id, err
}

func (a *Auth) Authenticate(ctx context.Context, raw string) (uuid.UUID, error) {
	id, err := a.tokens.Parse(raw)
	if err != nil {
		return uuid.Nil, ErrInvalidCredentials
	}
	ok, err := a.store.SessionActive(ctx, tokenHash(raw), id)
	if err != nil {
		return uuid.Nil, err
	}
	if !ok {
		return uuid.Nil, ErrInvalidCredentials
	}
	return id, nil
}

func (a *Auth) Logout(ctx context.Context, raw string) error {
	return a.store.RevokeSession(ctx, tokenHash(raw))
}

func (a *Auth) Profile(ctx context.Context, id uuid.UUID, nickname, bio string) (model.User, error) {
	return a.ProfileWithEmail(ctx, id, nickname, bio, nil)
}

// ProfileWithEmail 更新昵称、简介和可选邮箱（email 为 nil 表示不修改邮箱）。
func (a *Auth) ProfileWithEmail(ctx context.Context, id uuid.UUID, nickname, bio string, email *string) (model.User, error) {
	if len(nickname) > 128 || len(bio) > 2048 {
		return model.User{}, invalid("昵称最多 128 字节，简介最多 2048 字节")
	}
	if email != nil {
		cleaned, err := cleanEmail(*email)
		if err != nil {
			return model.User{}, err
		}
		return a.store.UpdateProfile(ctx, id, strings.TrimSpace(nickname), strings.TrimSpace(bio), cleaned)
	}
	return a.store.UpdateProfile(ctx, id, strings.TrimSpace(nickname), strings.TrimSpace(bio))
}

// Stats 返回用户的配额、已用空间、回收站、分享与去重节省统计。
func (a *Auth) Stats(ctx context.Context, id uuid.UUID) (model.UserStats, error) {
	st, err := a.store.GetUserStats(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return model.UserStats{}, ErrInvalidCredentials
	}
	return st, err
}

func (a *Auth) Password(ctx context.Context, id uuid.UUID, old, new string) error {
	u, err := a.Me(ctx, id)
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(old)) != nil {
		return ErrInvalidCredentials
	}
	if err = validateCredentials(u.Username, new); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(new), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return a.store.ChangePassword(ctx, id, u.PasswordHash, string(hash))
}

func (a *Auth) Delete(ctx context.Context, id uuid.UUID, password string) error {
	u, err := a.Me(ctx, id)
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return ErrInvalidCredentials
	}
	return a.store.DeleteAccountAndQueue(ctx, id, u.PasswordHash)
}
