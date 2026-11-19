-- 登录会话持久化：只存 token 摘要，支持重启后保持登出状态。
CREATE TABLE IF NOT EXISTS auth_sessions (
 token_hash TEXT PRIMARY KEY,
 user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS auth_sessions_expires_idx ON auth_sessions(expires_at);
-- 分片会话记住后端，切换默认存储后仍能续传。
ALTER TABLE upload_sessions ADD COLUMN IF NOT EXISTS backend TEXT NOT NULL DEFAULT 'local';
