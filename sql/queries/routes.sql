-- name: CreateRoute :one
-- CreateRoute 创建线路；price_ratio 是客户售价倍率（DEC-026：客户售价 = 模型基准价 × 倍率）；
-- mode/pool_kind 组合的 fixed/explicit 数量约束由 service 层校验。
INSERT INTO routes (name, mode, pool_kind, status, description, price_ratio)
VALUES (
    sqlc.arg(name),
    sqlc.arg(mode),
    sqlc.arg(pool_kind),
    sqlc.arg(status),
    sqlc.narg(description),
    sqlc.arg(price_ratio)
)
RETURNING *;

-- name: GetRouteByID :one
-- GetRouteByID 按主键读取单条线路。
SELECT * FROM routes WHERE id = sqlc.arg(id) LIMIT 1;

-- name: ListRoutes :many
-- ListRoutes 列出全部线路，供 admin 管理台展示。
SELECT * FROM routes ORDER BY id ASC;

-- name: UpdateRoute :one
-- UpdateRoute 更新线路的名称/策略/池类型/启停/简介/售价倍率。
UPDATE routes
SET name = sqlc.arg(name),
    mode = sqlc.arg(mode),
    pool_kind = sqlc.arg(pool_kind),
    status = sqlc.arg(status),
    description = sqlc.narg(description),
    price_ratio = sqlc.arg(price_ratio),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteRoute :execrows
-- DeleteRoute 删除线路；被 api_keys/users 引用时由 DB 外键拒绝（23503）。
DELETE FROM routes WHERE id = sqlc.arg(id);
