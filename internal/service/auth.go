package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/Miraitowa-kawayi/netdisk/internal/model"
	"github.com/Miraitowa-kawayi/netdisk/internal/repository"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	minUsernameLen    = 3
	maxUsernameLen    = 32
	minPasswordLen    = 8
	maxPasswordLen    = 72 // bcrypt 只看前 72 字节
	defaultRefreshTTL = 7 * 24 * time.Hour
	defaultQuotaBytes = int64(10) << 30 // 10 GiB
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// dummyHash 供"用户不存在"路径也走一次 bcrypt，使两条失败路径耗时一致。
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("netdisk-dummy-password"), bcrypt.DefaultCost)

// Auth 负责注册、登录、会话刷新与账号资料/统计管理。
type Auth struct {
	store             *repository.Store
	tokens            *Tokens
	refreshTTL        time.Duration
	defaultQuotaBytes int64
}

// AuthTokens 封装 access token 与 refresh token 及其到期时间。
type AuthTokens struct {
	Token            string
	ExpiresAt        time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
}

func NewAuth(store *repository.Store, tokens *Tokens) *Auth {
	return &Auth{
		store:             store,
		tokens:            tokens,
		refreshTTL:        defaultRefreshTTL,
		defaultQuotaBytes: defaultQuotaBytes,
	}
}

// NewAuthWithOptions 支持配置 refresh token 有效期与新用户默认配额。
func NewAuthWithOptions(store *repository.Store, tokens *Tokens, refreshTTL time.Duration, defaultQuota int64) *Auth {
	if refreshTTL <= 0 {
		refreshTTL = defaultRefreshTTL
	}
	if defaultQuota < 0 {
		defaultQuota = defaultQuotaBytes
	}
	return &Auth{
		store:             store,
		tokens:            tokens,
		refreshTTL:        refreshTTL,
		defaultQuotaBytes: defaultQuota,
	}
}

// Tokens 暴露给 router 装鉴权中间件。
func (a *Auth) Tokens() *Tokens { return a.tokens }

// Register 建用户（兼容旧签名）。用户名已存在返回 ErrUsernameTaken。
func (a *Auth) Register(ctx context.Context, username, password, nickname string) (model.User, error) {
	return a.RegisterWithEmail(ctx, username, password, nickname, "")
}

// RegisterWithEmail 建用户并支持可选邮箱绑定。
func (a *Auth) RegisterWithEmail(ctx context.Context, username, password, nickname, email string) (model.User, error) {
	username = strings.TrimSpace(username)
	if err := validateCredentials(username, password); err != nil {
		return model.User{}, err
	}
	cleanedEmail, err := cleanEmail(email)
	if err != nil {
		return model.User{}, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return model.User{}, err
	}

	user, err := a.store.CreateUserWithQuota(ctx, username, string(hash), strings.TrimSpace(nickname), cleanedEmail, a.defaultQuotaBytes)
	if errors.Is(err, repository.ErrUniqueViolation) {
		return model.User{}, ErrUsernameTaken
	}
	if err != nil {
		return model.User{}, err
	}
	return user, nil
}

// Login 校验凭证并签发 token（兼容旧签名）。
func (a *Auth) Login(ctx context.Context, username, password string) (model.User, string, time.Time, error) {
	user, tok, err := a.LoginWithRefresh(ctx, username, password)
	if err != nil {
		return model.User{}, "", time.Time{}, err
	}
	return user, tok.Token, tok.ExpiresAt, nil
}

// LoginWithRefresh 校验凭证并签发 access token + refresh token 对。
func (a *Auth) LoginWithRefresh(ctx context.Context, username, password string) (model.User, AuthTokens, error) {
	user, err := a.store.GetUserByUsername(ctx, strings.TrimSpace(username))
	if errors.Is(err, repository.ErrNotFound) {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password)) // 拉平两条路径的耗时
		return model.User{}, AuthTokens{}, ErrInvalidCredentials
	}
	if err != nil {
		return model.User{}, AuthTokens{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return model.User{}, AuthTokens{}, ErrInvalidCredentials
	}

	token, expiresAt, err := a.tokens.Issue(user.ID)
	if err != nil {
		return model.User{}, AuthTokens{}, err
	}
	refreshRaw, err := newRefreshToken()
	if err != nil {
		return model.User{}, AuthTokens{}, err
	}
	refreshExp := time.Now().Add(a.refreshTTL)
	if err := a.store.CreateAuthSessionWithRefresh(ctx, tokenHash(token), user.ID, expiresAt, tokenHash(refreshRaw), refreshExp); err != nil {
		return model.User{}, AuthTokens{}, err
	}
	return user, AuthTokens{
		Token:            token,
		ExpiresAt:        expiresAt,
		RefreshToken:     refreshRaw,
		RefreshExpiresAt: refreshExp,
	}, nil
}

