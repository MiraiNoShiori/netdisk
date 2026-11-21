package handler

import (
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/MiraiNoShiori/netdisk/internal/httpx"
)

type registerRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Nickname string `json:"nickname"`
	Email    string `json:"email"`
}

func (s *Server) authRegister(w http.ResponseWriter, r *http.Request) {
	if s.deps.Cfg != nil && !s.deps.Cfg.AllowRegister {
		httpx.Fail(w, r, s.deps.Logger, httpx.Forbidden("已关闭公开注册"))
		return
	}
	var req registerRequest
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("请求体不是合法的 JSON").With(err))
		return
	}

	user, err := s.deps.Auth.RegisterWithEmail(r.Context(), req.Username, req.Password, req.Nickname, req.Email)
	if err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"user": user})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("请求体不是合法的 JSON").With(err))
		return
	}

	user, tok, err := s.deps.Auth.LoginWithRefresh(r.Context(), req.Username, req.Password)
	if err != nil {
		s.failService(w, r, err)
		return
	}
	s.setDownloadCookie(w, r, tok.Token, tok.ExpiresAt, 0)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"token":              tok.Token,
		"token_type":         "Bearer",
		"expires_at":         tok.ExpiresAt,
		"refresh_token":      tok.RefreshToken,
		"refresh_expires_at": tok.RefreshExpiresAt,
		"user":               user,
	})
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (s *Server) authRefresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("请求体不是合法的 JSON").With(err))
		return
	}
	user, tok, err := s.deps.Auth.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		s.failService(w, r, err)
		return
	}
	s.setDownloadCookie(w, r, tok.Token, tok.ExpiresAt, 0)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"token":              tok.Token,
		"token_type":         "Bearer",
		"expires_at":         tok.ExpiresAt,
		"refresh_token":      tok.RefreshToken,
		"refresh_expires_at": tok.RefreshExpiresAt,
		"user":               user,
	})
}

func (s *Server) setDownloadCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: "netdisk_download", Value: token, Path: "/api/v1", Expires: expires, MaxAge: maxAge,
		HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) secureCookie(r *http.Request) bool {
	cfg := s.deps.Cfg
	if cfg == nil {
		return r.TLS != nil
	}
	switch cfg.CookieSecure {
	case "true":
		return true
	case "false":
		return false
	}
	if r.TLS != nil {
		return true
	}
	if !strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return false
	}
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	for _, prefix := range cfg.TrustedProxyCIDRs {
		if prefix.Contains(peer.Addr().Unmap()) {
			return true
		}
	}
	return false
}

func (s *Server) authMe(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(r)
	if !ok {
		httpx.Fail(w, r, s.deps.Logger, httpx.Unauthorized("authentication required"))
		return
	}

	user, err := s.deps.Auth.Me(r.Context(), id)
	if err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (s *Server) authStats(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(r)
	if !ok {
		httpx.Fail(w, r, s.deps.Logger, httpx.Unauthorized("authentication required"))
		return
	}
	stats, err := s.deps.Auth.Stats(r.Context(), id)
	if err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stats": stats})
}
