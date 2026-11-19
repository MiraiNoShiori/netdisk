package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Miraitowa-kawayi/netdisk/internal/httpx"
	"github.com/google/uuid"
)

// TokenParser 由 service.Auth 实现，使 middleware 不必 import 业务包。
type TokenParser interface {
	Authenticate(context.Context, string) (uuid.UUID, error)
}

type ctxKey int

const (
	userIDKey ctxKey = iota
	rawTokenKey
)

// RequireAuth 校验 Authorization: Bearer <token>，通过后把用户 id 与原始 token 放进 context；
// 失败一律 401，不区分缺少、签名错误或过期。
func RequireAuth(logger *slog.Logger, tp TokenParser) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
				if c, err := r.Cookie("netdisk_download"); err == nil {
					raw = c.Value
					ok = true
				}
			}
			if !ok {
				httpx.Fail(w, r, logger, httpx.Unauthorized("missing or malformed Authorization header"))
				return
			}
			id, err := tp.Authenticate(r.Context(), raw)
			if err != nil {
				httpx.Fail(w, r, logger, httpx.Unauthorized("invalid or expired token"))
				return
			}
			ctx := context.WithValue(r.Context(), userIDKey, id)
			ctx = context.WithValue(ctx, rawTokenKey, raw)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserIDFromContext 取出 RequireAuth 放进去的用户 id。
func UserIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userIDKey).(uuid.UUID)
	return id, ok && id != uuid.Nil
}

// TokenFromContext 取出 RequireAuth 验证通过的原始 token。
func TokenFromContext(ctx context.Context) (string, bool) {
	raw, ok := ctx.Value(rawTokenKey).(string)
	return raw, ok && raw != ""
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	return token, token != ""
}
