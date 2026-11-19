package repository

import (
	"context"
	"time"

	"github.com/Miraitowa-kawayi/netdisk/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const shareColumns = `id, token, node_id, created_by, expires_at, password_hash, max_downloads, download_count, visit_count, created_at`

func scanShare(row pgx.Row) (model.Share, error) {
	var s model.Share
	err := row.Scan(
		&s.ID, &s.Token, &s.NodeID, &s.CreatedBy, &s.ExpiresAt,
		&s.PasswordHash, &s.MaxDownloads, &s.DownloadCount, &s.VisitCount, &s.CreatedAt,
	)
	s.HasPassword = s.PasswordHash != nil && *s.PasswordHash != ""
	return s, translate(err)
}

// CreateShare 建一条分享；token 由 service 生成，expiresAt 为 nil 表示永不过期。
func (s *Store) CreateShare(ctx context.Context, nodeID, createdBy uuid.UUID, token string, expiresAt *time.Time) (model.Share, error) {
	return s.CreateShareWithOptions(ctx, nodeID, createdBy, token, expiresAt, nil, nil)
}

// CreateShareWithOptions 建一条带可选密码和最大下载次数限制的分享。
func (s *Store) CreateShareWithOptions(ctx context.Context, nodeID, createdBy uuid.UUID, token string, expiresAt *time.Time, passwordHash *string, maxDownloads *int64) (model.Share, error) {
	row := s.pool.QueryRow(ctx, `
INSERT INTO shares (token, node_id, created_by, expires_at, password_hash, max_downloads)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING `+shareColumns, token, nodeID, createdBy, expiresAt, passwordHash, maxDownloads)
	return scanShare(row)
}

// GetShareByToken 按公开 token 取分享，不带 owner_id 条件（token 即授权凭据）。
func (s *Store) GetShareByToken(ctx context.Context, token string) (model.Share, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+shareColumns+` FROM shares WHERE token = $1`, token)
	return scanShare(row)
}

// GetShareByID 按分享 ID 与拥有者查询单条分享（管理端分析使用）。
func (s *Store) GetShareByID(ctx context.Context, ownerID, id uuid.UUID) (model.Share, error) {
	row := s.pool.QueryRow(ctx, `
SELECT s.id, s.token, s.node_id, s.created_by, s.expires_at,
       s.password_hash, s.max_downloads, s.download_count, s.visit_count, s.created_at,
       COALESCE(n.name, ''), COALESCE(n.is_dir, false)
  FROM shares s
  LEFT JOIN nodes n ON n.id = s.node_id
 WHERE s.id = $1 AND s.created_by = $2`, id, ownerID)
	var sh model.Share
	err := row.Scan(
		&sh.ID, &sh.Token, &sh.NodeID, &sh.CreatedBy, &sh.ExpiresAt,
		&sh.PasswordHash, &sh.MaxDownloads, &sh.DownloadCount, &sh.VisitCount, &sh.CreatedAt,
		&sh.NodeName, &sh.NodeIsDir,
	)
	sh.HasPassword = sh.PasswordHash != nil && *sh.PasswordHash != ""
	return sh, translate(err)
}

// ListSharesByOwner 列某人创建的分享（顺带返回目标节点名称与类型），新的在前。
func (s *Store) ListSharesByOwner(ctx context.Context, ownerID uuid.UUID) ([]model.Share, error) {
	rows, err := s.pool.Query(ctx, `
SELECT s.id, s.token, s.node_id, s.created_by, s.expires_at,
       s.password_hash, s.max_downloads, s.download_count, s.visit_count, s.created_at,
       COALESCE(n.name, ''), COALESCE(n.is_dir, false)
  FROM shares s
  LEFT JOIN nodes n ON n.id = s.node_id
 WHERE s.created_by = $1
 ORDER BY s.created_at DESC`, ownerID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	shares := make([]model.Share, 0, 8)
	for rows.Next() {
		var sh model.Share
		if err := rows.Scan(
			&sh.ID, &sh.Token, &sh.NodeID, &sh.CreatedBy, &sh.ExpiresAt,
			&sh.PasswordHash, &sh.MaxDownloads, &sh.DownloadCount, &sh.VisitCount, &sh.CreatedAt,
			&sh.NodeName, &sh.NodeIsDir,
		); err != nil {
			return nil, translate(err)
		}
		sh.HasPassword = sh.PasswordHash != nil && *sh.PasswordHash != ""
		shares = append(shares, sh)
	}
	return shares, translate(rows.Err())
}

// DeleteShare 撤销一条分享；owner_id 一并进 WHERE，撤销别人的分享返回 ErrNotFound。
func (s *Store) DeleteShare(ctx context.Context, ownerID, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM shares WHERE id = $1 AND created_by = $2`, id, ownerID)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// IncrementShareVisit 给访问计数 +1；调用方失败只记 WARN，不影响访问。
func (s *Store) IncrementShareVisit(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE shares SET visit_count = visit_count + 1 WHERE id = $1`, id)
	return translate(err)
}

