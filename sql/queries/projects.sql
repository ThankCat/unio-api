-- name: CreateProject :one
-- CreateProject 在指定用户下创建项目。
INSERT INTO projects (user_id, name)
VALUES ($1, $2)
RETURNING id, user_id, name, created_at, updated_at;

-- name: GetProjectForUser :one
-- GetProjectForUser 按 project_id 和 user_id 读取项目并校验归属。
SELECT id, user_id, name, created_at, updated_at
FROM projects
WHERE id = sqlc.arg(project_id)
AND user_id = sqlc.arg(user_id)
LIMIT 1;

-- name: ListProjectsPage :many
-- ListProjectsPage 供 admin 分页倒序列出项目；user_id 为空时列全部。
SELECT id, user_id, name, created_at, updated_at
FROM projects
WHERE (sqlc.narg('user_id')::bigint IS NULL OR user_id = sqlc.narg('user_id')::bigint)
ORDER BY id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountProjects :one
-- CountProjects 供 admin 项目列表分页统计总数；user_id 为空时统计全部。
SELECT COUNT(*)
FROM projects
WHERE (sqlc.narg('user_id')::bigint IS NULL OR user_id = sqlc.narg('user_id')::bigint);

-- name: GetProjectByID :one
-- GetProjectByID 供 admin 按 id 读取项目。
SELECT id, user_id, name, created_at, updated_at
FROM projects
WHERE id = sqlc.arg(id)
LIMIT 1;
