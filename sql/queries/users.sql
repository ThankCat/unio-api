-- name: CreateUser :one
-- CreateUser 创建用户账号并返回用户事实。
INSERT INTO users (email, password_hash, display_name)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUserByEmail :one
-- GetUserByEmail 按邮箱大小写不敏感读取用户账号。
SELECT id, email, password_hash, display_name, created_at, updated_at
FROM users
WHERE lower(email) = lower(sqlc.arg(email))
LIMIT 1;

-- name: ListUsersPage :many
-- ListUsersPage 供 admin 分页倒序列出用户（不返回 password_hash）；q 为空不过滤。
SELECT u.id, u.email, u.display_name, u.created_at, u.updated_at
FROM users u
WHERE (sqlc.narg('q')::text IS NULL
       OR u.email ILIKE '%' || sqlc.narg('q')::text || '%'
       OR u.display_name ILIKE '%' || sqlc.narg('q')::text || '%')
ORDER BY u.id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountUsers :one
-- CountUsers 供 admin 用户列表分页统计总数；q 为空不过滤。
SELECT COUNT(*)
FROM users
WHERE (sqlc.narg('q')::text IS NULL
       OR email ILIKE '%' || sqlc.narg('q')::text || '%'
       OR display_name ILIKE '%' || sqlc.narg('q')::text || '%');

-- name: GetUserByID :one
-- GetUserByID 供 admin 按 id 读取用户（不返回 password_hash）。
SELECT u.id, u.email, u.display_name, u.created_at, u.updated_at
FROM users u
WHERE u.id = sqlc.arg(id)
LIMIT 1;