// ConsumeShareDownload 原子校验下载次数上限并累加 download_count；达到上限返回 false。
func (s *Store) ConsumeShareDownload(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
UPDATE shares
   SET download_count = download_count + 1
 WHERE id = $1
   AND (max_downloads IS NULL OR download_count < max_downloads)`, id)
	if err != nil {
		return false, translate(err)
	}
	return tag.RowsAffected() == 1, nil
}

// RecordShareAccessLog 记录一次分享访问事件（view / download / auth_failed）。
func (s *Store) RecordShareAccessLog(ctx context.Context, shareID uuid.UUID, action string, nodeID *uuid.UUID, clientIP, userAgent string) error {
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO share_access_logs (share_id, action, node_id, client_ip, user_agent)
VALUES ($1, $2, $3, $4, $5)`, shareID, action, nodeID, clientIP, userAgent)
	return translate(err)
}

// GetShareAnalytics 汇总单条分享链接的独立访客 IP、失败认证次数、近 7 天按日趋势与最近 50 条访问日志。
func (s *Store) GetShareAnalytics(ctx context.Context, ownerID, shareID uuid.UUID) (model.ShareAnalytics, error) {
	share, err := s.GetShareByID(ctx, ownerID, shareID)
	if err != nil {
		return model.ShareAnalytics{}, err
	}

	out := model.ShareAnalytics{
		Share:      share,
		DailyStats: make([]model.ShareDailyStat, 0, 7),
		RecentLogs: make([]model.ShareAccessLog, 0, 16),
	}

	if err := s.pool.QueryRow(ctx, `
SELECT COUNT(DISTINCT NULLIF(client_ip, '')),
       COUNT(*) FILTER (WHERE action = 'auth_failed')
  FROM share_access_logs
 WHERE share_id = $1`, shareID).Scan(&out.UniqueIPs, &out.FailedAuths); err != nil {
		return model.ShareAnalytics{}, translate(err)
	}

	dayRows, err := s.pool.Query(ctx, `
SELECT to_char(date_trunc('day', created_at), 'YYYY-MM-DD') AS day,
       COUNT(*) FILTER (WHERE action = 'view') AS views,
       COUNT(*) FILTER (WHERE action = 'download') AS downloads
  FROM share_access_logs
 WHERE share_id = $1 AND created_at >= now() - interval '7 days'
 GROUP BY day
 ORDER BY day ASC`, shareID)
	if err != nil {
		return model.ShareAnalytics{}, translate(err)
	}
	for dayRows.Next() {
		var d model.ShareDailyStat
		if err := dayRows.Scan(&d.Date, &d.Views, &d.Downloads); err != nil {
			dayRows.Close()
			return model.ShareAnalytics{}, translate(err)
		}
		out.DailyStats = append(out.DailyStats, d)
	}
	dayRows.Close()
	if err := dayRows.Err(); err != nil {
		return model.ShareAnalytics{}, translate(err)
	}

	logRows, err := s.pool.Query(ctx, `
SELECT id, share_id, action, node_id, client_ip, user_agent, created_at
  FROM share_access_logs
 WHERE share_id = $1
 ORDER BY created_at DESC
 LIMIT 50`, shareID)
	if err != nil {
		return model.ShareAnalytics{}, translate(err)
	}
	defer logRows.Close()
	for logRows.Next() {
		var l model.ShareAccessLog
		if err := logRows.Scan(&l.ID, &l.ShareID, &l.Action, &l.NodeID, &l.ClientIP, &l.UserAgent, &l.CreatedAt); err != nil {
			return model.ShareAnalytics{}, translate(err)
		}
		out.RecentLogs = append(out.RecentLogs, l)
	}
	return out, translate(logRows.Err())
}

// NodeIsLive 判断节点存在，且从它到根的整条祖先链上没有任何一环被软删。
// 起点和递归都不能带 deleted_at IS NULL，因为正需要查出被删的那一环；带上会使结果恒为真。
func (s *Store) NodeIsLive(ctx context.Context, ownerID, id uuid.UUID) (bool, error) {
	const query = `
WITH RECURSIVE up AS (
    SELECT id, parent_id, deleted_at
      FROM nodes
     WHERE id = $1 AND owner_id = $2
    UNION ALL
    SELECT n.id, n.parent_id, n.deleted_at
      FROM nodes n
      JOIN up ON n.id = up.parent_id
     WHERE n.owner_id = $2
) CYCLE id SET is_cycle USING path
SELECT EXISTS (SELECT 1 FROM up)
   AND NOT EXISTS (SELECT 1 FROM up WHERE deleted_at IS NOT NULL)`

	var live bool
	if err := s.pool.QueryRow(ctx, query, id, ownerID).Scan(&live); err != nil {
		return false, translate(err)
	}
	return live, nil
}
