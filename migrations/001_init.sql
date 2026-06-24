-- dns-edge 初始化 Schema
-- 所有 DDL 幂等（IF NOT EXISTS），可重复执行。
-- 对应 Go 内嵌副本：internal/pg/store.go schemaSQL 常量，修改时保持同步。

-- ── zones ──────────────────────────────────────────────────────────────────
-- 每行代表一个 DNS Zone（授权域），例如 "example.com."
-- deleted_at 非空 = 软删除；增量同步（Phase 4）通过 updated_at 感知变更。
CREATE TABLE IF NOT EXISTS zones (
    id         BIGSERIAL    PRIMARY KEY,
    name       TEXT         NOT NULL,              -- FQDN，带尾点，例 "example.com."
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,                        -- 软删除时间戳；NULL = 活跃
    CONSTRAINT zones_name_key UNIQUE (name)
);

CREATE INDEX IF NOT EXISTS idx_zones_updated_at ON zones (updated_at);

-- ── records ────────────────────────────────────────────────────────────────
-- 每行代表一条 DNS 资源记录。
-- weight = 0 代表不参与加权；均等分配由 DNS Handler 保证。
-- 同名同类型多条允许（加权分流），但活跃记录（deleted_at IS NULL）中不允许完全重复。
-- 唯一约束通过下方的 partial index 实现，而非表级 CONSTRAINT，
-- 原因：表级 UNIQUE 对软删除行也生效，导致删除后无法用相同值重建记录。
CREATE TABLE IF NOT EXISTS records (
    id         BIGSERIAL    PRIMARY KEY,
    zone_id    BIGINT       NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
    name       TEXT         NOT NULL,              -- 所有者名称 FQDN，带尾点
    type       TEXT         NOT NULL,              -- "A", "AAAA", "CNAME", "MX" …
    ttl        INTEGER      NOT NULL DEFAULT 300,
    value      TEXT         NOT NULL,              -- rdata 文本形式，例 "1.2.3.4"
    weight     INTEGER      NOT NULL DEFAULT 0,   -- 静态权重（0 = 均等）
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

-- 增量同步：按 updated_at 拉取最近变更
CREATE INDEX IF NOT EXISTS idx_records_updated_at  ON records (updated_at);
-- 查询热路径：按 (zone_id, name, type) 快速过滤活跃记录
CREATE INDEX IF NOT EXISTS idx_records_zone_lookup ON records (zone_id, name, type)
    WHERE deleted_at IS NULL;
-- 唯一约束：仅对活跃记录（deleted_at IS NULL）生效，允许软删除后用相同值重建
CREATE UNIQUE INDEX IF NOT EXISTS records_unique_active_rrset
    ON records (zone_id, name, type, value)
    WHERE deleted_at IS NULL;

-- ── 触发器：记录更新时自动刷新 updated_at ───────────────────────────────────
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger WHERE tgname = 'trg_zones_updated_at'
    ) THEN
        CREATE TRIGGER trg_zones_updated_at
            BEFORE UPDATE ON zones
            FOR EACH ROW EXECUTE FUNCTION set_updated_at();
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger WHERE tgname = 'trg_records_updated_at'
    ) THEN
        CREATE TRIGGER trg_records_updated_at
            BEFORE UPDATE ON records
            FOR EACH ROW EXECUTE FUNCTION set_updated_at();
    END IF;
END $$;
