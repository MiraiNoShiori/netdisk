package handler

import (
	"net/http"

	"github.com/MiraiNoShiori/netdisk/internal/httpx"
	"github.com/MiraiNoShiori/netdisk/internal/middleware"
)

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	raw, ok := middleware.TokenFromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, s.deps.Logger, httpx.Unauthorized("authentication required"))
		return
	}
	if err := s.deps.Auth.Logout(r.Context(), raw); err != nil {
		s.failService(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "netdisk_download", Path: "/api/v1", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nickname string  `json:"nickname"`
		Bio      string  `json:"bio"`
		Email    *string `json:"email"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("无效资料"))
		return
	}
	id, _ := userID(r)
	u, err := s.deps.Auth.ProfileWithEmail(r.Context(), id, req.Nickname, req.Bio, req.Email)
	if err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"user": u})
}

func (s *Server) password(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("无效请求"))
		return
	}
	id, _ := userID(r)
	if err := s.deps.Auth.Password(r.Context(), id, req.Old, req.New); err != nil {
		s.failService(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "netdisk_download", Path: "/api/v1", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("无效请求"))
		return
	}
	id, _ := userID(r)
	if err := s.deps.Auth.Delete(r.Context(), id, req.Password); err != nil {
		s.failService(w, r, err)
		return
	}
	for _, room := range s.deps.Peers.List(id) {
		_ = s.deps.Peers.Delete(id, room.ID)
	}
	http.SetCookie(w, &http.Cookie{Name: "netdisk_download", Path: "/api/v1", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}
