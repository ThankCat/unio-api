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

-- name: GetAPIKeyByHash :one
-- GetAPIKeyByHash 按 key hash 读取 API Key，带出所属用户 ID 与 Key 绑定线路，并计算是否已达费用上限。
-- spend_limit_reached 在 SQL 层判定，避免认证路径在 Go 里做 NUMERIC 比较（M7 费用上限闸门）。
-- route_id 是运行时线路解析的唯一依据（线路必填，无默认回落；线路缺失/停用则拒绝请求）。
SELECT k.id, k.user_id, k.name, k.key_prefix, k.key_hash, k.last_used_at, k.expires_at, k.disabled_at, k.revoked_at, k.created_at, k.updated_at,
       (k.spend_limit IS NOT NULL AND k.spent_total >= k.spend_limit) AS spend_limit_reached,
       k.route_id,
       k.rpm_limit, k.tpm_limit, k.rpd_limit
FROM api_keys k
WHERE key_hash = $1
LIMIT 1;

-- name: UpdateAPIKeyLastUsedAt :exec
-- UpdateAPIKeyLastUsedAt 更新 API Key 最近使用时间。
UPDATE api_keys
SET last_used_at = sqlc.arg(last_used_at), updated_at = now()
WHERE id = sqlc.arg(id);

-- name: AddAPIKeySpentTotal :exec
-- AddAPIKeySpentTotal 在 settlement capture 时累加该 Key 的累计花费（M7 费用上限计数器）。
UPDATE api_keys
SET spent_total = spent_total + sqlc.arg(amount), updated_at = now()
WHERE id = sqlc.arg(id);

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

-- name: SetAPIKeyRateLimits :one
-- SetAPIKeyRateLimits 设置/清除 API Key 的令牌级限流上限（P2-8）；各列 NULL=继承全局默认，0=不限，>0=具体上限。
UPDATE api_keys
SET rpm_limit = sqlc.narg(rpm_limit), tpm_limit = sqlc.narg(tpm_limit), rpd_limit = sqlc.narg(rpd_limit), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, user_id, name, key_prefix, key_plaintext, last_used_at, expires_at, disabled_at, revoked_at, spend_limit, spent_total, route_id, rpm_limit, tpm_limit, rpd_limit, created_at, updated_at;
