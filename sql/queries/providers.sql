-- name: ListProviders :many
-- ListProviders 列出全部 provider，按 id 升序，供 admin 管理台展示。
SELECT id, slug, name, status, created_at, updated_at
FROM providers
ORDER BY id;

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
