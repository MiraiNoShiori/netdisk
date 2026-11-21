package service

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/MiraiNoShiori/netdisk/internal/model"
	"github.com/MiraiNoShiori/netdisk/internal/repository"
	"github.com/MiraiNoShiori/netdisk/internal/storage"
	"github.com/google/uuid"
)

const maxNameLen = 255

// Files 实现文件的上传、下载、改名、删除、回收站、检索与打包下载。
type Files struct {
	AllowGlobalInstantUpload bool // 部署时显式开启才允许跨账号按哈希复用
	store                    *repository.Store
	storage                  storage.Storage
	backend                  string // 写进 blobs.backend，local | s3
	logger                   *slog.Logger
}

func NewFiles(store *repository.Store, st storage.Storage, backend string, logger *slog.Logger) *Files {
	return &Files{store: store, storage: st, backend: backend, logger: logger}
}

// checkQuota 校验用户新增 delta 字节后是否超过存储配额；quota <= 0 表示不限制。
func checkQuota(ctx context.Context, store *repository.Store, ownerID uuid.UUID, delta int64) error {
	if delta <= 0 {
		return nil
	}
	used, quota, err := store.GetUserUsageAndQuota(ctx, ownerID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if quota > 0 && delta > quota-used {
		return ErrQuotaExceeded
	}
	return nil
}

// UploadInput 是一次上传。Body 尚未读完，Upload 会在读取过程中写入存储。
type UploadInput struct {
	OwnerID  uuid.UUID
	ParentID *uuid.UUID // nil = 根目录
	Name     string
	SizeHint int64 // 拿不到就传 -1；真实大小以实际读到的字节数为准
	Body     io.Reader
}

// Upload 把 Body 边读边写进存储，登记 blob，再建节点。
func (f *Files) Upload(ctx context.Context, in UploadInput) (model.Node, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return model.Node{}, err
	}
	if err := f.checkParent(ctx, in.OwnerID, in.ParentID); err != nil {
		return model.Node{}, err
	}
	used, quota, err := f.store.GetUserUsageAndQuota(ctx, in.OwnerID)
	if err != nil {
		return model.Node{}, mapNotFound(err)
	}
	if quota > 0 {
		remaining := quota - used
		if remaining <= 0 || in.SizeHint > remaining {
			return model.Node{}, ErrQuotaExceeded
		}
		in.Body = &quotaReader{r: in.Body, remaining: remaining}
	}

	// 先写自己专属的 key，避免并发上传互相覆盖。
	key := "blobs/" + uuid.NewString()

	counted := &countingReader{r: in.Body}
	hash, err := f.storage.Put(ctx, key, counted, in.SizeHint)
	if err != nil {
		return model.Node{}, fmt.Errorf("write content: %w", err)
	}

	// 未知长度的流式上传读完后再用真实字节数做配额兜底校验。
	if err := checkQuota(ctx, f.store, in.OwnerID, counted.n); err != nil {
		_ = f.dropUploadObject(ctx, key)
		return model.Node{}, err
	}

	blob, recorded, err := f.store.UpsertBlob(ctx, hash, counted.n, f.backend, key)
	if err != nil {
		_ = f.dropUploadObject(ctx, key) // 登记失败则删掉刚写的对象
		return model.Node{}, err
	}
	if !recorded {
		// 内容已存在：删掉刚写的副本，复用库里的对象。
		if err := f.dropUploadObject(ctx, key); err != nil {
			return model.Node{}, fmt.Errorf("drop duplicate object: %w", err)
		}
	}

	node, err := f.store.CreateNode(ctx, in.OwnerID, in.ParentID, name, false, blob.Size, &blob.ID)
	if err != nil {
		// 并发重名被唯一索引挡下：收回本次登记。
		if recorded {
			f.cleanupBlob(ctx, blob.ID, blob.StorageKey)
		}
		if errors.Is(err, repository.ErrUniqueViolation) {
			return model.Node{}, ErrNameConflict
		}
		return model.Node{}, err
	}
	node.Backend = blob.Backend
	return node, nil
}

// cleanupBlob 收回一次失败的登记：先删行，再删对象。
func (f *Files) cleanupBlob(ctx context.Context, blobID uuid.UUID, storageKey string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	deleted, err := f.store.DeleteBlobIfUnreferenced(cleanupCtx, blobID)
	if err != nil || !deleted {
		return // 仍有别的节点引用，不删对象
	}
	_ = f.dropUploadObject(ctx, storageKey)
}

