-- name: CreateProviderEndpoint :one
-- CreateProviderEndpoint 在某 Provider 下创建一个 Endpoint（唯一 API Root / 公共故障域）。
-- base_url 必须已由服务层规范化；base_url_revision/status_revision 从 1 开始。
INSERT INTO provider_endpoints (provider_id, name, base_url, status)
VALUES (sqlc.arg(provider_id), sqlc.arg(name), sqlc.arg(base_url), sqlc.arg(status))
RETURNING id, provider_id, name, base_url, base_url_revision, status, status_revision, archived_at, created_at, updated_at;

-- name: GetProviderEndpoint :one
-- GetProviderEndpoint 按 id 读取单个 Endpoint。
SELECT id, provider_id, name, base_url, base_url_revision, status, status_revision, archived_at, created_at, updated_at
FROM provider_endpoints
WHERE id = $1
LIMIT 1;

-- name: ListProviderEndpointsByProvider :many
-- ListProviderEndpointsByProvider 列出某 Provider 下全部 Endpoint（按 id 升序）。
SELECT id, provider_id, name, base_url, base_url_revision, status, status_revision, archived_at, created_at, updated_at
FROM provider_endpoints
WHERE provider_id = $1
ORDER BY id;

-- name: ListProviderEndpointsPage :many
-- ListProviderEndpointsPage 按 provider/状态/关键字过滤后分页列出 Endpoint，连带 Provider 名称与所属 Channel 数。
SELECT
    pe.id, pe.provider_id, pe.name, pe.base_url, pe.base_url_revision, pe.status, pe.status_revision,
    pe.archived_at, pe.created_at, pe.updated_at,
    p.name AS provider_name,
    (SELECT COUNT(*) FROM channels c WHERE c.provider_endpoint_id = pe.id)::bigint AS channel_count
FROM provider_endpoints pe
JOIN providers p ON p.id = pe.provider_id
WHERE (sqlc.narg('provider_id')::bigint IS NULL OR pe.provider_id = sqlc.narg('provider_id')::bigint)
  AND (sqlc.narg('status')::text IS NULL OR pe.status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR pe.name ILIKE '%' || sqlc.narg('q')::text || '%'
    OR pe.base_url ILIKE '%' || sqlc.narg('q')::text || '%'
  )
ORDER BY pe.provider_id, pe.id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountProviderEndpoints :one
-- CountProviderEndpoints 与 ListProviderEndpointsPage 相同过滤条件下的总数。
SELECT COUNT(*) AS total
FROM provider_endpoints pe
WHERE (sqlc.narg('provider_id')::bigint IS NULL OR pe.provider_id = sqlc.narg('provider_id')::bigint)
  AND (sqlc.narg('status')::text IS NULL OR pe.status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR pe.name ILIKE '%' || sqlc.narg('q')::text || '%'
    OR pe.base_url ILIKE '%' || sqlc.narg('q')::text || '%'
  );

-- name: UpdateProviderEndpointName :one
-- UpdateProviderEndpointName 仅更新展示名（不触碰 base_url/status/revision）。
UPDATE provider_endpoints
SET name = sqlc.arg(name), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, provider_id, name, base_url, base_url_revision, status, status_revision, archived_at, created_at, updated_at;

-- name: UpdateProviderEndpointBaseURL :one
-- UpdateProviderEndpointBaseURL 更新规范化 base_url，并原子递增 base_url_revision。
-- 服务层只有在规范化结果真变化时才调用；同值更新不应调用本查询。
UPDATE provider_endpoints
SET base_url = sqlc.arg(base_url), base_url_revision = base_url_revision + 1, updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, provider_id, name, base_url, base_url_revision, status, status_revision, archived_at, created_at, updated_at;

-- name: UpdateProviderEndpointStatus :one
-- UpdateProviderEndpointStatus 更新有效 status，并原子递增 status_revision；archived 时置 archived_at。
-- 服务层只有在 status 真变化时才调用。
UPDATE provider_endpoints
SET status = sqlc.arg(status),
    status_revision = status_revision + 1,
    archived_at = CASE WHEN sqlc.arg(status)::text = 'archived' THEN now() ELSE NULL END,
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, provider_id, name, base_url, base_url_revision, status, status_revision, archived_at, created_at, updated_at;

-- name: ListProviderEndpointsByProviderForUpdate :many
-- ListProviderEndpointsByProviderForUpdate 在 Provider status 级联时按 id 升序锁定全部受影响 Endpoint。
SELECT id, provider_id, name, base_url, base_url_revision, status, status_revision, archived_at, created_at, updated_at
FROM provider_endpoints
WHERE provider_id = $1
ORDER BY id
FOR UPDATE;

-- name: CountChannelsByProviderEndpoint :one
-- CountChannelsByProviderEndpoint 返回绑定到某 Endpoint 的（未归档）Channel 数，用于归档护栏。
SELECT COUNT(*) AS total
FROM channels
WHERE provider_endpoint_id = sqlc.arg(provider_endpoint_id)
  AND status <> 'archived';
