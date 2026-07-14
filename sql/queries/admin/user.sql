-- name: CreateAPIKey :one
-- CreateAPIKey 创建用户 API Key，保存 key hash（认证用）、展示前缀与完整明文（供多次复制）；route_id 必填（线路必须显式绑定，无默认回落）。
INSERT INTO api_keys (user_id, name, key_prefix, key_hash, key_plaintext, expires_at, route_id)
VALUES (
    sqlc.arg(user_id),
    sqlc.arg(name),
    sqlc.arg(key_prefix),
    sqlc.arg(key_hash),
    sqlc.arg(key_plaintext),
    sqlc.arg(expires_at),
    sqlc.arg(route_id)
)
RETURNING *;

-- name: ListAPIKeysByUserPage :many
-- ListAPIKeysByUserPage 供 admin 按用户分页倒序列出 API Key（返回明文 key 供复制，不返回 key_hash）。
SELECT id, user_id, name, key_prefix, key_plaintext, last_used_at, expires_at, disabled_at, revoked_at, spend_limit, spent_total, route_id, rpm_limit, tpm_limit, rpd_limit, created_at, updated_at
FROM api_keys
WHERE user_id = sqlc.arg(user_id)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountAPIKeysByUser :one
-- CountAPIKeysByUser 供 admin API Key 列表分页统计总数。
SELECT COUNT(*) FROM api_keys WHERE user_id = sqlc.arg(user_id);

-- name: GetAPIKeyByID :one
-- GetAPIKeyByID 供 admin 按 id 读取单把 API Key（带所属用户 ID 与 Key 绑定线路；返回明文 key 供复制）。
SELECT k.id, k.user_id, k.name, k.key_prefix, k.key_plaintext, k.last_used_at, k.expires_at, k.disabled_at, k.revoked_at, k.spend_limit, k.spent_total, k.route_id, k.rpm_limit, k.tpm_limit, k.rpd_limit, k.created_at, k.updated_at
FROM api_keys k
WHERE k.id = sqlc.arg(id)
LIMIT 1;

-- name: SetAPIKeyDisabled :one
-- SetAPIKeyDisabled 启停 API Key：disabled_at 置 now() 为停用，置 NULL 为启用。
UPDATE api_keys
SET disabled_at = sqlc.narg(disabled_at), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, user_id, name, key_prefix, key_plaintext, last_used_at, expires_at, disabled_at, revoked_at, spend_limit, spent_total, route_id, rpm_limit, tpm_limit, rpd_limit, created_at, updated_at;

-- name: RevokeAPIKey :one
-- RevokeAPIKey 永久吊销 API Key（revoked_at 置 now()，不可逆）。
UPDATE api_keys
SET revoked_at = now(), updated_at = now()
WHERE id = sqlc.arg(id) AND revoked_at IS NULL
RETURNING id, user_id, name, key_prefix, key_plaintext, last_used_at, expires_at, disabled_at, revoked_at, spend_limit, spent_total, route_id, rpm_limit, tpm_limit, rpd_limit, created_at, updated_at;

-- name: DeleteAPIKey :execrows
-- DeleteAPIKey 物理删除 API Key，用于清理误建/未使用的 Key。
-- 一旦 Key 已产生调用历史（request_records.api_key_id NO ACTION 外键引用），DB 拒绝删除（23503），
-- 上层降级为 conflict，提示改用吊销——保住计费/审计链路。返回受影响行数（0 表示 Key 不存在）。
DELETE FROM api_keys WHERE id = sqlc.arg(id);

-- name: SetAPIKeySpendLimit :one
-- SetAPIKeySpendLimit 设置/清除 API Key 费用上限；spend_limit 为 NULL 表示不限额。
UPDATE api_keys
SET spend_limit = sqlc.narg(spend_limit), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, user_id, name, key_prefix, key_plaintext, last_used_at, expires_at, disabled_at, revoked_at, spend_limit, spent_total, route_id, rpm_limit, tpm_limit, rpd_limit, created_at, updated_at;

-- name: SetAPIKeyRoute :one
-- SetAPIKeyRoute 改绑 API Key 的线路；route_id 必填（线路不可清空，必须指向一条线路）。
UPDATE api_keys
SET route_id = sqlc.arg(route_id), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, user_id, name, key_prefix, key_plaintext, last_used_at, expires_at, disabled_at, revoked_at, spend_limit, spent_total, route_id, rpm_limit, tpm_limit, rpd_limit, created_at, updated_at;

