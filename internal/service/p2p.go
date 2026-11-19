package service

import (
	"github.com/google/uuid"
	"strings"
	"sync"
	"time"
)

type PeerShare struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Token     string    `json:"token"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	ExpiresAt time.Time `json:"expires_at"`
	Offer     string    `json:"offer,omitempty"`
	Answer    string    `json:"answer,omitempty"`
	Owner     uuid.UUID `json:"-"`
}

// Peers 只保存短期信令，重启即失效；文件字节永远不经过此服务。
type Peers struct {
	mu    sync.Mutex
	rooms map[string]PeerShare
	now   func() time.Time
}

func NewPeers() *Peers { return &Peers{rooms: map[string]PeerShare{}, now: time.Now} }
func (p *Peers) prune() {
	for id, r := range p.rooms {
		if !r.ExpiresAt.After(p.now()) {
			delete(p.rooms, id)
		}
	}
}
func (p *Peers) Create(owner uuid.UUID, name string, size int64) (PeerShare, error) {
	name, err := cleanName(name)
	if err != nil {
		return PeerShare{}, err
	}
	if size < 0 || size > 1<<40 {
		return PeerShare{}, invalid("文件大小超出范围")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	if len(p.rooms) >= 1024 {
		return PeerShare{}, invalid("信令服务容量已满，请稍后重试")
	}
	count := 0
	for _, r := range p.rooms {
		if r.Owner == owner {
			count++
		}
	}
	if count >= 10 {
		return PeerShare{}, invalid("每用户最多 10 个有效直传分享")
	}
	token, err := newShareToken()
	if err != nil {
		return PeerShare{}, err
	}
	r := PeerShare{ID: uuid.NewString(), Type: "p2p", Token: token, Owner: owner, Name: name, Size: size, ExpiresAt: p.now().Add(30 * time.Minute)}
	p.rooms[r.ID] = r
	return r, nil
}
func (p *Peers) List(owner uuid.UUID) []PeerShare {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	out := []PeerShare{}
	for _, r := range p.rooms {
		if r.Owner == owner {
			out = append(out, r)
		}
	}
	return out
}
func (p *Peers) Get(owner uuid.UUID, id string) (PeerShare, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	r, ok := p.rooms[id]
	if !ok || r.Owner != owner {
		return PeerShare{}, ErrNotFound
	}
	return r, nil
}
func (p *Peers) Delete(owner uuid.UUID, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.rooms[id]
	if !ok || r.Owner != owner {
		return ErrNotFound
	}
	delete(p.rooms, id)
	return nil
}
func validSDP(s string) bool { return len(s) > 0 && len(s) <= 48<<10 && strings.HasPrefix(s, "v=0") }
func (p *Peers) Offer(owner uuid.UUID, id, sdp string) error {
	if !validSDP(sdp) {
		return invalid("SDP 无效或超过 48 KiB")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	r, ok := p.rooms[id]
	if !ok || r.Owner != owner {
		return ErrNotFound
	}
	if r.Offer != "" && r.Offer != sdp {
		return ErrConflict
	}
	r.Offer = sdp
	p.rooms[id] = r
	return nil
}
func (p *Peers) Resolve(token string) (PeerShare, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	for _, r := range p.rooms {
		if r.Token == token {
			r.Answer = ""
			return r, nil
		}
	}
	return PeerShare{}, ErrNotFound
}
func (p *Peers) Answer(token, sdp string) error {
	if !validSDP(sdp) {
		return invalid("SDP 无效或超过 48 KiB")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	for id, r := range p.rooms {
		if r.Token == token {
			if r.Offer == "" {
				return ErrUploadNotPending
			}
			if r.Answer != "" && r.Answer != sdp {
				return ErrConflict
			}
			r.Answer = sdp
			p.rooms[id] = r
			return nil
		}
	}
	return ErrNotFound
}
