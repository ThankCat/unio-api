-- name: CreateProviderOrigin :one
-- CreateProviderOrigin 在某 Provider 下创建一个 Origin（唯一 API Root / 公共故障域）。
-- base_url 必须已由服务层规范化；base_url_revision/status_revision 从 1 开始。
INSERT INTO provider_origins (provider_id, name, base_url, status)
VALUES (sqlc.arg(provider_id), sqlc.arg(name), sqlc.arg(base_url), sqlc.arg(status))
RETURNING id, provider_id, name, base_url, base_url_revision, status, status_revision, archived_at, created_at, updated_at;

-- name: GetProviderOrigin :one
-- GetProviderOrigin 按 id 读取单个 Origin。
SELECT id, provider_id, name, base_url, base_url_revision, status, status_revision, archived_at, created_at, updated_at
FROM provider_origins
WHERE id = $1
LIMIT 1;

-- name: ListProviderOriginsByProvider :many
-- ListProviderOriginsByProvider 列出某 Provider 下全部 Origin（按 id 升序）。
SELECT id, provider_id, name, base_url, base_url_revision, status, status_revision, archived_at, created_at, updated_at
FROM provider_origins
WHERE provider_id = $1
ORDER BY id;

-- name: ListProviderOriginsPage :many
-- ListProviderOriginsPage 按 provider/状态/关键字过滤后分页列出 Origin，连带 Provider 名称与所属 Channel 数。
SELECT
    pe.id, pe.provider_id, pe.name, pe.base_url, pe.base_url_revision, pe.status, pe.status_revision,
    pe.archived_at, pe.created_at, pe.updated_at,
    p.name AS provider_name,
    (SELECT COUNT(*) FROM channels c WHERE c.provider_origin_id = pe.id)::bigint AS channel_count
FROM provider_origins pe
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

-- name: CountProviderOrigins :one
-- CountProviderOrigins 与 ListProviderOriginsPage 相同过滤条件下的总数。
SELECT COUNT(*) AS total
FROM provider_origins pe
WHERE (sqlc.narg('provider_id')::bigint IS NULL OR pe.provider_id = sqlc.narg('provider_id')::bigint)
  AND (sqlc.narg('status')::text IS NULL OR pe.status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR pe.name ILIKE '%' || sqlc.narg('q')::text || '%'
    OR pe.base_url ILIKE '%' || sqlc.narg('q')::text || '%'
  );

-- name: UpdateProviderOriginName :one
-- UpdateProviderOriginName 仅更新展示名（不触碰 base_url/status/revision）。
UPDATE provider_origins
SET name = sqlc.arg(name), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, provider_id, name, base_url, base_url_revision, status, status_revision, archived_at, created_at, updated_at;

-- name: UpdateProviderOriginBaseURL :one
-- UpdateProviderOriginBaseURL 更新规范化 base_url，并原子递增 base_url_revision。
-- 服务层只有在规范化结果真变化时才调用；同值更新不应调用本查询。
UPDATE provider_origins
SET base_url = sqlc.arg(base_url), base_url_revision = base_url_revision + 1, updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, provider_id, name, base_url, base_url_revision, status, status_revision, archived_at, created_at, updated_at;

-- name: UpdateProviderOriginStatus :one
-- UpdateProviderOriginStatus 更新有效 status，并原子递增 status_revision；archived 时置 archived_at。
-- 服务层只有在 status 真变化时才调用。
UPDATE provider_origins
SET status = sqlc.arg(status),
    status_revision = status_revision + 1,
    archived_at = CASE WHEN sqlc.arg(status)::text = 'archived' THEN now() ELSE NULL END,
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, provider_id, name, base_url, base_url_revision, status, status_revision, archived_at, created_at, updated_at;

-- name: ListProviderOriginsByProviderForUpdate :many
-- ListProviderOriginsByProviderForUpdate 在 Provider status 级联时按 id 升序锁定全部受影响 Origin。
SELECT id, provider_id, name, base_url, base_url_revision, status, status_revision, archived_at, created_at, updated_at
FROM provider_origins
WHERE provider_id = $1
ORDER BY id
FOR UPDATE;

-- name: CountChannelsByProviderOrigin :one
-- CountChannelsByProviderOrigin 返回绑定到某 Origin 的（未归档）Channel 数，用于归档护栏。
SELECT COUNT(*) AS total
FROM channels
WHERE provider_origin_id = sqlc.arg(provider_origin_id)
  AND status <> 'archived';
