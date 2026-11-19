package handler

import (
	"github.com/Miraitowa-kawayi/netdisk/internal/httpx"
	"github.com/go-chi/chi/v5"
	"net/http"
)

func (s *Server) createPeer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("无效请求"))
		return
	}
	owner, _ := userID(r)
	room, err := s.deps.Peers.Create(owner, req.Name, req.Size)
	if err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, 201, map[string]any{"share": room, "url": "/p2p.html#" + room.Token})
}
func (s *Server) listPeers(w http.ResponseWriter, r *http.Request) {
	owner, _ := userID(r)
	httpx.WriteJSON(w, 200, map[string]any{"shares": s.deps.Peers.List(owner)})
}
func (s *Server) getPeer(w http.ResponseWriter, r *http.Request) {
	owner, _ := userID(r)
	room, err := s.deps.Peers.Get(owner, chi.URLParam(r, "id"))
	if err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, room)
}
func (s *Server) deletePeer(w http.ResponseWriter, r *http.Request) {
	owner, _ := userID(r)
	if err := s.deps.Peers.Delete(owner, chi.URLParam(r, "id")); err != nil {
		s.failService(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) offerPeer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SDP string `json:"sdp"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("无效 SDP"))
		return
	}
	owner, _ := userID(r)
	if err := s.deps.Peers.Offer(owner, chi.URLParam(r, "id"), req.SDP); err != nil {
		s.failService(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) resolvePeer(w http.ResponseWriter, r *http.Request) {
	room, err := s.deps.Peers.Resolve(chi.URLParam(r, "token"))
	if err != nil {
		s.failService(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, room)
}
func (s *Server) answerPeer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SDP string `json:"sdp"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid("无效 SDP"))
		return
	}
	if err := s.deps.Peers.Answer(chi.URLParam(r, "token"), req.SDP); err != nil {
		s.failService(w, r, err)
		return
	}
	w.WriteHeader(204)
}
