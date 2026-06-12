-- name: CreateAPIKey :one
-- CreateAPIKey 创建项目 API Key，只保存 key hash 和展示前缀。
INSERT INTO api_keys (project_id, name, key_prefix, key_hash, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, project_id, name, key_prefix, key_hash, last_used_at, expires_at, disabled_at, revoked_at, created_at, updated_at, spend_limit, spent_total;

-- name: GetAPIKeyByHash :one
-- GetAPIKeyByHash 按 key hash 读取 API Key，带出所属用户 ID，并计算是否已达费用上限。
-- spend_limit_reached 在 SQL 层判定，避免认证路径在 Go 里做 NUMERIC 比较（M7 费用上限闸门）。
SELECT k.id, p.user_id, k.project_id, k.name, k.key_prefix, k.key_hash, k.last_used_at, k.expires_at, k.disabled_at, k.revoked_at, k.created_at, k.updated_at,
       (k.spend_limit IS NOT NULL AND k.spent_total >= k.spend_limit) AS spend_limit_reached
FROM api_keys k
JOIN projects p ON p.id = k.project_id
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

-- name: ListAPIKeysByProjectPage :many
-- ListAPIKeysByProjectPage 供 admin 按项目分页倒序列出 API Key（不返回 key_hash）。
SELECT id, project_id, name, key_prefix, last_used_at, expires_at, disabled_at, revoked_at, spend_limit, spent_total, created_at, updated_at
FROM api_keys
WHERE project_id = sqlc.arg(project_id)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountAPIKeysByProject :one
-- CountAPIKeysByProject 供 admin API Key 列表分页统计总数。
SELECT COUNT(*) FROM api_keys WHERE project_id = sqlc.arg(project_id);

-- name: GetAPIKeyByID :one
-- GetAPIKeyByID 供 admin 按 id 读取单把 API Key（带所属用户 ID）。
SELECT k.id, p.user_id, k.project_id, k.name, k.key_prefix, k.last_used_at, k.expires_at, k.disabled_at, k.revoked_at, k.spend_limit, k.spent_total, k.created_at, k.updated_at
FROM api_keys k
JOIN projects p ON p.id = k.project_id
WHERE k.id = sqlc.arg(id)
LIMIT 1;

-- name: SetAPIKeyDisabled :one
-- SetAPIKeyDisabled 启停 API Key：disabled_at 置 now() 为停用，置 NULL 为启用。
UPDATE api_keys
SET disabled_at = sqlc.narg(disabled_at), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, project_id, name, key_prefix, last_used_at, expires_at, disabled_at, revoked_at, spend_limit, spent_total, created_at, updated_at;

-- name: RevokeAPIKey :one
-- RevokeAPIKey 永久吊销 API Key（revoked_at 置 now()，不可逆）。
UPDATE api_keys
SET revoked_at = now(), updated_at = now()
WHERE id = sqlc.arg(id) AND revoked_at IS NULL
RETURNING id, project_id, name, key_prefix, last_used_at, expires_at, disabled_at, revoked_at, spend_limit, spent_total, created_at, updated_at;

-- name: SetAPIKeySpendLimit :one
-- SetAPIKeySpendLimit 设置/清除 API Key 费用上限；spend_limit 为 NULL 表示不限额。
UPDATE api_keys
SET spend_limit = sqlc.narg(spend_limit), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, project_id, name, key_prefix, last_used_at, expires_at, disabled_at, revoked_at, spend_limit, spent_total, created_at, updated_at;