// dropUploadObject 回滚独占的对象。客户端断开或存储删除超时后仍可登记重试。
func (f *Files) dropUploadObject(ctx context.Context, key string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := f.storage.Delete(cleanupCtx, key)
	if err == nil || errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	queueCtx, cancelQueue := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelQueue()
	queueErr := f.store.QueueObject(queueCtx, f.backend, key)
	f.logger.Warn("delete upload object", "key", key, "error", err, "queue_error", queueErr)
	return err
}

// InstantInput 是一次秒传请求：按客户端提供的 SHA-256 复用已存内容。
type InstantInput struct {
	OwnerID  uuid.UUID
	ParentID *uuid.UUID // nil = 根目录
	Name     string
	Hash     string // 客户端算好的 SHA-256（十六进制，大小写都收）
	Size     int64  // 只作参照；真实大小以库里 blob.size 为准
}

// InstantUpload 按内容 hash 命中已有 blob 就直接建节点，不接收字节。
// 节点大小以 blobs.size 为准，in.Size 仅用于一致性校验。
func (f *Files) InstantUpload(ctx context.Context, in InstantInput) (model.Node, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return model.Node{}, err
	}
	hash, err := cleanHash(in.Hash)
	if err != nil {
		return model.Node{}, err
	}
	if err := f.checkParent(ctx, in.OwnerID, in.ParentID); err != nil {
		return model.Node{}, err
	}

	var blob model.Blob
	if f.AllowGlobalInstantUpload {
		blob, err = f.store.GetBlobByHash(ctx, hash)
	} else {
		blob, err = f.store.GetOwnedBlobByHash(ctx, in.OwnerID, hash)
	}
	if errors.Is(err, repository.ErrNotFound) {
		return model.Node{}, ErrContentNotStored
	}
	if err != nil {
		return model.Node{}, err
	}
	if in.Size != blob.Size {
		return model.Node{}, ErrContentNotStored
	}
	if err := checkQuota(ctx, f.store, in.OwnerID, blob.Size); err != nil {
		return model.Node{}, err
	}

	node, err := f.store.CreateNode(ctx, in.OwnerID, in.ParentID, name, false, blob.Size, &blob.ID)
	if errors.Is(err, repository.ErrUniqueViolation) {
		return model.Node{}, ErrNameConflict
	}
	if err != nil {
		return model.Node{}, err
	}
	node.Backend = blob.Backend
	return node, nil
}

// List 列目录的直接子项；目录不存在或不是文件夹时返回错误而不是空列表。
func (f *Files) List(ctx context.Context, ownerID uuid.UUID, parentID *uuid.UUID) ([]model.Node, error) {
	if err := f.checkParent(ctx, ownerID, parentID); err != nil {
		return nil, err
	}
	return f.store.ListChildren(ctx, ownerID, parentID)
}

// ListResult 封装支持分页、排序与搜索的文件查询结果。
type ListResult struct {
	Nodes   []model.Node
	Total   int64
	Limit   int
	Offset  int
	HasMore bool
}

// Query 按目录、关键词、类型、排序和分页条件查询文件列表。
func (f *Files) Query(ctx context.Context, ownerID uuid.UUID, opts repository.ListFilesOptions) (ListResult, error) {
	if opts.FilterByDir {
		if err := f.checkParent(ctx, ownerID, opts.ParentID); err != nil {
			return ListResult{}, err
		}
	}
	if opts.Limit < 0 || opts.Limit > 500 {
		return ListResult{}, invalid("limit 必须在 0 到 500 之间")
	}
	if opts.Offset < 0 {
		return ListResult{}, invalid("offset 不能为负")
	}
	nodes, total, err := f.store.QueryNodes(ctx, ownerID, opts)
	if err != nil {
		return ListResult{}, err
	}
	hasMore := opts.Limit > 0 && int64(opts.Offset+len(nodes)) < total
	return ListResult{
		Nodes:   nodes,
		Total:   total,
		Limit:   opts.Limit,
		Offset:  opts.Offset,
		HasMore: hasMore,
	}, nil
}

