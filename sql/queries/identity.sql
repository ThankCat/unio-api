-- name: CreateUser :one
INSERT INTO users (email, password_hash, display_name)
VALUES ($1, $2, $3)
RETURNING id, email, password_hash, display_name, created_at, updated_at;

-- name: GetUserByEmail :one
SELECT id, email, password_hash, display_name, created_at, updated_at
FROM users
WHERE lower(email) = lower(sqlc.arg(email))
LIMIT 1;

-- name: CreateProject :one
INSERT INTO projects (user_id, name)
VALUES ($1, $2)
RETURNING id, user_id, name, created_at, updated_at;

-- name: CreateAPIKey :one
INSERT INTO api_keys (project_id, name, key_prefix, key_hash, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, project_id, name, key_prefix, key_hash, last_used_at, expires_at, disabled_at, revoked_at, created_at, updated_at;

-- name: GetAPIKeyByHash :one
SELECT id, project_id, name, key_prefix, key_hash, last_used_at, expires_at, disabled_at, revoked_at, created_at, updated_at
FROM api_keys
WHERE key_hash = $1
LIMIT 1;