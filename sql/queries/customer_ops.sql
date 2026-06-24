-- §3.7 客户中心（用户/项目/API Key）只读运维聚合。金额仅 USD。
-- 用户余额来自 user_balances（USD）；消费来自 ledger_entries(debit, USD)；
-- 请求来自 request_records 按 user/project/api_key 归因。Key 状态由时间戳派生。

-- name: UsersOpsSummary :one
SELECT
    (SELECT COUNT(*) FROM users) AS user_total,
    (SELECT COALESCE(SUM(balance), 0) FROM user_balances WHERE currency = 'USD')::numeric AS balance_usd,
    (SELECT COALESCE(SUM(reserved_balance), 0) FROM user_balances WHERE currency = 'USD')::numeric AS reserved_usd,
    (SELECT COUNT(*) FROM user_balances WHERE currency = 'USD' AND (balance - reserved_balance) < 5) AS low_balance_total,
    COUNT(r.id) FILTER (WHERE r.status IN ('succeeded', 'failed', 'canceled')) AS request_total,
    COUNT(r.id) FILTER (WHERE r.status = 'succeeded') AS request_succeeded,
    COALESCE((
        SELECT SUM(le.amount) FROM ledger_entries le
        WHERE le.entry_type = 'debit' AND le.currency = 'USD'
          AND (sqlc.narg('from_time')::timestamptz IS NULL OR le.created_at >= sqlc.narg('from_time')::timestamptz)
          AND (sqlc.narg('to_time')::timestamptz IS NULL OR le.created_at < sqlc.narg('to_time')::timestamptz)
    ), 0)::numeric AS consumption_usd
FROM request_records r
WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz);

-- name: UsersOpsTable :many
SELECT
    u.id,
    u.email,
    u.display_name,
    COALESCE(ub.balance, 0)::numeric AS balance_usd,
    COALESCE(ub.reserved_balance, 0)::numeric AS reserved_usd,
    (SELECT COUNT(*) FROM projects pj WHERE pj.user_id = u.id) AS project_count,
    (SELECT COUNT(*) FROM api_keys k JOIN projects pj ON pj.id = k.project_id WHERE pj.user_id = u.id) AS key_total,
    COUNT(r.id) FILTER (WHERE r.status IN ('succeeded', 'failed', 'canceled')) AS request_total,
    COUNT(r.id) FILTER (WHERE r.status = 'succeeded') AS request_succeeded,
    COALESCE((
        SELECT SUM(le.amount) FROM ledger_entries le
        WHERE le.user_id = u.id AND le.entry_type = 'debit' AND le.currency = 'USD'
          AND (sqlc.narg('from_time')::timestamptz IS NULL OR le.created_at >= sqlc.narg('from_time')::timestamptz)
          AND (sqlc.narg('to_time')::timestamptz IS NULL OR le.created_at < sqlc.narg('to_time')::timestamptz)
    ), 0)::numeric AS consumption_usd,
    (MAX(r.created_at))::timestamptz AS last_used_at
FROM users u
LEFT JOIN user_balances ub ON ub.user_id = u.id AND ub.currency = 'USD'
LEFT JOIN request_records r
    ON r.user_id = u.id
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
WHERE (sqlc.narg('search')::text IS NULL OR u.email ILIKE '%' || sqlc.narg('search')::text || '%' OR u.display_name ILIKE '%' || sqlc.narg('search')::text || '%')
GROUP BY u.id, u.email, u.display_name, ub.balance, ub.reserved_balance
ORDER BY consumption_usd DESC, u.id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: UsersOpsTableCount :one
SELECT COUNT(*) AS total
FROM users u
WHERE (sqlc.narg('search')::text IS NULL OR u.email ILIKE '%' || sqlc.narg('search')::text || '%' OR u.display_name ILIKE '%' || sqlc.narg('search')::text || '%');

-- name: UserOpsDetail :one
SELECT
    COALESCE((SELECT balance FROM user_balances WHERE user_balances.user_id = sqlc.arg('user_id') AND currency = 'USD'), 0)::numeric AS balance_usd,
    COALESCE((SELECT reserved_balance FROM user_balances WHERE user_balances.user_id = sqlc.arg('user_id') AND currency = 'USD'), 0)::numeric AS reserved_usd,
    COUNT(r.id) FILTER (WHERE r.status IN ('succeeded', 'failed', 'canceled')) AS request_total,
    COUNT(r.id) FILTER (WHERE r.status = 'succeeded') AS request_succeeded,
    COALESCE((
        SELECT SUM(le.amount) FROM ledger_entries le
        WHERE le.user_id = sqlc.arg('user_id') AND le.entry_type = 'debit' AND le.currency = 'USD'
          AND (sqlc.narg('from_time')::timestamptz IS NULL OR le.created_at >= sqlc.narg('from_time')::timestamptz)
          AND (sqlc.narg('to_time')::timestamptz IS NULL OR le.created_at < sqlc.narg('to_time')::timestamptz)
    ), 0)::numeric AS consumption_usd
