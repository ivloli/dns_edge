-- 为已部署的实例补充 route_tags 列。
-- 新部署走 EnsureSchema / 001_init.sql，不需要执行本文件。
ALTER TABLE records ADD COLUMN IF NOT EXISTS route_tags TEXT NOT NULL DEFAULT '';
