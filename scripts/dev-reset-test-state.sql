-- 本地测试态重置：清空请求/账本/校正流水，恢复 models.dev 粗能力。
-- 保留：users/projects/api_keys、providers/channels/channel_models、channel_prices、
--       models（含展示价）、routes、model_catalog*、project_model_policies。
-- 用法：scripts/dev-reset-test-state.sh

BEGIN;

-- 1. 补偿与账本（先删 settlement，再删 reservation）
DELETE FROM settlement_recovery_jobs;
DELETE FROM ledger_billing_exceptions;
DELETE FROM ledger_reservations;
DELETE FROM ledger_entries;

-- 2. 请求与用量事实
DELETE FROM cost_snapshots;
DELETE FROM price_snapshots;
DELETE FROM usage_line_items;
DELETE FROM usage_records;
DELETE FROM request_attempts;
DELETE FROM request_records;

-- 3. 账本投影与 API Key 计数
UPDATE user_balances
SET balance = 0,
    reserved_balance = 0,
    updated_at = now();

UPDATE api_keys
SET spent_total = 0,
    updated_at = now();

-- 4. 能力校正产物
TRUNCATE model_capability_observations;
TRUNCATE model_capability_suggestions;
TRUNCATE model_capability_sync_jobs;

UPDATE capability_calibration_state
SET last_processed_attempt_id = 0,
    locked_by = NULL,
    locked_until = NULL,
    updated_at = now()
WHERE id = 1;

-- 5. 运行时能力 → models.dev 目录粗能力（仅已采纳模型）
DELETE FROM model_capabilities mc
USING model_catalog_links mcl
WHERE mc.model_id = mcl.model_id;

INSERT INTO model_capabilities (model_id, capability_key, support_level, limits, updated_by)
SELECT mcl.model_id, mcc.capability_key, mcc.support_level, mcc.limits, 'dev-reset'
FROM model_catalog_links mcl
JOIN model_catalog_capabilities mcc ON mcc.canonical_id = mcl.canonical_id;

-- 未从目录采纳的模型：清空人工/校正写入的能力（若有）
DELETE FROM model_capabilities mc
WHERE NOT EXISTS (
    SELECT 1 FROM model_catalog_links mcl WHERE mcl.model_id = mc.model_id
);

-- 同步采纳基线指纹（等同 catalog-refresh 的能力部分，不改 models 元数据/展示价）
UPDATE model_catalog_links mcl
SET adopted_fingerprint = mc.fingerprint
FROM model_catalog mc
WHERE mc.canonical_id = mcl.canonical_id;

COMMIT;
