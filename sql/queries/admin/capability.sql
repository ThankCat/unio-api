-- name: ListCapabilityKeys :many
-- ListCapabilityKeys 列出能力 key 字典（Admin 下拉/矩阵/字典页，含中文描述）。
SELECT key, domain, display_name, description, sort_order, deprecated, protocol_scope, created_at, updated_at
FROM capability_keys
ORDER BY sort_order, key;

-- name: GetCapabilityKey :one
-- GetCapabilityKey 按 key 读取字典行。
SELECT key, domain, display_name, description, sort_order, deprecated, protocol_scope, created_at, updated_at
FROM capability_keys
WHERE key = sqlc.arg(key);

-- name: CreateCapabilityKey :one
-- CreateCapabilityKey 新增能力 key 字典行（key 创建后不可改）。
INSERT INTO capability_keys (
    key, domain, display_name, description, sort_order, deprecated, protocol_scope
) VALUES (
    sqlc.arg(key), sqlc.arg(domain), sqlc.arg(display_name), sqlc.arg(description),
    sqlc.arg(sort_order), sqlc.arg(deprecated), sqlc.arg(protocol_scope)
)
RETURNING key, domain, display_name, description, sort_order, deprecated, protocol_scope, created_at, updated_at;

-- name: UpdateCapabilityKey :one
-- UpdateCapabilityKey 更新字典元数据（不含 key 本身）。
UPDATE capability_keys
SET
    domain = sqlc.arg(domain),
    display_name = sqlc.arg(display_name),
    description = sqlc.arg(description),
    sort_order = sqlc.arg(sort_order),
    deprecated = sqlc.arg(deprecated),
    protocol_scope = sqlc.arg(protocol_scope),
    updated_at = now()
WHERE key = sqlc.arg(key)
RETURNING key, domain, display_name, description, sort_order, deprecated, protocol_scope, created_at, updated_at;

-- name: DeleteCapabilityKey :exec
-- DeleteCapabilityKey 删除字典行；被 model_capabilities 引用时由 FK RESTRICT 拒绝。
DELETE FROM capability_keys WHERE key = sqlc.arg(key);

-- name: CapabilityKeyExists :one
-- CapabilityKeyExists 判断能力 key 是否在字典内（写入 model_capabilities 前的合法性校验）。
SELECT EXISTS (SELECT 1 FROM capability_keys WHERE key = sqlc.arg(key)) AS exists;

-- name: ListSyncJobs :many
-- ListSyncJobs 分页倒序列出能力同步任务（admin 同步页展示用，不区分来源）。
SELECT *
FROM model_capability_sync_jobs
ORDER BY
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'created_at') IN ('', 'created_at') AND COALESCE(sqlc.narg('sort_desc')::bool, true) THEN created_at END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'created_at') IN ('', 'created_at') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, true) THEN created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN status END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN status END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'source' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN source END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'source' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN source END ASC NULLS LAST,
  id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountSyncJobs :one
-- CountSyncJobs 返回能力同步任务总条数。
SELECT COUNT(*) AS total
FROM model_capability_sync_jobs;
