package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/MiraiNoShiori/netdisk/internal/model"
	"github.com/MiraiNoShiori/netdisk/internal/repository"
	"github.com/MiraiNoShiori/netdisk/internal/storage"
	"github.com/google/uuid"
)

// Uploads 是分片上传（断点续传）的业务层，会话状态为 pending/completed/aborted。
type Uploads struct {
	store   *repository.Store
	storage storage.Storage
	backend string // 写进 blobs.backend，和 Files 一致
	logger  *slog.Logger
}

func NewUploads(store *repository.Store, st storage.Storage, backend string, logger *slog.Logger) *Uploads {
	return &Uploads{store: store, storage: st, backend: backend, logger: logger}
}

// partKey 返回分片对象在存储中的 key。
func partKey(sessionID uuid.UUID, partNo int) string {
	return fmt.Sprintf("uploads/%s/%d", sessionID, partNo)
}

// checkParentDir 确认父目录存在、属于自己且是目录；parentID 为 nil 表示根目录。
func checkParentDir(ctx context.Context, store *repository.Store, ownerID uuid.UUID, parentID *uuid.UUID) error {
	if parentID == nil {
		return nil
	}
	parent, err := store.GetNode(ctx, ownerID, *parentID)
	if errors.Is(err, repository.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !parent.IsDir {
		return invalid("parent_id 指向的不是文件夹")
	}
	return nil
}

// mapNotFound 把 repository 的 ErrNotFound 翻成业务层的 ErrNotFound，其余原样。
func mapNotFound(err error) error {
	if errors.Is(err, repository.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// CreateInput 是开一次分片上传的请求。
type CreateInput struct {
	OwnerID     uuid.UUID
	ParentID    *uuid.UUID // nil = 根目录
	Name        string
	TotalSize   int64
	ChunkSize   int64
	ContentHash *string // 客户端预申报的整份 SHA-256（可选）
}

// Create 开一个分片会话，part_count 由 total_size/chunk_size 向上取整（至少 1）。
func (u *Uploads) Create(ctx context.Context, in CreateInput) (model.UploadSession, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return model.UploadSession{}, err
	}
	if in.TotalSize < 0 {
		return model.UploadSession{}, invalid("total_size 不能为负")
	}
	if in.ChunkSize <= 0 {
		return model.UploadSession{}, invalid("chunk_size 必须为正")
	}
	if err := checkParentDir(ctx, u.store, in.OwnerID, in.ParentID); err != nil {
		return model.UploadSession{}, err
	}
	if err := checkQuota(ctx, u.store, in.OwnerID, in.TotalSize); err != nil {
		return model.UploadSession{}, err
	}

	if in.TotalSize > 1<<40 || in.ChunkSize > 64<<20 {
		return model.UploadSession{}, invalid("文件最多 1 TiB，分片最多 64 MiB")
	}
	if in.ContentHash != nil {
		hash, err := cleanHash(*in.ContentHash)
		if err != nil {
			return model.UploadSession{}, err
		}
		in.ContentHash = &hash
	}
	partCount := int(in.TotalSize / in.ChunkSize)
	if in.TotalSize%in.ChunkSize != 0 {
		partCount++
	}
	if partCount > 100000 {
		return model.UploadSession{}, invalid("分片数最多 100000")
	}
	if partCount < 1 {
		partCount = 1 // 空文件也是"1 片、0 字节"
	}
	return u.store.CreateUploadSession(ctx, in.OwnerID, in.ParentID, name,
		in.TotalSize, in.ChunkSize, partCount, in.ContentHash, u.backend)
}

// UploadStatus 是一次查询会话的结果：收到的分片号 + 还缺的分片号。
type UploadStatus struct {
	Session       model.UploadSession
	ReceivedParts []int
	MissingParts  []int
}

// Status 查会话进度，返回已收到的分片号和仍缺的分片号。
func (u *Uploads) Status(ctx context.Context, ownerID, sessionID uuid.UUID) (UploadStatus, error) {
	sess, err := u.store.GetUploadSession(ctx, ownerID, sessionID)
	if err != nil {
		return UploadStatus{}, mapNotFound(err)
	}
	parts, err := u.store.ListUploadParts(ctx, sessionID)
	if err != nil {
		return UploadStatus{}, err
	}

	have := make(map[int]bool, len(parts))
	received := make([]int, 0, len(parts))
	for _, p := range parts {
		have[p.PartNo] = true
		received = append(received, p.PartNo)
	}
	missing := make([]int, 0)
	for n := 0; n < sess.PartCount; n++ {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	return UploadStatus{Session: sess, ReceivedParts: received, MissingParts: missing}, nil
}

// List 列某用户所有 pending 会话。
func (u *Uploads) List(ctx context.Context, ownerID uuid.UUID) ([]model.UploadSession, error) {
	return u.store.ListPendingUploadSessions(ctx, ownerID)
}

// expectedPartSize 返回第 partNo 片应有的字节数；最后一片为 total_size 减去前面各片之和。
func expectedPartSize(sess model.UploadSession, partNo int) int64 {
	if partNo == sess.PartCount-1 {
		return sess.TotalSize - int64(sess.PartCount-1)*sess.ChunkSize
	}
	return sess.ChunkSize
}

// PutPart 收一个分片：流式写入存储并登记行（重复上传同一片为幂等覆盖）。
// 分片大小必须严格等于应有字节数，不符则删掉刚写入的对象并返回错误。
func (u *Uploads) PutPart(ctx context.Context, ownerID, sessionID uuid.UUID, partNo int, body io.Reader) (model.UploadPart, error) {
	unlock, err := u.store.LockUpload(ctx, sessionID)
	if err != nil {
		return model.UploadPart{}, err
	}
	defer unlock()

	sess, err := u.store.GetUploadSession(ctx, ownerID, sessionID)
	if err != nil {
		return model.UploadPart{}, mapNotFound(err)
	}
	if sess.Status != "pending" || !sess.ExpiresAt.After(time.Now()) {
		return model.UploadPart{}, ErrUploadNotPending
	}
	if partNo < 0 || partNo >= sess.PartCount {
		return model.UploadPart{}, invalid("part_no 超出范围：本会话共 %d 片（0..%d）", sess.PartCount, sess.PartCount-1)
	}

	st, err := storage.For(u.storage, sess.Backend)
	if err != nil {
		return model.UploadPart{}, err
	}
	oldParts, err := u.store.ListUploadParts(ctx, sessionID)
	if err != nil {
		return model.UploadPart{}, err
	}
	want := expectedPartSize(sess, partNo)
	key := partKey(sessionID, partNo) + "-" + uuid.NewString()
	// 多读 1 字节用于判断超长，实际大小由随后的 Stat 校验。
	checksum, err := st.Put(ctx, key, io.LimitReader(body, want+1), want)
	if err != nil {
		return model.UploadPart{}, fmt.Errorf("write part: %w", err)
	}
	info, err := st.Stat(ctx, key)
	if err != nil {
		return model.UploadPart{}, err
	}
	if info.Size != want {
		_ = st.Delete(ctx, key)
		return model.UploadPart{}, invalid("第 %d 片大小应为 %d 字节，实收 %d", partNo, want, info.Size)
	}

	if err := u.store.PutUploadPart(ctx, sessionID, partNo, info.Size, checksum, key); err != nil {
		_ = st.Delete(ctx, key)
		return model.UploadPart{}, err
	}
	for _, old := range oldParts {
		if old.PartNo == partNo {
			_ = st.Delete(ctx, old.StorageKey)
		}
	}
	return model.UploadPart{StorageKey: key, SessionID: sessionID, PartNo: partNo, Size: info.Size, Checksum: checksum}, nil
}

// Abort 放弃一个会话：标记 aborted 并删掉已收到的分片对象；
// 已不在 pending 的会话重复放弃也返回 nil。
func (u *Uploads) Abort(ctx context.Context, ownerID, sessionID uuid.UUID) error {
	unlock, err := u.store.LockUpload(ctx, sessionID)
	if err != nil {
		return err
	}
	defer unlock()

	sess, err := u.store.GetUploadSession(ctx, ownerID, sessionID)
	if err != nil {
		return mapNotFound(err)
	}
	st, err := storage.For(u.storage, sess.Backend)
	if err != nil {
		return err
	}
	parts, err := u.store.ListUploadParts(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, p := range parts {
		if err := st.Delete(ctx, p.StorageKey); err != nil && !errors.Is(err, storage.ErrNotFound) {
			u.logger.Warn("delete aborted part", "session_id", sessionID, "part_no", p.PartNo, "error", err)
		}
	}
	if sess.Status == "pending" {
		if _, err := u.store.TransitionUploadSession(ctx, ownerID, sessionID, "pending", "aborted"); err != nil {
			return err
		}
	}
	return nil
}

// Complete 收尾一个分片会话：按 part_no 顺序拼接分片、登记 blob、建节点，
// 并把会话标记为 completed、删掉临时分片对象。
// 缺分片时返回 ErrUploadIncomplete 且不改动任何状态。
func (u *Uploads) Complete(ctx context.Context, ownerID, sessionID uuid.UUID) (model.Node, error) {
	unlock, err := u.store.LockUpload(ctx, sessionID)
	if err != nil {
		return model.Node{}, err
	}
	defer unlock()

	session, err := u.store.GetUploadSession(ctx, ownerID, sessionID)
	if err != nil {
		return model.Node{}, mapNotFound(err)
	}
	if session.Status != "pending" || !session.ExpiresAt.After(time.Now()) {
		return model.Node{}, ErrUploadNotPending
	}
	st, err := storage.For(u.storage, session.Backend)
	if err != nil {
		return model.Node{}, err
	}
	if err := checkParentDir(ctx, u.store, ownerID, session.ParentID); err != nil {
		return model.Node{}, err
	}
	if err := checkQuota(ctx, u.store, ownerID, session.TotalSize); err != nil {
		return model.Node{}, err
	}
	parts, err := u.store.ListUploadParts(ctx, sessionID)
	if err != nil {
		return model.Node{}, err
	}
	if len(parts) != session.PartCount {
		return model.Node{}, ErrUploadIncomplete
	}
	srcKeys := make([]string, 0, len(parts))
	for _, part := range parts {
		srcKeys = append(srcKeys, part.StorageKey)
	}
	destKey := "blobs/" + uuid.NewString()

	hash, size, err := st.Concat(ctx, destKey, srcKeys)
	if err != nil {
		return model.Node{}, fmt.Errorf("concat parts: %w", err)
	}
	if size != session.TotalSize || (session.ContentHash != nil && hash != *session.ContentHash) {
		_ = st.Delete(ctx, destKey)
		return model.Node{}, invalid("完整文件大小或 SHA-256 校验失败")
	}
	node, _, recorded, err := u.store.CompleteUploadWithBlob(ctx, ownerID, sessionID, hash, size, session.Backend, destKey)
	if err != nil {
		if delErr := st.Delete(ctx, destKey); delErr != nil && !errors.Is(delErr, storage.ErrNotFound) {
			_ = u.store.QueueObject(ctx, session.Backend, destKey)
			u.logger.Warn("delete rolled-back upload blob", "session_id", sessionID, "key", destKey, "error", delErr)
		}
		if errors.Is(err, repository.ErrUniqueViolation) {
			return model.Node{}, ErrNameConflict
		}
		if errors.Is(err, repository.ErrNotFound) {
			return model.Node{}, ErrUploadNotPending
		}
		return model.Node{}, fmt.Errorf("create node: %w", err)
	}
	if !recorded {
		if err := st.Delete(ctx, destKey); err != nil && !errors.Is(err, storage.ErrNotFound) {
			_ = u.store.QueueObject(ctx, session.Backend, destKey)
			u.logger.Warn("delete duplicate upload blob", "session_id", sessionID, "key", destKey, "error", err)
		}
	}

	// 节点已建、会话已 completed，删分片失败只留下垃圾，不影响本次收尾结果。
	for _, part := range parts {
		key := part.StorageKey
		if err := st.Delete(ctx, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
			u.logger.Warn("delete completed part", "session_id", sessionID, "part_no", part.PartNo, "error", err)
		}
	}
	return node, nil
}