// ListFolders 返回用户所有存活目录，供前端目录树选择器使用。
func (f *Files) ListFolders(ctx context.Context, ownerID uuid.UUID) ([]model.Node, error) {
	return f.store.ListAllFolders(ctx, ownerID)
}

// Ancestors 返回某节点自顶向下的祖先链，供面包屑导航定位。
func (f *Files) Ancestors(ctx context.Context, ownerID, id uuid.UUID) ([]model.Node, error) {
	chain, err := f.store.ListNodeAncestors(ctx, ownerID, id)
	if err != nil {
		return nil, err
	}
	if len(chain) == 0 {
		return nil, ErrNotFound
	}
	return chain, nil
}

// CreateDir 新建文件夹。
func (f *Files) CreateDir(ctx context.Context, ownerID uuid.UUID, parentID *uuid.UUID, name string) (model.Node, error) {
	name, err := cleanName(name)
	if err != nil {
		return model.Node{}, err
	}
	if err := f.checkParent(ctx, ownerID, parentID); err != nil {
		return model.Node{}, err
	}

	node, err := f.store.CreateNode(ctx, ownerID, parentID, name, true, 0, nil)
	if errors.Is(err, repository.ErrUniqueViolation) {
		return model.Node{}, ErrNameConflict
	}
	if err != nil {
		return model.Node{}, err
	}
	return node, nil
}

// UpdateInput 是一次修改请求，两个字段都可选。
type UpdateInput struct {
	Name      *string // 非 nil → 改名
	SetParent bool    // true → 移动（ParentID 为 nil 表示移回根目录）
	ParentID  *uuid.UUID
}

// Update 改名和/或移动一个节点。
func (f *Files) Update(ctx context.Context, ownerID, id uuid.UUID, in UpdateInput) (model.Node, error) {
	var name *string
	if in.Name != nil {
		cleaned, err := cleanName(*in.Name)
		if err != nil {
			return model.Node{}, err
		}
		name = &cleaned
	}

	if in.SetParent {
		if err := f.checkParent(ctx, ownerID, in.ParentID); err != nil {
			return model.Node{}, err
		}

		// 移到根目录（ParentID == nil）不可能成环，跳过环检测。
		if in.ParentID != nil {
			cycle, err := f.store.IsSelfOrDescendant(ctx, ownerID, *in.ParentID, id)
			if err != nil {
				return model.Node{}, err
			}
			if cycle {
				return model.Node{}, ErrCycle
			}
		}
	}

	node, err := f.store.UpdateNode(ctx, ownerID, id, name, in.ParentID, in.SetParent)
	switch {
	case errors.Is(err, repository.ErrUniqueViolation):
		return model.Node{}, ErrNameConflict
	case errors.Is(err, repository.ErrNotFound):
		return model.Node{}, ErrNotFound
	case err != nil:
		return model.Node{}, err
	}
	return node, nil
}

