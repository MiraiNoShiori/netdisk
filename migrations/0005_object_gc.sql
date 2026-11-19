-- 元数据事务完成后再删物理对象。失败保留队列，后台可重试。
CREATE TABLE IF NOT EXISTS object_gc (
 backend TEXT NOT NULL,
 storage_key TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(backend,storage_key)
);
