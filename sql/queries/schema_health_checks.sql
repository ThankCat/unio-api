-- name: CreateSchemaHealthCheck :one
INSERT INTO schema_health_checks (name)
VALUES ($1)
RETURNING id, name, created_at;

-- name: GetSchemaHealthCheckByName :one
SELECT id, name, created_at
FROM schema_health_checks
WHERE name = $1;