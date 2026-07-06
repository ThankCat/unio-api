-- name: CreateRoute :one
-- CreateRoute 创建线路；price_ratio 是客户售价倍率（DEC-026：客户售价 = 模型基准价 × 倍率）；
-- rpm/tpm/rpd_limit 是线路级限流上限（DEC-027：NULL=继承全局默认，0=不限，>0=上限）；
-- mode/pool_kind 组合的 fixed/explicit 数量约束由 service 层校验。
INSERT INTO routes (name, mode, pool_kind, status, description, price_ratio, rpm_limit, tpm_limit, rpd_limit)
VALUES (
    sqlc.arg(name),
    sqlc.arg(mode),
    sqlc.arg(pool_kind),
    sqlc.arg(status),
    sqlc.narg(description),
    sqlc.arg(price_ratio),
    sqlc.narg(rpm_limit),
    sqlc.narg(tpm_limit),
    sqlc.narg(rpd_limit)
)
RETURNING *;

-- name: GetRouteByID :one
-- GetRouteByID 按主键读取单条线路。
SELECT * FROM routes WHERE id = sqlc.arg(id) LIMIT 1;

-- name: ListRoutes :many
-- ListRoutes 列出全部线路，供 admin 管理台展示。
SELECT * FROM routes ORDER BY id ASC;

-- name: UpdateRoute :one
-- UpdateRoute 更新线路的名称/策略/池类型/启停/简介/售价倍率/线路级限流上限。
UPDATE routes
SET name = sqlc.arg(name),
    mode = sqlc.arg(mode),
    pool_kind = sqlc.arg(pool_kind),
    status = sqlc.arg(status),
    description = sqlc.narg(description),
    price_ratio = sqlc.arg(price_ratio),
    rpm_limit = sqlc.narg(rpm_limit),
    tpm_limit = sqlc.narg(tpm_limit),
    rpd_limit = sqlc.narg(rpd_limit),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteRoute :execrows
-- DeleteRoute 删除线路；被 api_keys/users 引用时由 DB 外键拒绝（23503）。
DELETE FROM routes WHERE id = sqlc.arg(id);

-- name: CountApiKeysByRoute :one
-- CountApiKeysByRoute 统计绑定到某线路的 api_key 数量，供线路归档护栏（有 key 则拦截）。
SELECT COUNT(*) AS total FROM api_keys WHERE route_id = sqlc.arg(route_id);

-- name: ArchiveRoute :execrows
-- ArchiveRoute 归档线路（要求已无绑定 key，服务层护栏保证）：置 archived + 释放全局唯一线路名
-- （追加 __archived_<id> 后缀）。route_channels 保留（线路已隐藏，便于恢复）。
UPDATE routes
SET status = 'archived', archived_at = now(), name = name || '__archived_' || id::text
WHERE routes.id = sqlc.arg(id) AND routes.status <> 'archived';

-- name: ArchiveRouteWithKeyMigration :execrows
-- ArchiveRouteWithKeyMigration 单事务内先把源线路全部 api_key 迁到目标线路，再归档源线路
-- （§4B 入口②「迁移并归档」）。目标线路有效性（存在且 enabled、非自身）由服务层先校验。
WITH migrated AS (
    UPDATE api_keys SET route_id = sqlc.arg(target_route_id), updated_at = now()
    WHERE route_id = sqlc.arg(id)
)
UPDATE routes
SET status = 'archived', archived_at = now(), name = name || '__archived_' || id::text
WHERE routes.id = sqlc.arg(id) AND routes.status <> 'archived';

-- name: RestoreRoute :execrows
-- RestoreRoute 取消归档线路：archived → disabled（archived_at 清空）。route_channels 原样保留；
-- 归档前已无 key，恢复后仍无 key，需手动绑定或迁入。
UPDATE routes
SET status = 'disabled', archived_at = NULL
WHERE id = sqlc.arg(id) AND status = 'archived';

-- name: ListEmptyRoutesWithKeys :many
-- ListEmptyRoutesWithKeys 列出「候选池为空但仍有绑定 key」的非归档线路，供归档后预警断供。
SELECT rt.id, rt.name,
    (SELECT COUNT(*) FROM api_keys k WHERE k.route_id = rt.id) AS key_count
FROM routes rt
WHERE rt.status <> 'archived'
  AND NOT EXISTS (SELECT 1 FROM route_channels rc WHERE rc.route_id = rt.id)
  AND EXISTS (SELECT 1 FROM api_keys k WHERE k.route_id = rt.id)
ORDER BY rt.id;
