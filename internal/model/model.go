// Package model 是数据库表的 Go 映射，字段与 migrations/*.sql 对应。
package model

import (
	"time"

	"github.com/google/uuid"
)

// User 对应 users 表；PasswordHash 不序列化。
type User struct {
	ID           uuid.UUID `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Nickname     string    `json:"nickname"`
	Bio          string    `json:"bio"`
	Email        string    `json:"email"`
	QuotaBytes   int64     `json:"quota_bytes"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// UserStats 汇总用户的容量占用、文件数、回收站与去重节省统计。
type UserStats struct {
	UsedBytes       int64 `json:"used_bytes"`
	LiveBytes       int64 `json:"live_bytes"`
	TrashBytes      int64 `json:"trash_bytes"`
	QuotaBytes      int64 `json:"quota_bytes"`
	FileCount       int64 `json:"file_count"`
	DirCount        int64 `json:"dir_count"`
	TrashCount      int64 `json:"trash_count"`
	ShareCount      int64 `json:"share_count"`
	LocalBytes      int64 `json:"local_bytes"`
	S3Bytes         int64 `json:"s3_bytes"`
	DedupSavedBytes int64 `json:"dedup_saved_bytes"`
}

// Blob 是一份存储内容，多个 Node 可指向同一个 Blob。
type Blob struct {
	ID             uuid.UUID  `json:"id"`
	ContentHash    string     `json:"content_hash"`
	Size           int64      `json:"size"`
	Backend        string     `json:"backend"`
	StorageKey     string     `json:"-"`
	DownloadCount  int64      `json:"download_count"`
	LastAccessedAt *time.Time `json:"last_accessed_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// Node 是文件或文件夹，由 is_dir 区分，parent_id 自引用成树。
type Node struct {
	ID            uuid.UUID  `json:"id"`
	OwnerID       uuid.UUID  `json:"owner_id"`
	ParentID      *uuid.UUID `json:"parent_id"` // nil = 根目录
	Name          string     `json:"name"`
	IsDir         bool       `json:"is_dir"`
	Size          int64      `json:"size"`
	BlobID        *uuid.UUID `json:"-"` // 目录为 nil
	Backend       string     `json:"backend,omitempty"`
	DownloadCount int64      `json:"download_count,omitempty"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
	DeletedRootID *uuid.UUID `json:"deleted_root_id,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// ZipEntry 用于单次递归查询生成流式 ZIP，避免 N+1 查询。
type ZipEntry struct {
	RelPath    string
	IsDir      bool
	UpdatedAt  time.Time
	Backend    string
	StorageKey string
}

// Share 是一条分享链接，token 为公开访问凭据。
type Share struct {
	ID            uuid.UUID  `json:"id"`
	Token         string     `json:"token"`
	NodeID        uuid.UUID  `json:"node_id"`
	NodeName      string     `json:"node_name,omitempty"`
	NodeIsDir     bool       `json:"node_is_dir,omitempty"`
	CreatedBy     uuid.UUID  `json:"created_by"`
	ExpiresAt     *time.Time `json:"expires_at"`
	PasswordHash  *string    `json:"-"`
	HasPassword   bool       `json:"has_password"`
	MaxDownloads  *int64     `json:"max_downloads"`
	DownloadCount int64      `json:"download_count"`
	VisitCount    int64      `json:"visit_count"`
	CreatedAt     time.Time  `json:"created_at"`
}

// ShareAccessLog 记录一条分享访问日志（浏览、下载或密码失败）。
type ShareAccessLog struct {
	ID        uuid.UUID  `json:"id"`
	ShareID   uuid.UUID  `json:"share_id"`
	Action    string     `json:"action"`
	NodeID    *uuid.UUID `json:"node_id,omitempty"`
	ClientIP  string     `json:"client_ip"`
	UserAgent string     `json:"user_agent"`
	CreatedAt time.Time  `json:"created_at"`
}

// ShareDailyStat 按天聚合的访问与下载统计。
type ShareDailyStat struct {
	Date      string `json:"date"`
	Views     int64  `json:"views"`
	Downloads int64  `json:"downloads"`
}

// ShareAnalytics 汇总单条分享链接的访问分析数据。
type ShareAnalytics struct {
	Share       Share            `json:"share"`
	UniqueIPs   int64            `json:"unique_ips"`
	FailedAuths int64            `json:"failed_auths"`
	DailyStats  []ShareDailyStat `json:"daily_stats"`
	RecentLogs  []ShareAccessLog `json:"recent_logs"`
}

// UploadSession 是一次分片上传会话。
type UploadSession struct {
	Backend     string     `json:"backend"`
	ID          uuid.UUID  `json:"id"`
	OwnerID     uuid.UUID  `json:"owner_id"`
	ParentID    *uuid.UUID `json:"parent_id"`
	Name        string     `json:"name"`
	TotalSize   int64      `json:"total_size"`
	ChunkSize   int64      `json:"chunk_size"`
	PartCount   int        `json:"part_count"`
	Status      string     `json:"status"` // pending | completed | aborted
	ContentHash *string    `json:"content_hash"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
}

// UploadPart 是已收到的一个分片，主键 (SessionID, PartNo) 保证重传幂等。
type UploadPart struct {
	StorageKey string    `json:"-"`
	SessionID  uuid.UUID `json:"session_id"`
	PartNo     int       `json:"part_no"`
	Size       int64     `json:"size"`
	Checksum   string    `json:"checksum"`
	CreatedAt  time.Time `json:"created_at"`
}
