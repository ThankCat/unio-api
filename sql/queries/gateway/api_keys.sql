-- name: GetAPIKeyByHash :one
-- GetAPIKeyByHash 按 key hash 读取 API Key，带出所属用户 ID 与用户级限流，并计算是否已达费用上限。
-- spend_limit_reached 在 SQL 层判定，避免认证路径在 Go 里做 NUMERIC 比较（M7 费用上限闸门）。
-- 限流上限（rpm/rpd/concurrency）取自所属用户（按用户计数）：同一用户的多把 Key 共享配额。
SELECT k.id, k.user_id, k.name, k.key_prefix, k.key_hash, k.last_used_at, k.expires_at, k.disabled_at, k.revoked_at, k.created_at, k.updated_at,
       (k.spend_limit IS NOT NULL AND k.spent_total >= k.spend_limit) AS spend_limit_reached,
       u.rpm_limit AS user_rpm_limit,
       u.rpd_limit AS user_rpd_limit,
       u.concurrency_limit AS user_concurrency_limit
FROM api_keys k
JOIN users u ON u.id = k.user_id
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
