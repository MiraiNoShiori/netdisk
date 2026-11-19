package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"strings"
	"time"

	"github.com/Miraitowa-kawayi/netdisk/internal/model"
	"github.com/Miraitowa-kawayi/netdisk/internal/repository"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// Shares 是分享链接的业务层。
// 匿名访问路径只认 token（以及可选提取码），不做登录态校验。
type Shares struct {
	store  *repository.Store
	files  *Files // 复用文件下载逻辑
	logger *slog.Logger
}

func NewShares(store *repository.Store, files *Files, logger *slog.Logger) *Shares {
	return &Shares{store: store, files: files, logger: logger}
}

// ShareInput 是建一条分享的请求。
type ShareInput struct {
	OwnerID      uuid.UUID
	NodeID       uuid.UUID
	ExpiresIn    time.Duration // 0 = 永不过期
	Password     string        // 可选提取码（4–72 字节）
	MaxDownloads *int64        // 可选最大下载次数（>= 1）
}

// ShareAccessMeta 传递匿名访问上下文，用于提取码校验与访问日志记录。
type ShareAccessMeta struct {
	Password         string
	PasswordVerified bool // 仅由 handler 验证服务端 HMAC 签名 Cookie 后置为 true
	ClientIP         string
	UserAgent        string
}

// ShareView 是管理接口回给客户端的一条分享。
type ShareView struct {
	Share model.Share `json:"share"`
	URL   string      `json:"url"` // 相对路径，客户端自己拼 host
}

// Create 给一个节点建分享链接；节点必须存活（属于自己、未被软删、祖先链干净）。
func (s *Shares) Create(ctx context.Context, in ShareInput) (ShareView, error) {
	live, err := s.store.NodeIsLive(ctx, in.OwnerID, in.NodeID)
	if err != nil {
		return ShareView{}, err
	}
	if !live {
		return ShareView{}, ErrNotFound
	}

	var passwordHash *string
	if pw := strings.TrimSpace(in.Password); pw != "" {
		if len(pw) < 4 || len(pw) > maxPasswordLen {
			return ShareView{}, invalid("分享提取码长度必须在 4–%d 字节之间", maxPasswordLen)
		}
		h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			return ShareView{}, err
		}
		hs := string(h)
		passwordHash = &hs
	}
	if in.MaxDownloads != nil && *in.MaxDownloads < 1 {
		return ShareView{}, invalid("max_downloads 必须大于等于 1")
	}

	token, err := newShareToken()
	if err != nil {
		return ShareView{}, err
	}
	var expiresAt *time.Time
	if in.ExpiresIn > 0 {
		at := time.Now().Add(in.ExpiresIn)
		expiresAt = &at
	}

	share, err := s.store.CreateShareWithOptions(ctx, in.NodeID, in.OwnerID, token, expiresAt, passwordHash, in.MaxDownloads)
	if err != nil {
		return ShareView{}, err
	}
	return ShareView{Share: share, URL: "/api/v1/share/" + token}, nil
}

// List 列我建过的分享。
func (s *Shares) List(ctx context.Context, ownerID uuid.UUID) ([]model.Share, error) {
	return s.store.ListSharesByOwner(ctx, ownerID)
}

// Analytics 返回单条分享链接的访问趋势、独立 IP 与最近访问日志。
func (s *Shares) Analytics(ctx context.Context, ownerID, shareID uuid.UUID) (model.ShareAnalytics, error) {
	a, err := s.store.GetShareAnalytics(ctx, ownerID, shareID)
	if err != nil {
		return model.ShareAnalytics{}, mapNotFound(err)
	}
	return a, nil
}

// Revoke 撤销一条分享；不是自己的返回 ErrNotFound。
func (s *Shares) Revoke(ctx context.Context, ownerID, shareID uuid.UUID) error {
	return mapNotFound(s.store.DeleteShare(ctx, ownerID, shareID))
}

// PublicView 是匿名访问拿到的内容：节点元信息 +（目录时）直接子项 + 分享配额摘要。
type PublicView struct {
	Node          model.Node   `json:"node"`
	Children      []model.Node `json:"children"`
	ExpiresAt     *time.Time   `json:"expires_at,omitempty"`
	MaxDownloads  *int64       `json:"max_downloads,omitempty"`
	DownloadCount int64        `json:"download_count"`
}

// VerifyPassword 校验分享提取码；返回分享的密码哈希摘要（供 handler 签发短期免密 cookie）。
func (s *Shares) VerifyPassword(ctx context.Context, token string, meta ShareAccessMeta) (model.Share, error) {
	share, err := s.liveShare(ctx, token)
	if err != nil {
		return model.Share{}, err
	}
	if err := s.checkSharePassword(ctx, share, meta); err != nil {
		return model.Share{}, err
	}
	return share, nil
}

// Resolve 匿名打开一条分享（兼容旧签名）。
func (s *Shares) Resolve(ctx context.Context, token string, targets ...uuid.UUID) (PublicView, error) {
	var target *uuid.UUID
	if len(targets) > 0 {
		t := targets[0]
		target = &t
	}
	return s.ResolveWithAccess(ctx, token, target, ShareAccessMeta{})
}