// Delete 将节点及其子树移入回收站（软删）；底层内容块保留，直到从回收站彻底删除或过期清理。
func (f *Files) Delete(ctx context.Context, ownerID, id uuid.UUID) error {
	if err := f.store.SoftDeleteSubtree(ctx, ownerID, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// DeletePermanent 跳过回收站立即彻底删除节点及其子树，并回收无引用内容块。
func (f *Files) DeletePermanent(ctx context.Context, ownerID, id uuid.UUID) error {
	locs, err := f.store.PurgeLiveSubtreeAndReclaim(ctx, ownerID, id)
	if err != nil {
		return mapNotFound(err)
	}
	f.deleteReclaimedObjects(ctx, locs)
	return nil
}

// ListTrash 列出用户回收站中的顶层项目。
func (f *Files) ListTrash(ctx context.Context, ownerID uuid.UUID) ([]model.Node, error) {
	return f.store.ListTrash(ctx, ownerID)
}

// RestoreTrash 从回收站恢复某项及其整棵子树；若目标目录同名冲突返回 ErrNameConflict。
func (f *Files) RestoreTrash(ctx context.Context, ownerID, id uuid.UUID, targetParent *uuid.UUID, overrideParent bool) (model.Node, error) {
	if overrideParent {
		if err := f.checkParent(ctx, ownerID, targetParent); err != nil {
			return model.Node{}, err
		}
	}
	node, err := f.store.RestoreTrashSubtree(ctx, ownerID, id, targetParent, overrideParent)
	switch {
	case errors.Is(err, repository.ErrUniqueViolation):
		return model.Node{}, ErrNameConflict
	case errors.Is(err, repository.ErrNotFound):
		return model.Node{}, ErrNotFound
	case err != nil:
		return model.Node{}, err
	}
	return node, nil
}

// PurgeTrashItem 彻底删除回收站中的单项（连同子树）并回收孤儿内容块。
func (f *Files) PurgeTrashItem(ctx context.Context, ownerID, id uuid.UUID) error {
	locs, err := f.store.PurgeTrashSubtree(ctx, ownerID, id)
	if err != nil {
		return mapNotFound(err)
	}
	f.deleteReclaimedObjects(ctx, locs)
	return nil
}

// EmptyTrash 清空当前用户全部回收站内容并回收孤儿内容块。
func (f *Files) EmptyTrash(ctx context.Context, ownerID uuid.UUID) error {
	locs, err := f.store.PurgeAllTrash(ctx, ownerID)
	if err != nil {
		return err
	}
	f.deleteReclaimedObjects(ctx, locs)
	return nil
}

// PurgeExpiredTrash 供后台维护任务定期彻底清理超过保留期限的回收站记录。
func (f *Files) PurgeExpiredTrash(ctx context.Context, retention time.Duration) error {
	if retention <= 0 {
		return nil
	}
	locs, err := f.store.PurgeExpiredTrash(ctx, time.Now().Add(-retention), 200)
	if err != nil {
		return err
	}
	f.deleteReclaimedObjects(ctx, locs)
	return nil
}

func (f *Files) deleteReclaimedObjects(ctx context.Context, locs []repository.BlobLocation) {
	for _, key := range locs {
		st, selectErr := storage.For(f.storage, key.Backend)
		if selectErr != nil {
			f.logger.Warn("missing reclaimed backend", "error", selectErr)
			_ = f.store.QueueObject(ctx, key.Backend, key.Key)
			continue
		}
		if err := st.Delete(ctx, key.Key); err != nil && !errors.Is(err, storage.ErrNotFound) {
			_ = f.store.QueueObject(ctx, key.Backend, key.Key)
			f.logger.Warn("delete reclaimed object", "key", key, "error", err)
		}
	}
}

// Download 是一次下载需要的全部东西。
type Download struct {
	Name    string
	Size    int64
	ModTime time.Time
	Content io.ReadSeekCloser // 必须可 Seek：http.ServeContent 依赖它支持 Range
}

// OpenDownload 打开一个文件供下载；所有权校验由 GetNode 完成，同时累计内容热度。
func (f *Files) OpenDownload(ctx context.Context, ownerID, id uuid.UUID) (Download, error) {
	node, err := f.store.GetNode(ctx, ownerID, id)
	if errors.Is(err, repository.ErrNotFound) {
		return Download{}, ErrNotFound
	}
	if err != nil {
		return Download{}, err
	}
	if node.BlobID == nil {
		return Download{}, ErrNotAFile
	}

	blob, err := f.store.GetBlobByID(ctx, *node.BlobID)
	if err != nil {
		return Download{}, err
	}

	rc, _, err := storage.OpenFrom(ctx, f.storage, blob.Backend, blob.StorageKey)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return Download{}, ErrNotFound
		}
		return Download{}, err
	}
	// 热度计数失败不阻断下载。
	if err := f.store.RecordBlobDownload(ctx, blob.ID); err != nil {
		f.logger.Warn("record blob download", "blob_id", blob.ID, "error", err)
	}
	return Download{Name: node.Name, Size: blob.Size, ModTime: node.UpdatedAt, Content: rc}, nil
}

// ZipDownload 是一次文件夹打包下载。
type ZipDownload struct {
	Name    string        // 建议的文件名（含 .zip）
	Content io.ReadCloser // 边遍历边产生的 zip 字节流
}

