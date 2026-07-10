-- 清理 AI Agent / blackbox 測試殘留數據
-- 保留：user_id=3 真實帳號、VIP 線路、正式渠道/服務商

BEGIN;

CREATE TEMP TABLE _test_users AS
SELECT id FROM users
WHERE email LIKE 'blackbox-%@example.test'
   OR email IN (
     'user-1783324938033750000@example.com',
     'user-1783500318805297000@example.com',
     'user-1783501173518610000@example.com'
   );

CREATE TEMP TABLE _test_routes AS
SELECT id FROM routes
WHERE name LIKE 'blackbox-route-%'
   OR name LIKE 'ledger-route-%'
   OR name LIKE 'identity-route-%'
   OR name LIKE 'e2e-route-%';

CREATE TEMP TABLE _test_channels AS
SELECT id FROM channels
WHERE name LIKE 'blackbox-%';

CREATE TEMP TABLE _test_providers AS
SELECT id FROM providers
WHERE slug LIKE 'blackbox-provider-%';

-- 測試用戶的請求/帳本關聯數據
DELETE FROM ledger_billing_exceptions WHERE user_id IN (SELECT id FROM _test_users);
DELETE FROM ledger_reservations WHERE user_id IN (SELECT id FROM _test_users);
DELETE FROM ledger_entries WHERE user_id IN (SELECT id FROM _test_users);
DELETE FROM settlement_recovery_jobs WHERE request_record_id IN (
  SELECT id FROM request_records WHERE user_id IN (SELECT id FROM _test_users)
);
DELETE FROM cost_snapshots WHERE request_record_id IN (
  SELECT id FROM request_records WHERE user_id IN (SELECT id FROM _test_users)
);
DELETE FROM price_snapshots WHERE request_record_id IN (
  SELECT id FROM request_records WHERE user_id IN (SELECT id FROM _test_users)
);
DELETE FROM usage_line_items WHERE usage_record_id IN (
  SELECT id FROM usage_records WHERE request_record_id IN (
    SELECT id FROM request_records WHERE user_id IN (SELECT id FROM _test_users)
  )
);
DELETE FROM usage_records WHERE request_record_id IN (
  SELECT id FROM request_records WHERE user_id IN (SELECT id FROM _test_users)
);
DELETE FROM request_attempts WHERE request_record_id IN (
  SELECT id FROM request_records WHERE user_id IN (SELECT id FROM _test_users)
);
DELETE FROM request_records WHERE user_id IN (SELECT id FROM _test_users);

-- 測試用戶的 API Key 和餘額
DELETE FROM api_keys WHERE user_id IN (SELECT id FROM _test_users);
DELETE FROM user_balances WHERE user_id IN (SELECT id FROM _test_users);
DELETE FROM users WHERE id IN (SELECT id FROM _test_users);

-- AI 建立的臨時驗證 Key（先清其請求關聯數據）
DELETE FROM ledger_billing_exceptions WHERE request_record_id IN (
  SELECT id FROM request_records WHERE api_key_id = 124
);
DELETE FROM ledger_reservations WHERE request_record_id IN (
  SELECT id FROM request_records WHERE api_key_id = 124
);
DELETE FROM ledger_entries WHERE request_record_id IN (
  SELECT id FROM request_records WHERE api_key_id = 124
);
DELETE FROM settlement_recovery_jobs WHERE request_record_id IN (
  SELECT id FROM request_records WHERE api_key_id = 124
);
DELETE FROM cost_snapshots WHERE request_record_id IN (
  SELECT id FROM request_records WHERE api_key_id = 124
);
DELETE FROM price_snapshots WHERE request_record_id IN (
  SELECT id FROM request_records WHERE api_key_id = 124
);
DELETE FROM usage_line_items WHERE usage_record_id IN (
  SELECT id FROM usage_records WHERE request_record_id IN (
    SELECT id FROM request_records WHERE api_key_id = 124
  )
);
DELETE FROM usage_records WHERE request_record_id IN (
  SELECT id FROM request_records WHERE api_key_id = 124
);
DELETE FROM request_attempts WHERE request_record_id IN (
  SELECT id FROM request_records WHERE api_key_id = 124
);
DELETE FROM request_records WHERE api_key_id = 124;
DELETE FROM api_keys WHERE id = 124 AND name = 'tmp-verify-key';

-- 測試線路
DELETE FROM route_channels WHERE route_id IN (SELECT id FROM _test_routes);
DELETE FROM routes WHERE id IN (SELECT id FROM _test_routes);

-- 測試渠道
DELETE FROM channel_test_logs WHERE channel_id IN (SELECT id FROM _test_channels);
DELETE FROM channel_models WHERE channel_id IN (SELECT id FROM _test_channels);
DELETE FROM channel_prices WHERE channel_id IN (SELECT id FROM _test_channels);
DELETE FROM channels WHERE id IN (SELECT id FROM _test_channels);

-- 測試服務商
DELETE FROM providers WHERE id IN (SELECT id FROM _test_providers);

COMMIT;
