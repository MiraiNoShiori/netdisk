package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/MiraiNoShiori/netdisk/internal/httpx"
)

// AuthLimit 只信任实际 TCP 来源，不接受客户端伪造的 X-Forwarded-For。
// 对登录/注册/刷新会话与匿名分享提取码验证施加每 IP 每分钟窗口限流。
func AuthLimit(logger *slog.Logger) func(http.Handler) http.Handler {
	type entry struct {
		count int
		until time.Time
	}
	var mu sync.Mutex
	entries := map[string]entry{}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bucket, limit := rateLimitBucket(r)
			if bucket == "" {
				next.ServeHTTP(w, r)
				return
			}
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			key := bucket + ":" + host
			now := time.Now()
			mu.Lock()
			e := entries[key]
			if !e.until.After(now) {
				e = entry{until: now.Add(time.Minute)}
			}
			if len(entries) >= 10000 {
				for k, v := range entries {
					if !v.until.After(now) {
						delete(entries, k)
					}
				}
			}
			blocked := e.count >= limit || (len(entries) >= 10000 && entries[key].until.IsZero())
			if !blocked {
				e.count++
				entries[key] = e
			}
			mu.Unlock()
			if blocked {
				w.Header().Set("Retry-After", "60")
				httpx.Fail(w, r, logger, httpx.New(429, httpx.CodeRateLimited, "请求过于频繁，请稍后重试"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func rateLimitBucket(r *http.Request) (string, int) {
	if r.Method == http.MethodPost {
		switch r.URL.Path {
		case "/api/v1/auth/login", "/api/v1/auth/register":
			return "auth", 20
		case "/api/v1/auth/refresh":
			return "refresh", 30
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/share/") && strings.HasSuffix(r.URL.Path, "/verify") {
			return "share-verify", 30
		}
	}
	return "", 0
}