FROM request_records r
WHERE r.user_id = sqlc.arg('user_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz);

-- name: UserOpsKeys :many
-- UserOpsKeys 跨项目汇总用户的 API Key（抽屉 Key Tab）。状态由时间戳派生。
SELECT
    k.id,
    k.name,
    k.project_id,
    pj.name AS project_name,
    k.disabled_at,
    k.revoked_at,
    k.expires_at,
    k.spend_limit,
    k.spent_total,
    k.last_used_at
FROM api_keys k
JOIN projects pj ON pj.id = k.project_id
WHERE pj.user_id = sqlc.arg('user_id')
ORDER BY k.id
LIMIT 200;

-- name: ProjectsOpsSummary :one
SELECT
    (SELECT COUNT(*) FROM projects) AS project_total,
    (SELECT COUNT(*) FROM api_keys) AS key_total,
    (SELECT COUNT(*) FROM api_keys WHERE disabled_at IS NULL AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())) AS key_enabled,
    COUNT(r.id) FILTER (WHERE r.status IN ('succeeded', 'failed', 'canceled')) AS request_total,
    COALESCE((
        SELECT SUM(le.amount) FROM ledger_entries le
        WHERE le.entry_type = 'debit' AND le.currency = 'USD'
          AND (sqlc.narg('from_time')::timestamptz IS NULL OR le.created_at >= sqlc.narg('from_time')::timestamptz)
          AND (sqlc.narg('to_time')::timestamptz IS NULL OR le.created_at < sqlc.narg('to_time')::timestamptz)
    ), 0)::numeric AS consumption_usd
FROM request_records r
WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz);

-- name: ProjectsOpsTable :many
SELECT
    pj.id,
    pj.name,
    pj.user_id,
    u.email AS user_email,
    rt.name AS default_route_name,
    (SELECT COUNT(*) FROM api_keys k WHERE k.project_id = pj.id) AS key_total,
    (SELECT COUNT(*) FROM api_keys k WHERE k.project_id = pj.id AND k.disabled_at IS NULL AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > now())) AS key_enabled,
    COUNT(r.id) FILTER (WHERE r.status IN ('succeeded', 'failed', 'canceled')) AS request_total,
    COALESCE((
        SELECT SUM(le.amount) FROM ledger_entries le
        JOIN request_records rr ON rr.id = le.request_record_id
        WHERE rr.project_id = pj.id AND le.entry_type = 'debit' AND le.currency = 'USD'
          AND (sqlc.narg('from_time')::timestamptz IS NULL OR le.created_at >= sqlc.narg('from_time')::timestamptz)
          AND (sqlc.narg('to_time')::timestamptz IS NULL OR le.created_at < sqlc.narg('to_time')::timestamptz)
    ), 0)::numeric AS consumption_usd,
    (MAX(r.created_at))::timestamptz AS last_used_at
FROM projects pj
JOIN users u ON u.id = pj.user_id
LEFT JOIN routes rt ON rt.id = pj.default_route_id
LEFT JOIN request_records r
    ON r.project_id = pj.id
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
WHERE (sqlc.narg('search')::text IS NULL OR pj.name ILIKE '%' || sqlc.narg('search')::text || '%' OR u.email ILIKE '%' || sqlc.narg('search')::text || '%')
GROUP BY pj.id, pj.name, pj.user_id, u.email, rt.name
ORDER BY consumption_usd DESC, pj.id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ProjectsOpsTableCount :one
SELECT COUNT(*) AS total
FROM projects pj
JOIN users u ON u.id = pj.user_id
WHERE (sqlc.narg('search')::text IS NULL OR pj.name ILIKE '%' || sqlc.narg('search')::text || '%' OR u.email ILIKE '%' || sqlc.narg('search')::text || '%');

-- name: ApiKeysOpsSummary :one
-- ApiKeysOpsSummary 项目范围内 Key 概况。
SELECT
    COUNT(*) AS key_total,
    COUNT(*) FILTER (WHERE disabled_at IS NULL AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())) AS key_enabled,
    COUNT(*) FILTER (WHERE spend_limit IS NOT NULL AND spent_total >= spend_limit) AS spend_capped
FROM api_keys
WHERE project_id = sqlc.arg('project_id');

-- name: ApiKeysOpsTable :many
-- ApiKeysOpsTable 项目范围内 Key 运维表（请求/消费按 api_key 归因）。
SELECT
    k.id,
    k.name,
    k.key_prefix,
    k.project_id,
    k.disabled_at,
    k.revoked_at,
    k.expires_at,
    k.spend_limit,
    k.spent_total,
    k.last_used_at,
    rt.name AS route_name,
    COUNT(r.id) FILTER (WHERE r.status IN ('succeeded', 'failed', 'canceled')) AS request_total,
    COUNT(r.id) FILTER (WHERE r.status = 'succeeded') AS request_succeeded,
    COALESCE((
        SELECT SUM(le.amount) FROM ledger_entries le
        JOIN request_records rr ON rr.id = le.request_record_id
        WHERE rr.api_key_id = k.id AND le.entry_type = 'debit' AND le.currency = 'USD'
          AND (sqlc.narg('from_time')::timestamptz IS NULL OR le.created_at >= sqlc.narg('from_time')::timestamptz)
          AND (sqlc.narg('to_time')::timestamptz IS NULL OR le.created_at < sqlc.narg('to_time')::timestamptz)
    ), 0)::numeric AS consumption_usd
FROM api_keys k
LEFT JOIN routes rt ON rt.id = k.route_id
LEFT JOIN request_records r
    ON r.api_key_id = k.id
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
WHERE k.project_id = sqlc.arg('project_id')
GROUP BY k.id, k.name, k.key_prefix, k.project_id, k.disabled_at, k.revoked_at, k.expires_at, k.spend_limit, k.spent_total, k.last_used_at, rt.name
ORDER BY k.id;
