-- name: ListRequestAttemptsByRequest :many
-- ListRequestAttemptsByRequest 按请求 ID 列出完整上游尝试链路。
SELECT *
FROM request_attempts
WHERE request_record_id = sqlc.arg(request_record_id)
ORDER BY attempt_index;
