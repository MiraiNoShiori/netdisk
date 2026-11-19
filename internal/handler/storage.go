package handler

import (
	"errors"
	"io"
	"net/http"

	"github.com/Miraitowa-kawayi/netdisk/internal/httpx"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (s *Server) migrateFile(w http.ResponseWriter, r *http.Request) {
	owner, _ := userID(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("id 必须为 UUID"))
		return
	}
	var req struct {
		Backend string `json:"backend"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("请求体不是合法 JSON"))
		return
	}
	if err := s.deps.Files.Migrate(r.Context(), owner, id, req.Backend); err != nil {
		s.failService(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) directURL(w http.ResponseWriter, r *http.Request) {
	owner, _ := userID(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("id 必须为 UUID"))
		return
	}
	url, err := s.deps.Files.DirectURL(r.Context(), owner, id)
	if err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"url": url, "expires_in_seconds": 300})
}

// storagePolicy 返回服务端可用存储后端和热点自动分层配置。
func (s *Server) storagePolicy(w http.ResponseWriter, r *http.Request) {
	var threshold, minSize int64
	if s.deps.Cfg != nil {
		threshold = s.deps.Cfg.AutoTierThreshold
		minSize = s.deps.Cfg.AutoTierMinSize
	}
	info := s.deps.Files.StoragePolicy(threshold, minSize)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"policy": info})
}

// runAutoTier 手动触发一次热点文件（local → s3）智能迁移扫描。
func (s *Server) runAutoTier(w http.ResponseWriter, r *http.Request) {
	threshold := int64(3)
	minSize := int64(0)
	if s.deps.Cfg != nil {
		if s.deps.Cfg.AutoTierThreshold > 0 {
			threshold = s.deps.Cfg.AutoTierThreshold
		}
		minSize = s.deps.Cfg.AutoTierMinSize
	}
	if r.Body != nil {
		var req struct {
			Threshold *int64 `json:"threshold"`
			MinSize   *int64 `json:"min_size"`
		}
		err := decodeJSON(w, r, &req)
		if err != nil && !errors.Is(err, io.EOF) {
			httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("请求体不是合法 JSON"))
			return
		}
		if req.Threshold != nil {
			if *req.Threshold < 1 {
				httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("threshold 必须大于等于 1"))
				return
			}
			threshold = *req.Threshold
		}
		if req.MinSize != nil {
			if *req.MinSize < 0 {
				httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("min_size 不能为负"))
				return
			}
			minSize = *req.MinSize
		}
	}

	migrated, err := s.deps.Files.AutoTierHotBlobs(r.Context(), threshold, minSize, 20)
	if err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"migrated":  migrated,
		"threshold": threshold,
		"min_size":  minSize,
	})
}
