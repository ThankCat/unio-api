-- name: CreateRoute :one
-- CreateRoute 创建自定义线路（is_builtin 恒 false）；mode/pool_kind 组合的 fixed/explicit 数量约束由 service 层校验。
INSERT INTO routes (name, mode, pool_kind, status, description, is_builtin)
VALUES (
    sqlc.arg(name),
    sqlc.arg(mode),
    sqlc.arg(pool_kind),
    sqlc.arg(status),
    sqlc.narg(description),
    false
)
RETURNING *;

-- name: GetRouteByID :one
-- GetRouteByID 按主键读取单条线路。
SELECT * FROM routes WHERE id = sqlc.arg(id) LIMIT 1;

-- name: ListRoutes :many
-- ListRoutes 列出全部线路，内置（经济/稳定）排在前，供 admin 管理台展示。
SELECT * FROM routes ORDER BY is_builtin DESC, id ASC;

-- name: GetBuiltinCheapestRoute :one
-- GetBuiltinCheapestRoute 读取内置「经济」线路，作为线路解析的最终回落。
SELECT * FROM routes WHERE is_builtin = true AND mode = 'cheapest' LIMIT 1;

-- name: UpdateRoute :one
-- UpdateRoute 更新自定义线路的名称/策略/池类型/启停/简介；内置线路只读由 service 层拦截。
UPDATE routes
SET name = sqlc.arg(name),
    mode = sqlc.arg(mode),
    pool_kind = sqlc.arg(pool_kind),
    status = sqlc.arg(status),
    description = sqlc.narg(description),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteRoute :execrows
-- DeleteRoute 删除自定义线路（内置线路不可删）；被 api_keys/projects 引用时由 DB 外键拒绝（23503）。
DELETE FROM routes WHERE id = sqlc.arg(id) AND is_builtin = false;