-- name: SetAPIKeyName :one
-- SetAPIKeyName 更新 API Key 名称。
UPDATE api_keys
SET name = sqlc.arg(name), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, user_id, name, key_prefix, key_plaintext, last_used_at, expires_at, disabled_at, revoked_at, spend_limit, spent_total, route_id, rpm_limit, tpm_limit, rpd_limit, created_at, updated_at;

-- name: SetAPIKeyExpiresAt :one
-- SetAPIKeyExpiresAt 设置/清除 API Key 过期时间；expires_at 为 NULL 表示永不过期。
UPDATE api_keys
SET expires_at = sqlc.narg(expires_at), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, user_id, name, key_prefix, key_plaintext, last_used_at, expires_at, disabled_at, revoked_at, spend_limit, spent_total, route_id, rpm_limit, tpm_limit, rpd_limit, created_at, updated_at;

-- name: SetAPIKeyRateLimits :one
-- SetAPIKeyRateLimits 设置/清除 API Key 的令牌级限流上限（P2-8）；各列 NULL=继承全局默认，0=不限，>0=具体上限。
UPDATE api_keys
SET rpm_limit = sqlc.narg(rpm_limit), tpm_limit = sqlc.narg(tpm_limit), rpd_limit = sqlc.narg(rpd_limit), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, user_id, name, key_prefix, key_plaintext, last_used_at, expires_at, disabled_at, revoked_at, spend_limit, spent_total, route_id, rpm_limit, tpm_limit, rpd_limit, created_at, updated_at;

-- §3.7 客户中心（用户/API Key）只读运维聚合。金额仅 USD。
-- 用户余额来自 user_balances（USD）；消费来自 ledger_entries(debit, USD)；
-- 请求来自 request_records 按 user/api_key 归因。Key 状态由时间戳派生。

-- name: UsersOpsTable :many
SELECT
    u.id,
    u.email,
    u.display_name,
    COALESCE(ub.balance, 0)::numeric AS balance_usd,
    COALESCE(ub.reserved_balance, 0)::numeric AS reserved_usd,
    (SELECT COUNT(*) FROM api_keys k WHERE k.user_id = u.id) AS key_total,
    COUNT(r.id) FILTER (WHERE r.status IN ('succeeded', 'failed')) AS request_total,
    COUNT(r.id) FILTER (WHERE r.status = 'succeeded') AS request_succeeded,
    COALESCE((
        SELECT SUM(le.amount) FROM ledger_entries le
        WHERE le.user_id = u.id AND le.entry_type = 'debit' AND le.currency = 'USD'
          AND (sqlc.narg('from_time')::timestamptz IS NULL OR le.created_at >= sqlc.narg('from_time')::timestamptz)
          AND (sqlc.narg('to_time')::timestamptz IS NULL OR le.created_at < sqlc.narg('to_time')::timestamptz)
    ), 0)::numeric AS consumption_usd,
    COALESCE((
        SELECT SUM(le.amount) FROM ledger_entries le
        WHERE le.user_id = u.id AND le.entry_type = 'debit' AND le.currency = 'USD'
    ), 0)::numeric AS total_consumption_usd,
    COALESCE((
        SELECT SUM(le.amount) FROM ledger_entries le
        WHERE le.user_id = u.id AND le.entry_type IN ('credit', 'adjustment_credit') AND le.currency = 'USD'
    ), 0)::numeric AS total_topup_usd,
    (
        SELECT MAX(r2.created_at) FROM request_records r2 WHERE r2.user_id = u.id
    )::timestamptz AS last_used_at,
    u.created_at