// ResolveWithAccess 匿名打开一条分享，支持提取码校验与访问日志记录。
func (s *Shares) ResolveWithAccess(ctx context.Context, token string, targetID *uuid.UUID, meta ShareAccessMeta) (PublicView, error) {
	share, err := s.liveShare(ctx, token)
	if err != nil {
		return PublicView{}, err
	}
	if err := s.checkSharePassword(ctx, share, meta); err != nil {
		return PublicView{}, err
	}

	target := share.NodeID
	if targetID != nil && *targetID != target {
		ok, err := s.store.IsSelfOrDescendant(ctx, share.CreatedBy, *targetID, target)
		if err != nil {
			return PublicView{}, err
		}
		if !ok {
			return PublicView{}, ErrNotFound
		}
		target = *targetID
	}
	node, err := s.store.GetNode(ctx, share.CreatedBy, target)
	if err != nil {
		return PublicView{}, mapNotFound(err)
	}

	children := make([]model.Node, 0)
	if node.IsDir {
		if children, err = s.store.ListChildren(ctx, share.CreatedBy, &node.ID); err != nil {
			return PublicView{}, err
		}
	}

	// 计数与日志写失败不影响本次访问。
	if err := s.store.IncrementShareVisit(ctx, share.ID); err != nil {
		s.logger.Warn("increment share visit", "share_id", share.ID, "error", err)
	}
	if err := s.store.RecordShareAccessLog(ctx, share.ID, "view", &node.ID, meta.ClientIP, meta.UserAgent); err != nil {
		s.logger.Warn("record share view log", "share_id", share.ID, "error", err)
	}
	return PublicView{
		Node:          node,
		Children:      children,
		ExpiresAt:     share.ExpiresAt,
		MaxDownloads:  share.MaxDownloads,
		DownloadCount: share.DownloadCount,
	}, nil
}

// OpenDownload 匿名下载（兼容旧签名）。
func (s *Shares) OpenDownload(ctx context.Context, token string, nodeID *uuid.UUID) (Download, error) {
	return s.OpenDownloadWithAccess(ctx, token, nodeID, ShareAccessMeta{})
}

// OpenDownloadWithAccess 匿名下载，强制校验提取码、原子扣减最大下载次数并写访问日志。
func (s *Shares) OpenDownloadWithAccess(ctx context.Context, token string, nodeID *uuid.UUID, meta ShareAccessMeta) (Download, error) {
	share, err := s.liveShare(ctx, token)
	if err != nil {
		return Download{}, err
	}
	if err := s.checkSharePassword(ctx, share, meta); err != nil {
		return Download{}, err
	}

	target := share.NodeID
	if nodeID != nil && *nodeID != share.NodeID {
		ok, err := s.store.IsSelfOrDescendant(ctx, share.CreatedBy, *nodeID, share.NodeID)
		if err != nil {
			return Download{}, err
		}
		if !ok {
			// 不在该子树内一律按不存在处理，避免拿链接枚举 node id。
			return Download{}, ErrNotFound
		}
		target = *nodeID
	}

	// 先检查目标确实是可下载的文件，再扣减下载次数，避免下目录或无效请求白扣额度。
	dl, err := s.files.OpenDownload(ctx, share.CreatedBy, target)
	if err != nil {
		return Download{}, err
	}
	allowed, err := s.store.ConsumeShareDownload(ctx, share.ID)
	if err != nil {
		dl.Content.Close()
		return Download{}, err
	}
	if !allowed {
		dl.Content.Close()
		return Download{}, ErrShareLimitReached
	}
	if err := s.store.RecordShareAccessLog(ctx, share.ID, "download", &target, meta.ClientIP, meta.UserAgent); err != nil {
		s.logger.Warn("record share download log", "share_id", share.ID, "error", err)
	}
	return dl, nil
}

// CheckShareCookie 验证由服务端签发的免密 Cookie 签名是否与当前分享匹配。
func (s *Shares) SharePasswordFingerprint(ctx context.Context, token string) (string, error) {
	share, err := s.liveShare(ctx, token)
	if err != nil {
		return "", err
	}
	if !share.HasPassword || share.PasswordHash == nil {
		return "", nil
	}
	return *share.PasswordHash, nil
}

func (s *Shares) checkSharePassword(ctx context.Context, share model.Share, meta ShareAccessMeta) error {
	if !share.HasPassword || share.PasswordHash == nil {
		return nil
	}
	if meta.PasswordVerified {
		return nil
	}
	pw := strings.TrimSpace(meta.Password)
	if pw == "" {
		return ErrSharePasswordRequired
	}
	if bcrypt.CompareHashAndPassword([]byte(*share.PasswordHash), []byte(pw)) != nil {
		_ = s.store.RecordShareAccessLog(ctx, share.ID, "auth_failed", nil, meta.ClientIP, meta.UserAgent)
		return ErrSharePasswordInvalid
	}
	return nil
}

// liveShare 取一条可用的分享；不存在、已过期或目标已失效都返回 ErrNotFound。
func (s *Shares) liveShare(ctx context.Context, token string) (model.Share, error) {
	share, err := s.store.GetShareByToken(ctx, token)
	if err != nil {
		return model.Share{}, mapNotFound(err)
	}
	if share.ExpiresAt != nil && !share.ExpiresAt.After(time.Now()) {
		return model.Share{}, ErrNotFound
	}
	live, err := s.store.NodeIsLive(ctx, share.CreatedBy, share.NodeID)
	if err != nil {
		return model.Share{}, err
	}
	if !live {
		return model.Share{}, ErrNotFound
	}
	return share, nil
}

// newShareToken 生成 128 位十六进制的公开访问 token。
func newShareToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
