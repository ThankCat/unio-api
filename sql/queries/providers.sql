-- name: ListProviders :many
-- ListProviders 列出全部 provider，按 id 升序，供 admin 管理台展示。
SELECT id, slug, name, status, created_at, updated_at, archived_at
FROM providers
ORDER BY id;

-- name: ListProvidersPage :many
-- ListProvidersPage 按状态/关键字过滤后分页列出 provider；status、q 为 NULL 时不过滤。
SELECT id, slug, name, status, created_at, updated_at, archived_at
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
SELECT id, slug, name, status, created_at, updated_at, archived_at
FROM providers
WHERE id = $1
LIMIT 1;

-- name: CreateProvider :one
-- CreateProvider 创建 provider；slug 全局唯一由 DB 唯一约束保证。
INSERT INTO providers (slug, name, status)
VALUES (sqlc.arg(slug), sqlc.arg(name), sqlc.arg(status))
RETURNING id, slug, name, status, created_at, updated_at, archived_at;

-- name: UpdateProvider :one
-- UpdateProvider 更新 provider 的展示名与启停状态；slug 作为稳定业务标识不可变。
UPDATE providers
SET name = sqlc.arg(name), status = sqlc.arg(status), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, slug, name, status, created_at, updated_at, archived_at;

-- name: DeleteProvider :execrows
-- DeleteProvider 物理删除 provider，用于清理录错且从未使用的脏数据。
-- provider 无自身配置子表，故不级联：名下仍有 channel，或被 request/cost 历史（NO ACTION 外键）
-- 引用时，DB 拒绝删除（23503），上层降级为 conflict，提示先删渠道或改用停用。
DELETE FROM providers WHERE id = sqlc.arg(id);

-- name: ArchiveProviderCascade :execrows
-- ArchiveProviderCascade 归档 provider：单事务内把名下未归档渠道一并归档（释放渠道名：追加
-- __archived_<id> 后缀）、从所有线路候选池移除这些渠道（route_channels 纯配置、无账务外键，可安全删），
-- 再把 provider 置 archived。slug 与 provider.name 不变（服务商标识稳定，归档大概率不再复用同 slug）。
-- 返回 providers 受影响行数（0 = provider 不存在或已归档）。恢复不向下级联，需逐个恢复渠道。
WITH archived_channels AS (
    UPDATE channels
    SET status = 'archived', archived_at = now(), name = name || '__archived_' || id::text
    WHERE provider_id = sqlc.arg(id) AND status <> 'archived'
    RETURNING id
),
cleared_pools AS (
    DELETE FROM route_channels WHERE channel_id IN (SELECT id FROM archived_channels)
)
UPDATE providers
SET status = 'archived', archived_at = now()
WHERE providers.id = sqlc.arg(id) AND providers.status <> 'archived';

-- name: RestoreProvider :execrows
-- RestoreProvider 取消归档 provider：archived → disabled（archived_at 清空）。不向下级联恢复渠道。
UPDATE providers
SET status = 'disabled', archived_at = NULL
WHERE id = sqlc.arg(id) AND status = 'archived';
