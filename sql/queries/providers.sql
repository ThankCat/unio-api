-- name: ListProviders :many
-- ListProviders 列出全部 provider，按 id 升序，供 admin 管理台展示。
SELECT id, slug, name, status, created_at, updated_at
FROM providers
ORDER BY id;

-- name: ListProvidersPage :many
-- ListProvidersPage 按状态/关键字过滤后分页列出 provider；status、q 为 NULL 时不过滤。
SELECT id, slug, name, status, created_at, updated_at
FROM providers
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR slug ILIKE '%' || sqlc.narg('q')::text || '%'
    OR name ILIKE '%' || sqlc.narg('q')::text || '%'
  )
ORDER BY id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountProviders :one
-- CountProviders 返回与 ListProvidersPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM providers
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR slug ILIKE '%' || sqlc.narg('q')::text || '%'
    OR name ILIKE '%' || sqlc.narg('q')::text || '%'
  );

-- name: GetProvider :one
-- GetProvider 按 id 读取单个 provider。
SELECT id, slug, name, status, created_at, updated_at
FROM providers
WHERE id = $1
LIMIT 1;

-- name: CreateProvider :one
-- CreateProvider 创建 provider；slug 全局唯一由 DB 唯一约束保证。
INSERT INTO providers (slug, name, status)
VALUES (sqlc.arg(slug), sqlc.arg(name), sqlc.arg(status))
RETURNING id, slug, name, status, created_at, updated_at;

-- name: UpdateProvider :one
-- UpdateProvider 更新 provider 的展示名与启停状态；slug 作为稳定业务标识不可变。
UPDATE providers
SET name = sqlc.arg(name), status = sqlc.arg(status), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, slug, name, status, created_at, updated_at;
