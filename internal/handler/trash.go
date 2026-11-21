package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/MiraiNoShiori/netdisk/internal/httpx"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (s *Server) listTrash(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		httpx.Fail(w, r, s.deps.Logger, httpx.Unauthorized("authentication required"))
		return
	}
	items, err := s.deps.Files.ListTrash(r.Context(), uid)
	if err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) restoreTrash(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		httpx.Fail(w, r, s.deps.Logger, httpx.Unauthorized("authentication required"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.NotFound("no such trash item"))
		return
	}

	var targetParent *uuid.UUID
	overrideParent := false
	if r.Body != nil {
		var req struct {
			ParentID json.RawMessage `json:"parent_id"`
		}
		err := decodeJSON(w, r, &req)
		if err != nil && !errors.Is(err, io.EOF) {
			httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("请求体不是合法的 JSON").With(err))
			return
		}
		if req.ParentID != nil {
			overrideParent = true
			if !bytes.Equal(bytes.TrimSpace(req.ParentID), []byte("null")) {
				var raw string
				if err := json.Unmarshal(req.ParentID, &raw); err != nil {
					httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("parent_id 必须是字符串或 null"))
					return
				}
				p, err := parseParentID(raw)
				if err != nil {
					httpx.Fail(w, r, s.deps.Logger, httpx.Invalid(err.Error()))
					return
				}
				targetParent = p
			}
		}
	}

	node, err := s.deps.Files.RestoreTrash(r.Context(), uid, id, targetParent, overrideParent)
	if err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"node": node})
}

func (s *Server) purgeTrashItem(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		httpx.Fail(w, r, s.deps.Logger, httpx.Unauthorized("authentication required"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.NotFound("no such trash item"))
		return
	}
	if err := s.deps.Files.PurgeTrashItem(r.Context(), uid, id); err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusNoContent, nil)
}

func (s *Server) emptyTrash(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		httpx.Fail(w, r, s.deps.Logger, httpx.Unauthorized("authentication required"))
		return
	}
	if err := s.deps.Files.EmptyTrash(r.Context(), uid); err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusNoContent, nil)
}
