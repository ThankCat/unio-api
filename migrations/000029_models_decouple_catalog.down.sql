-- 还原 source 枚举。
ALTER TABLE models DROP CONSTRAINT models_source_check;
ALTER TABLE models ADD CONSTRAINT models_source_check
    CHECK (source IN ('seed_models_dev', 'manual', 'import'));

-- 还原目录专属列。
ALTER TABLE models ADD COLUMN canonical_id TEXT UNIQUE;
ALTER TABLE models ADD COLUMN lab TEXT;
ALTER TABLE models ADD COLUMN removed_upstream_at TIMESTAMPTZ;

-- 还原 canonical_id 部分索引。
CREATE INDEX idx_models_canonical_id ON models (canonical_id) WHERE canonical_id IS NOT NULL;
