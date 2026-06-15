-- 阶段 14：models 表与 models.dev 目录解耦。
-- 目录专属列迁出到 model_catalog / model_catalog_links；models 只保留运营事实。

-- 先把历史 seed/import 行的 source 收敛到新枚举，避免 CHECK 收紧时失败（开发期 models 通常为空）。
UPDATE models SET source = 'catalog' WHERE source = 'seed_models_dev';
UPDATE models SET source = 'manual'  WHERE source = 'import';

-- canonical_id：采纳来源迁到 model_catalog_links（非唯一，支持一模板多模型）。
DROP INDEX IF EXISTS idx_models_canonical_id;
ALTER TABLE models DROP COLUMN canonical_id;

-- removed_upstream_at：属目录概念，迁到 model_catalog。
ALTER TABLE models DROP COLUMN removed_upstream_at;

-- lab（Q8）：运营侧与 owned_by 重复，统一用 owned_by；lab 概念仅保留在 model_catalog。
ALTER TABLE models DROP COLUMN lab;

-- source 收敛为 manual（空白手建）/ catalog（从目录采纳，采纳后完全可编辑）。
ALTER TABLE models DROP CONSTRAINT models_source_check;
ALTER TABLE models ADD CONSTRAINT models_source_check CHECK (source IN ('manual', 'catalog'));
