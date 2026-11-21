package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/MiraiNoShiori/netdisk/internal/httpx"
	"github.com/MiraiNoShiori/netdisk/internal/service"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// createShareRequest 是 POST /shares 的请求体，expires_in_seconds 缺省或 0 表示永不过期。
type createShareRequest struct {
	NodeID           string `json:"node_id"`
	ExpiresInSeconds int64  `json:"expires_in_seconds"`
	Password         string `json:"password"`
	MaxDownloads     *int64 `json:"max_downloads"`
}

// createShare 给自己的一个节点建分享链接，支持可选提取码与最大下载次数。
func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		httpx.Fail(w, r, s.deps.Logger, httpx.Unauthorized("authentication required"))
		return
	}

	var req createShareRequest
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("请求体不是合法的 JSON").With(err))
		return
	}
	nodeID, err := uuid.Parse(req.NodeID)
	if err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("node_id 必须是 UUID"))
		return
	}
	if req.ExpiresInSeconds < 0 || req.ExpiresInSeconds > 315360000 {
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("expires_in_seconds 必须在 0 到 315360000 之间"))
		return
	}

	view, err := s.deps.Shares.Create(r.Context(), service.ShareInput{
		OwnerID:      uid,
		NodeID:       nodeID,
		ExpiresIn:    time.Duration(req.ExpiresInSeconds) * time.Second,
		Password:     req.Password,
		MaxDownloads: req.MaxDownloads,
	})
	if err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"share": view.Share, "url": view.URL})
}

// listShares 列当前用户创建的分享。
func (s *Server) listShares(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		httpx.Fail(w, r, s.deps.Logger, httpx.Unauthorized("authentication required"))
		return
	}

	shares, err := s.deps.Shares.List(r.Context(), uid)
	if err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"shares": shares})
}

// shareAnalytics 返回单条分享链接的访问趋势与明细日志。
func (s *Server) shareAnalytics(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		httpx.Fail(w, r, s.deps.Logger, httpx.Unauthorized("authentication required"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.NotFound("no such share"))
		return
	}
	analytics, err := s.deps.Shares.Analytics(r.Context(), uid, id)
	if err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"analytics": analytics})
}

// deleteShare 撤销一条分享。
func (s *Server) deleteShare(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		httpx.Fail(w, r, s.deps.Logger, httpx.Unauthorized("authentication required"))
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.NotFound("no such share"))
		return
	}
	if err := s.deps.Shares.Revoke(r.Context(), uid, id); err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusNoContent, nil)
}

func (s *Server) shareCookieName(token string) string {
	prefix := token
	if len(prefix) > 16 {
		prefix = prefix[:16]
	}
	return "netdisk_share_" + prefix
}

func (s *Server) signShareUnlock(token, pwHash string) string {
	secret := "netdisk-share-cookie"
	if s.deps.Cfg != nil && s.deps.Cfg.JWTSecret != "" {
		secret = s.deps.Cfg.JWTSecret
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(token))
	mac.Write([]byte(":"))
	mac.Write([]byte(pwHash))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) shareAccessMeta(r *http.Request, token string) service.ShareAccessMeta {
	pw := strings.TrimSpace(r.Header.Get("X-Share-Password"))
	if pw == "" {
		pw = strings.TrimSpace(r.URL.Query().Get("password"))
	}
	verified := false
	if pw == "" {
		if c, err := r.Cookie(s.shareCookieName(token)); err == nil && c.Value != "" {
			if fp, err := s.deps.Shares.SharePasswordFingerprint(r.Context(), token); err == nil && fp != "" {
				want := s.signShareUnlock(token, fp)
				if hmac.Equal([]byte(c.Value), []byte(want)) {
					verified = true
				}
			}
		}
	}
	return service.ShareAccessMeta{
		Password:         pw,
		PasswordVerified: verified,
		ClientIP:         clientIP(r),
		UserAgent:        r.UserAgent(),
	}
}

// verifyShare 验证受密码保护的分享提取码，并下发 HMAC 签名 HttpOnly Cookie 以便直接浏览器下载。
func (s *Server) verifyShare(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("请求体不是合法的 JSON").With(err))
		return
	}
	share, err := s.deps.Shares.VerifyPassword(r.Context(), token, service.ShareAccessMeta{
		Password:  req.Password,
		ClientIP:  clientIP(r),
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		s.failService(w, r, err)
		return
	}
	if share.HasPassword && share.PasswordHash != nil {
		http.SetCookie(w, &http.Cookie{
			Name:     s.shareCookieName(token),
			Value:    s.signShareUnlock(token, *share.PasswordHash),
			Path:     "/api/v1/share/" + token,
			MaxAge:   3600,
			HttpOnly: true,
			Secure:   r.TLS != nil,
			SameSite: http.SameSiteStrictMode,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"verified": true})
}

// resolveShare 匿名打开一条分享，返回目标节点元信息，目录时连直接子项一起返回。
func (s *Server) resolveShare(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	var target *uuid.UUID
	if raw := r.URL.Query().Get("node_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httpx.Fail(w, r, s.deps.Logger, httpx.NotFound("no such file"))
			return
		}
		target = &id
	}
	view, err := s.deps.Shares.ResolveWithAccess(r.Context(), token, target, s.shareAccessMeta(r, token))
	if err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"node":           view.Node,
		"children":       view.Children,
		"expires_at":     view.ExpiresAt,
		"max_downloads":  view.MaxDownloads,
		"download_count": view.DownloadCount,
	})
}

// downloadViaShare 匿名下载；?node_id=<uuid> 可选，分享目录时指定下载的子项，支持 ?inline=1 安全预览。
func (s *Server) downloadViaShare(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	var target *uuid.UUID
	if raw := r.URL.Query().Get("node_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("node_id 必须是 UUID"))
			return
		}
		target = &id
	}

	dl, err := s.deps.Shares.OpenDownloadWithAccess(r.Context(), token, target, s.shareAccessMeta(r, token))
	if err != nil {
		s.failService(w, r, err)
		return
	}
	defer dl.Content.Close()

	serveDownload(w, r, dl)
}
