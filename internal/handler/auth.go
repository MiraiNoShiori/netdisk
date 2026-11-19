package handler

import (
	"net/http"

	"github.com/Miraitowa-kawayi/netdisk/internal/httpx"
)

type registerRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Nickname string `json:"nickname"`
	Email    string `json:"email"`
}

func (s *Server) authRegister(w http.ResponseWriter, r *http.Request) {
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
	http.SetCookie(w, &http.Cookie{Name: "netdisk_download", Value: tok.Token, Path: "/api/v1", Expires: tok.ExpiresAt, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
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
	http.SetCookie(w, &http.Cookie{Name: "netdisk_download", Value: tok.Token, Path: "/api/v1", Expires: tok.ExpiresAt, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"token":              tok.Token,
		"token_type":         "Bearer",
		"expires_at":         tok.ExpiresAt,
		"refresh_token":      tok.RefreshToken,
		"refresh_expires_at": tok.RefreshExpiresAt,
		"user":               user,
	})
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
