-- 开发库：保留 providers / channels / models / channel_models / channel_prices /
-- routes / model_capabilities / model_catalog* / capability_keys，
-- 清空请求/账本/旧身份，并创建新的测试用户、项目、API Key（key 由 shell 注入）。
-- 用法：scripts/dev-fresh-test-user.sh

BEGIN;

-- 1. 补偿与账本
DELETE FROM settlement_recovery_jobs;
DELETE FROM ledger_billing_exceptions;
DELETE FROM ledger_reservations;
DELETE FROM ledger_entries;

-- 2. 请求与用量
DELETE FROM cost_snapshots;
DELETE FROM price_snapshots;
DELETE FROM usage_line_items;
DELETE FROM usage_records;
DELETE FROM request_attempts;
DELETE FROM request_records;

-- 3. 项目策略与旧身份（保留 routes / route_channels）
DELETE FROM project_model_policies;
DELETE FROM api_keys;
DELETE FROM projects;
DELETE FROM user_balances;
DELETE FROM users;

-- 4. 同步任务审计（不影响模型/渠道）
TRUNCATE model_capability_sync_jobs;

-- 5. 序列复位（不碰 models / channels / providers / routes）
ALTER SEQUENCE users_id_seq RESTART WITH 1;
ALTER SEQUENCE projects_id_seq RESTART WITH 1;
ALTER SEQUENCE api_keys_id_seq RESTART WITH 1;
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

-- 6. 新测试用户 / 项目 / 余额
INSERT INTO users (email, password_hash, display_name)
VALUES (
    'test@unio.local',
    'dev-no-login',
    '测试用户'
);

INSERT INTO projects (user_id, name, default_route_id)
VALUES (1, '测试项目', (SELECT id FROM routes ORDER BY id LIMIT 1));

INSERT INTO user_balances (user_id, currency, balance, reserved_balance)
VALUES (1, 'USD', 100.0000000000, 0.0000000000);

COMMIT;
