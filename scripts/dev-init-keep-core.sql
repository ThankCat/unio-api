-- 开发库初始化：仅保留 users / projects / api_keys / providers / channels。
-- 清空模型、目录、线路、请求、账本等运营数据；余额与 spent_total 归零。
-- capability_keys（能力字典）为 migration seed，保留不动。
-- 用法：scripts/dev-init-keep-core.sh

BEGIN;

-- 1. 补偿与账本
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

-- 3. 线路（先解除 api_keys / projects 外键引用）
UPDATE api_keys SET route_id = NULL, updated_at = now() WHERE route_id IS NOT NULL;
UPDATE projects SET default_route_id = NULL, updated_at = now() WHERE default_route_id IS NOT NULL;
DELETE FROM route_channels;
DELETE FROM routes;

-- 4. 渠道-模型绑定与价格
DELETE FROM channel_prices;
DELETE FROM channel_models;

-- 5. 模型能力与目录
DELETE FROM model_capabilities;
DELETE FROM model_catalog_links;
DELETE FROM model_catalog_capabilities;
DELETE FROM model_catalog;
DELETE FROM project_model_policies;
DELETE FROM models;

-- 6. 同步任务审计
TRUNCATE model_capability_sync_jobs;

-- 7. 保留用户的余额投影归零；API Key 累计消费归零
UPDATE user_balances
SET balance = 0,
    reserved_balance = 0,
    updated_at = now();

UPDATE api_keys
SET spent_total = 0,
    updated_at = now();

-- 8. 自增序列复位（便于从零手工录入）
ALTER SEQUENCE models_id_seq RESTART WITH 1;
ALTER SEQUENCE routes_id_seq RESTART WITH 1;
ALTER SEQUENCE channel_models_id_seq RESTART WITH 1;
ALTER SEQUENCE channel_prices_id_seq RESTART WITH 1;
ALTER SEQUENCE request_records_id_seq RESTART WITH 1;
ALTER SEQUENCE request_attempts_id_seq RESTART WITH 1;
ALTER SEQUENCE usage_records_id_seq RESTART WITH 1;
ALTER SEQUENCE usage_line_items_id_seq RESTART WITH 1;
ALTER SEQUENCE price_snapshots_id_seq RESTART WITH 1;
ALTER SEQUENCE cost_snapshots_id_seq RESTART WITH 1;
ALTER SEQUENCE ledger_entries_id_seq RESTART WITH 1;
ALTER SEQUENCE ledger_reservations_id_seq RESTART WITH 1;
ALTER SEQUENCE ledger_billing_exceptions_id_seq RESTART WITH 1;
ALTER SEQUENCE settlement_recovery_jobs_id_seq RESTART WITH 1;
ALTER SEQUENCE model_capability_sync_jobs_id_seq RESTART WITH 1;

COMMIT;
