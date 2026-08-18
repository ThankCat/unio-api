-- name: CreateConsoleUser :one
INSERT INTO users (uid, email, password_hash, display_name, status)
VALUES (sqlc.arg(uid), sqlc.arg(email), sqlc.arg(password_hash), sqlc.arg(display_name), 'active')
RETURNING id, uid, email, password_hash, display_name, status, created_at, updated_at;

-- name: GetConsoleUserByEmail :one
SELECT id, uid, email, password_hash, display_name, status, created_at, updated_at
FROM users
WHERE lower(email) = lower(sqlc.arg(email))
LIMIT 1;

-- name: ConsoleRegistrationEmailExists :one
SELECT EXISTS (
    SELECT 1
    FROM users
    WHERE lower(email) = lower(sqlc.arg(email))
);

-- name: GetConsoleUserByUID :one
SELECT id, uid, email, password_hash, display_name, status, created_at, updated_at
FROM users
WHERE uid = sqlc.arg(uid)
LIMIT 1;

-- name: UpdateConsolePassword :one
UPDATE users
SET password_hash = sqlc.arg(password_hash), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, uid, email, password_hash, display_name, status, created_at, updated_at;
