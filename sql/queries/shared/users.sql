-- name: GetUserByEmail :one
-- GetUserByEmail 按邮箱大小写不敏感读取用户账号。
SELECT id, email, password_hash, display_name, created_at, updated_at
FROM users
WHERE lower(email) = lower(sqlc.arg(email))
LIMIT 1;