FROM users u
LEFT JOIN user_balances ub ON ub.user_id = u.id AND ub.currency = 'USD'
LEFT JOIN request_records r
    ON r.user_id = u.id
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
WHERE (sqlc.narg('search')::text IS NULL OR u.email ILIKE '%' || sqlc.narg('search')::text || '%' OR u.display_name ILIKE '%' || sqlc.narg('search')::text || '%' OR u.id::text = sqlc.narg('search')::text)
GROUP BY u.id, u.email, u.display_name, u.created_at, ub.balance, ub.reserved_balance
ORDER BY
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'consumption') IN ('', 'consumption') AND COALESCE(sqlc.narg('sort_desc')::bool, true) THEN COALESCE((SELECT SUM(le.amount) FROM ledger_entries le WHERE le.user_id = u.id AND le.entry_type = 'debit' AND le.currency = 'USD' AND (sqlc.narg('from_time')::timestamptz IS NULL OR le.created_at >= sqlc.narg('from_time')::timestamptz) AND (sqlc.narg('to_time')::timestamptz IS NULL OR le.created_at < sqlc.narg('to_time')::timestamptz)), 0) END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'consumption') IN ('', 'consumption') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, true) THEN COALESCE((SELECT SUM(le.amount) FROM ledger_entries le WHERE le.user_id = u.id AND le.entry_type = 'debit' AND le.currency = 'USD' AND (sqlc.narg('from_time')::timestamptz IS NULL OR le.created_at >= sqlc.narg('from_time')::timestamptz) AND (sqlc.narg('to_time')::timestamptz IS NULL OR le.created_at < sqlc.narg('to_time')::timestamptz)), 0) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'email' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN u.email END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'email' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN u.email END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'balance' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COALESCE(ub.balance, 0) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'balance' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COALESCE(ub.balance, 0) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'keys' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (SELECT COUNT(*) FROM api_keys k WHERE k.user_id = u.id) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'keys' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (SELECT COUNT(*) FROM api_keys k WHERE k.user_id = u.id) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'requests' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COUNT(r.id) FILTER (WHERE r.status IN ('succeeded', 'failed')) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'requests' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COUNT(r.id) FILTER (WHERE r.status IN ('succeeded', 'failed')) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'last_used' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (SELECT MAX(r2.created_at) FROM request_records r2 WHERE r2.user_id = u.id) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'last_used' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (SELECT MAX(r2.created_at) FROM request_records r2 WHERE r2.user_id = u.id) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN u.created_at END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN u.created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'total_consumption' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COALESCE((SELECT SUM(le.amount) FROM ledger_entries le WHERE le.user_id = u.id AND le.entry_type = 'debit' AND le.currency = 'USD'), 0) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'total_consumption' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COALESCE((SELECT SUM(le.amount) FROM ledger_entries le WHERE le.user_id = u.id AND le.entry_type = 'debit' AND le.currency = 'USD'), 0) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'total_topup' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COALESCE((SELECT SUM(le.amount) FROM ledger_entries le WHERE le.user_id = u.id AND le.entry_type IN ('credit', 'adjustment_credit') AND le.currency = 'USD'), 0) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'total_topup' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COALESCE((SELECT SUM(le.amount) FROM ledger_entries le WHERE le.user_id = u.id AND le.entry_type IN ('credit', 'adjustment_credit') AND le.currency = 'USD'), 0) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'display_name' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN u.display_name END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'display_name' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN u.display_name END ASC NULLS LAST,
  u.id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: UsersOpsTableCount :one
SELECT COUNT(*) AS total
FROM users u
WHERE (sqlc.narg('search')::text IS NULL OR u.email ILIKE '%' || sqlc.narg('search')::text || '%' OR u.display_name ILIKE '%' || sqlc.narg('search')::text || '%' OR u.id::text = sqlc.narg('search')::text);

-- name: UserOpsDetail :one
SELECT
    COALESCE((SELECT balance FROM user_balances WHERE user_balances.user_id = sqlc.arg('user_id') AND currency = 'USD'), 0)::numeric AS balance_usd,
    COALESCE((SELECT reserved_balance FROM user_balances WHERE user_balances.user_id = sqlc.arg('user_id') AND currency = 'USD'), 0)::numeric AS reserved_usd,
    COUNT(r.id) FILTER (WHERE r.status IN ('succeeded', 'failed')) AS request_total,
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
-- UserOpsKeys 汇总用户的 API Key（抽屉 Key Tab）。状态由时间戳派生。
SELECT
    k.id,
    k.name,
    k.disabled_at,
    k.revoked_at,
    k.expires_at,
    k.spend_limit,
    k.spent_total,
    k.last_used_at
FROM api_keys k
WHERE k.user_id = sqlc.arg('user_id')
ORDER BY k.id
LIMIT 200;

-- name: ApiKeysOpsSummary :one
-- ApiKeysOpsSummary 用户范围内 Key 概况。
SELECT
    COUNT(*) AS key_total,
    COUNT(*) FILTER (WHERE disabled_at IS NULL AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())) AS key_enabled,
    COUNT(*) FILTER (WHERE spend_limit IS NOT NULL AND spent_total >= spend_limit) AS spend_capped
FROM api_keys
WHERE user_id = sqlc.arg('user_id');

