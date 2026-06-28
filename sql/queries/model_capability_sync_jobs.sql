-- name: CreateSyncJob :one
-- CreateSyncJob 创建一条 pending 状态的能力同步任务。
INSERT INTO model_capability_sync_jobs (
    source,
    status
)
VALUES (
    sqlc.arg(source),
    'pending'
)
RETURNING *;

-- name: MarkSyncJobRunning :one
-- MarkSyncJobRunning 将同步任务标记为 running 并记录开始时间。
UPDATE model_capability_sync_jobs
SET status = 'running',
    started_at = now()
WHERE id = sqlc.arg(id)
    AND status = 'pending'
RETURNING *;

-- name: MarkSyncJobSucceeded :one
-- MarkSyncJobSucceeded 将同步任务标记为 succeeded 并落统计。
UPDATE model_capability_sync_jobs
SET status = 'succeeded',
    finished_at = now(),
    stats_json = sqlc.arg(stats_json)
WHERE id = sqlc.arg(id)
    AND status = 'running'
RETURNING *;

-- name: MarkSyncJobFailed :one
-- MarkSyncJobFailed 将同步任务标记为 failed 并记录失败原因。
UPDATE model_capability_sync_jobs
SET status = 'failed',
    finished_at = now(),
    error_text = sqlc.arg(error_text)
WHERE id = sqlc.arg(id)
    AND status IN ('pending', 'running')
RETURNING *;

-- name: GetLatestSyncJob :one
-- GetLatestSyncJob 读取指定来源最近一次同步任务。
SELECT *
FROM model_capability_sync_jobs
WHERE source = sqlc.arg(source)
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: ListSyncJobs :many
-- ListSyncJobs 分页倒序列出能力同步任务（admin 同步页展示用，不区分来源）。
SELECT *
FROM model_capability_sync_jobs
ORDER BY
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'created_at') IN ('', 'created_at') AND COALESCE(sqlc.narg('sort_desc')::bool, true) THEN created_at END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'created_at') IN ('', 'created_at') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, true) THEN created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN status END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN status END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'source' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN source END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'source' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN source END ASC NULLS LAST,
  id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountSyncJobs :one
-- CountSyncJobs 返回能力同步任务总条数。
SELECT COUNT(*) AS total
FROM model_capability_sync_jobs;
