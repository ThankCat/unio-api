-- name: CreateSchemaHealthCheck :one
-- CreateSchemaHealthCheck 创建一条 schema 健康检查记录。
INSERT INTO schema_health_checks (name)
VALUES ($1)
RETURNING id, name, created_at;

-- name: GetSchemaHealthCheckByName :one
-- GetSchemaHealthCheckByName 按名称读取 schema 健康检查记录。
SELECT id, name, created_at
FROM schema_health_checks
WHERE name = $1;