-- name: ApiKeysOpsTable :many
-- ApiKeysOpsTable 用户范围内 Key 运维表（请求/消费按 api_key 归因）。
SELECT
    k.id,
    k.name,
    k.key_prefix,
    k.key_plaintext,
    k.user_id,
    k.disabled_at,
    k.revoked_at,
    k.expires_at,
    k.spend_limit,
    k.spent_total,
    k.last_used_at,
    k.route_id,
    rt.name AS route_name,
    rt.price_ratio AS route_price_ratio,
    COUNT(r.id) FILTER (WHERE r.status IN ('succeeded', 'failed')) AS request_total,
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
WHERE k.user_id = sqlc.arg('user_id')
  AND (sqlc.narg('search')::text IS NULL OR k.name ILIKE '%' || sqlc.narg('search')::text || '%' OR k.key_prefix ILIKE '%' || sqlc.narg('search')::text || '%')
GROUP BY k.id, k.name, k.key_prefix, k.key_plaintext, k.user_id, k.disabled_at, k.revoked_at, k.expires_at, k.spend_limit, k.spent_total, k.last_used_at, k.route_id, rt.name, rt.price_ratio
ORDER BY
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'requests') IN ('', 'requests') AND COALESCE(sqlc.narg('sort_desc')::bool, true) THEN COUNT(r.id) FILTER (WHERE r.status IN ('succeeded', 'failed')) END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'requests') IN ('', 'requests') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, true) THEN COUNT(r.id) FILTER (WHERE r.status IN ('succeeded', 'failed')) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'name' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN k.name END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'name' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN k.name END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'spent' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN k.spent_total END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'spent' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN k.spent_total END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'consumption' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COALESCE((SELECT SUM(le.amount) FROM ledger_entries le JOIN request_records rr ON rr.id = le.request_record_id WHERE rr.api_key_id = k.id AND le.entry_type = 'debit' AND le.currency = 'USD' AND (sqlc.narg('from_time')::timestamptz IS NULL OR le.created_at >= sqlc.narg('from_time')::timestamptz) AND (sqlc.narg('to_time')::timestamptz IS NULL OR le.created_at < sqlc.narg('to_time')::timestamptz)), 0) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'consumption' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COALESCE((SELECT SUM(le.amount) FROM ledger_entries le JOIN request_records rr ON rr.id = le.request_record_id WHERE rr.api_key_id = k.id AND le.entry_type = 'debit' AND le.currency = 'USD' AND (sqlc.narg('from_time')::timestamptz IS NULL OR le.created_at >= sqlc.narg('from_time')::timestamptz) AND (sqlc.narg('to_time')::timestamptz IS NULL OR le.created_at < sqlc.narg('to_time')::timestamptz)), 0) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'last_used' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN k.last_used_at END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'last_used' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN k.last_used_at END ASC NULLS LAST,
  k.id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ApiKeysOpsTableCount :one
-- ApiKeysOpsTableCount 与 ApiKeysOpsTable 同过滤条件下的 Key 总数。
SELECT COUNT(*) AS total
FROM api_keys k
WHERE k.user_id = sqlc.arg('user_id')
  AND (sqlc.narg('search')::text IS NULL OR k.name ILIKE '%' || sqlc.narg('search')::text || '%' OR k.key_prefix ILIKE '%' || sqlc.narg('search')::text || '%');

-- name: ListUserBalancesByUser :many
-- ListUserBalancesByUser 供 admin 用户详情读取该用户全部币种余额。
SELECT
    id,
    user_id,
    currency,
    balance,
    reserved_balance,
    created_at,
    updated_at
FROM
    user_balances
WHERE
    user_id = sqlc.arg (user_id)
ORDER BY currency;

-- name: CreateUser :one
-- CreateUser 创建用户账号并返回用户事实。
INSERT INTO users (email, password_hash, display_name)
VALUES ($1, $2, $3)
RETURNING *;

-- name: ListUsersPage :many
-- ListUsersPage 供 admin 分页倒序列出用户（不返回 password_hash）；q 为空不过滤。
SELECT u.id, u.email, u.display_name, u.created_at, u.updated_at
FROM users u
WHERE (sqlc.narg('q')::text IS NULL
       OR u.email ILIKE '%' || sqlc.narg('q')::text || '%'
       OR u.display_name ILIKE '%' || sqlc.narg('q')::text || '%')
ORDER BY u.id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountUsers :one
-- CountUsers 供 admin 用户列表分页统计总数；q 为空不过滤。
SELECT COUNT(*)
FROM users
WHERE (sqlc.narg('q')::text IS NULL
       OR email ILIKE '%' || sqlc.narg('q')::text || '%'
       OR display_name ILIKE '%' || sqlc.narg('q')::text || '%');

-- name: GetUserByID :one
-- GetUserByID 供 admin 按 id 读取用户（不返回 password_hash）。
SELECT u.id, u.email, u.display_name, u.created_at, u.updated_at
FROM users u
WHERE u.id = sqlc.arg(id)
LIMIT 1;