// Refresh 消费 refresh token 并原子轮换出一对新的 access + refresh token。
func (a *Auth) Refresh(ctx context.Context, refreshToken string) (model.User, AuthTokens, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return model.User{}, AuthTokens{}, ErrInvalidCredentials
	}

	// 先查出对应会话是否有效无法直接获取 userID 直到原子消费；用两步安全方式：
	// 先生成新 refresh token，再在一个事务中由 RotateRefreshSession 原子删旧插新。
	// 因为 JWT 需要 userID，所以先根据旧会话拿 userID，再在事务里用旧 refresh_token_hash 做 CAS 删除并写入新 token。
	// 若并发刷新，只有一个事务的 DELETE 会命中，另一个返回 ErrNotFound -> ErrInvalidCredentials。
	newRefresh, err := newRefreshToken()
	if err != nil {
		return model.User{}, AuthTokens{}, err
	}
	newRefreshExp := time.Now().Add(a.refreshTTL)

	// 使用临时占位无法直接签发 JWT，因此先查找有效会话对应的 userID，再签发 JWT 并原子轮换。
	userID, err := a.lookupRefreshUser(ctx, tokenHash(refreshToken))
	if err != nil {
		return model.User{}, AuthTokens{}, err
	}
	newAccess, newAccessExp, err := a.tokens.Issue(userID)
	if err != nil {
		return model.User{}, AuthTokens{}, err
	}
	rotatedUserID, err := a.store.RotateRefreshSession(ctx, tokenHash(refreshToken), tokenHash(newAccess), newAccessExp, tokenHash(newRefresh), newRefreshExp)
	if errors.Is(err, repository.ErrNotFound) {
		return model.User{}, AuthTokens{}, ErrInvalidCredentials
	}
	if err != nil {
		return model.User{}, AuthTokens{}, err
	}
	user, err := a.Me(ctx, rotatedUserID)
	if err != nil {
		return model.User{}, AuthTokens{}, err
	}
	return user, AuthTokens{
		Token:            newAccess,
		ExpiresAt:        newAccessExp,
		RefreshToken:     newRefresh,
		RefreshExpiresAt: newRefreshExp,
	}, nil
}

// Me 取当前 token 对应的用户。
func (a *Auth) Me(ctx context.Context, userID uuid.UUID) (model.User, error) {
	user, err := a.store.GetUserByID(ctx, userID)
	if errors.Is(err, repository.ErrNotFound) {
		// token 有效但用户已被删：按未认证处理
		return model.User{}, ErrInvalidCredentials
	}
	return user, err
}

func newRefreshToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "rt_" + hex.EncodeToString(b[:]), nil
}

func cleanEmail(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if len(raw) > 254 {
		return "", invalid("邮箱最长 254 字节")
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil || addr.Address != raw || !strings.Contains(raw, "@") {
		return "", invalid("邮箱格式不正确")
	}
	return strings.ToLower(raw), nil
}

func validateCredentials(username, password string) error {
	if l := len(username); l < minUsernameLen || l > maxUsernameLen {
		return invalid("用户名长度必须在 %d–%d 之间", minUsernameLen, maxUsernameLen)
	}
	if !usernamePattern.MatchString(username) {
		return invalid("用户名只能包含字母、数字、下划线和连字符")
	}
	if l := len(password); l < minPasswordLen || l > maxPasswordLen {
		return invalid("密码长度必须在 %d–%d 之间", minPasswordLen, maxPasswordLen)
	}
	return nil
}