// OpenZip 打开一个文件夹的打包下载。
//
// 校验（节点存在、属于调用者、是目录）与整棵子树元数据查询在返回前一次完成（消除 N+1 查询）；
// 随后经 io.Pipe 在后台流式写入各个文件内容，内存仍保持 O(1)。
func (f *Files) OpenZip(ctx context.Context, ownerID, id uuid.UUID) (ZipDownload, error) {
	root, err := f.store.GetNode(ctx, ownerID, id)
	if errors.Is(err, repository.ErrNotFound) {
		return ZipDownload{}, ErrNotFound
	}
	if err != nil {
		return ZipDownload{}, err
	}
	if !root.IsDir {
		return ZipDownload{}, ErrNotADirectory
	}

	entries, err := f.store.ListSubtreeWithBlobs(ctx, ownerID, root.ID)
	if err != nil {
		return ZipDownload{}, err
	}

	pr, pw := io.Pipe()
	go func() {
		zw := zip.NewWriter(pw)
		// 出错时不再调用 zw.Close：否则读端会拿到一个空但合法的 zip。
		if err := f.writeZipEntries(ctx, zw, entries); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if err := zw.Close(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()

	return ZipDownload{Name: root.Name + ".zip", Content: pr}, nil
}

// writeZipEntries 顺序将子树条目流式写入 zw。
func (f *Files) writeZipEntries(ctx context.Context, zw *zip.Writer, entries []model.ZipEntry) error {
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir {
			dirHeader := &zip.FileHeader{
				Name:     strings.Trim(entry.RelPath, "/") + "/",
				Method:   zip.Store,
				Modified: entry.UpdatedAt,
			}
			if _, err := zw.CreateHeader(dirHeader); err != nil {
				return fmt.Errorf("create dir entry: %w", err)
			}
			continue
		}

		fileHeader := &zip.FileHeader{
			Name:     strings.TrimPrefix(entry.RelPath, "/"),
			Method:   getCompressionMethod(entry.RelPath),
			Modified: entry.UpdatedAt,
		}
		writer, err := zw.CreateHeader(fileHeader)
		if err != nil {
			return fmt.Errorf("create file entry: %w", err)
		}
		if entry.StorageKey == "" {
			continue
		}
		if err := func() error {
			reader, _, err := storage.OpenFrom(ctx, f.storage, entry.Backend, entry.StorageKey)
			if err != nil {
				return fmt.Errorf("open file content: %w", err)
			}
			defer reader.Close()
			_, err = io.Copy(writer, reader)
			return err
		}(); err != nil {
			return fmt.Errorf("write file content: %w", err)
		}
	}
	return nil
}

// getCompressionMethod 按扩展名挑压缩方式：已压缩的格式直接 Store，其余 Deflate。
func getCompressionMethod(name string) uint16 {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".zip", ".gz", ".7z", ".mp4", ".webm", ".mp3", ".aac":
		return zip.Store
	default:
		return zip.Deflate
	}
}

// checkParent 确认父目录存在、属于自己、且真的是个目录。
func (f *Files) checkParent(ctx context.Context, ownerID uuid.UUID, parentID *uuid.UUID) error {
	return checkParentDir(ctx, f.store, ownerID, parentID)
}

// quotaReader 到达限额后只探读一个字节，区分刚好读完与超额。
type quotaReader struct {
	r         io.Reader
	remaining int64
}

func (q *quotaReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if q.remaining == 0 {
		n, err := q.r.Read(p[:1])
		if n > 0 {
			return 0, ErrQuotaExceeded
		}
		return 0, err
	}
	if int64(len(p)) > q.remaining {
		p = p[:q.remaining]
	}
	n, err := q.r.Read(p)
	q.remaining -= int64(n)
	return n, err
}

// countingReader 统计实际读取的字节数，作为 blob.size 的来源。
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// cleanName 只留最后一段路径，挡掉 ../ 、空名和超长名。
func cleanName(raw string) (string, error) {
	raw = strings.ReplaceAll(raw, "\\", "/") // 浏览器偶尔会送 Windows 全路径
	name := path.Base(strings.TrimSpace(raw))
	if name == "" || name == "." || name == "/" || name == ".." {
		return "", invalid("文件名不能为空")
	}
	if strings.ContainsAny(name, "\x00\r\n") {
		return "", invalid("文件名含有控制字符")
	}
	if len(name) > maxNameLen {
		return "", invalid("文件名最长 %d 字节", maxNameLen)
	}
	return name, nil
}

// cleanHash 校验并规范化客户端上报的 SHA-256（统一为小写十六进制）。
func cleanHash(raw string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(raw))
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != sha256.Size {
		return "", invalid("hash 必须是 64 位十六进制的 SHA-256")
	}
	return h, nil
}
