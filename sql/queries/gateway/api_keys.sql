-- name: GetAPIKeyByHash :one
-- GetAPIKeyByHash 按 key hash 读取 API Key，带出所属用户 ID 与 Key 绑定线路，并计算是否已达费用上限。
-- spend_limit_reached 在 SQL 层判定，避免认证路径在 Go 里做 NUMERIC 比较（M7 费用上限闸门）。
-- route_id 是运行时线路解析的唯一依据（线路必填，无默认回落；线路缺失/停用则拒绝请求）。
-- 限流上限（rpm/tpm/rpd）取自绑定线路（DEC-027：限流归线路，按 (线路,用户) 计数）；
-- api_keys 自身的旧限流列已废弃，不再参与认证。
SELECT k.id, k.user_id, k.name, k.key_prefix, k.key_hash, k.last_used_at, k.expires_at, k.disabled_at, k.revoked_at, k.created_at, k.updated_at,
       (k.spend_limit IS NOT NULL AND k.spent_total >= k.spend_limit) AS spend_limit_reached,
       k.route_id,
       rt.rpm_limit AS route_rpm_limit,
       rt.tpm_limit AS route_tpm_limit,
       rt.rpd_limit AS route_rpd_limit
FROM api_keys k
JOIN routes rt ON rt.id = k.route_id
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
