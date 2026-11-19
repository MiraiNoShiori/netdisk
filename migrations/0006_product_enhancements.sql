-- 0006: 产品化能力（回收站、配额、分享加固、分层存储）
-- 包含：用户邮箱与存储配额、回收站（子树恢复与定期清理）、分享链接密码/下载次数/访问日志、
-- Refresh Token 会话轮换、以及热点文件智能分层所需的下载热度统计。

-- 1. 用户邮箱与配额（默认 10 GiB；0 表示不限）
ALTER TABLE users ADD COLUMN IF NOT EXISTS email TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS quota_bytes BIGINT NOT NULL DEFAULT 10737418240;

-- 2. 回收站：记录软删时的顶层根 ID，便于回收站按“删除项”分组展示并整棵恢复
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS deleted_root_id UUID;
CREATE INDEX IF NOT EXISTS nodes_trash_owner_idx
    ON nodes (owner_id, deleted_at DESC)
    WHERE deleted_at IS NOT NULL AND deleted_root_id = id;
CREATE INDEX IF NOT EXISTS nodes_deleted_root_idx
    ON nodes (owner_id, deleted_root_id)
    WHERE deleted_at IS NOT NULL;

-- 3. 分享链接加固：访问密码（bcrypt）、最大下载次数、下载计数
ALTER TABLE shares ADD COLUMN IF NOT EXISTS password_hash TEXT;
ALTER TABLE shares ADD COLUMN IF NOT EXISTS max_downloads BIGINT;
ALTER TABLE shares ADD COLUMN IF NOT EXISTS download_count BIGINT NOT NULL DEFAULT 0;

-- 4. 分享访问日志与数据分析
CREATE TABLE IF NOT EXISTS share_access_logs (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    share_id   UUID        NOT NULL REFERENCES shares (id) ON DELETE CASCADE,
    action     TEXT        NOT NULL, -- view | download | auth_failed
    node_id    UUID,
    client_ip  TEXT        NOT NULL DEFAULT '',
    user_agent TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS share_access_logs_share_idx
    ON share_access_logs (share_id, created_at DESC);

-- 5. Refresh Token 轮换（扩展现有 auth_sessions 表）
ALTER TABLE auth_sessions ADD COLUMN IF NOT EXISTS refresh_token_hash TEXT;
ALTER TABLE auth_sessions ADD COLUMN IF NOT EXISTS refresh_expires_at TIMESTAMPTZ;
CREATE UNIQUE INDEX IF NOT EXISTS auth_sessions_refresh_idx
    ON auth_sessions (refresh_token_hash)
    WHERE refresh_token_hash IS NOT NULL;

-- 6. 智能存储分层：记录内容块访问热度（供热点文件自动迁移至 S3）
ALTER TABLE blobs ADD COLUMN IF NOT EXISTS download_count BIGINT NOT NULL DEFAULT 0;
ALTER TABLE blobs ADD COLUMN IF NOT EXISTS last_accessed_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS blobs_hot_local_idx
    ON blobs (download_count DESC, size DESC)
    WHERE backend = 'local';
