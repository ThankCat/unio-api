-- name: CreateRequestAttempt :one
-- CreateRequestAttempt 创建一次请求下的一次上游 channel 尝试记录。
INSERT INTO request_attempts (
    request_record_id,
    attempt_index,
    provider_id,
    channel_id,
    adapter_key,
    upstream_model,
    upstream_protocol,
    upstream_response_id,
    upstream_response_model,
    upstream_finish_reason,
    finish_class,
    status,
    upstream_status_code,
    upstream_request_id,
    error_code,
    error_message,
    internal_error_detail,
    response_started_at,
    final_usage_received,
    usage_mapping_version,
    started_at,
    completed_at
)
VALUES (
           sqlc.arg(request_record_id),
           sqlc.arg(attempt_index),
           sqlc.arg(provider_id),
           sqlc.arg(channel_id),
           sqlc.arg(adapter_key),
           sqlc.arg(upstream_model),
           sqlc.arg(upstream_protocol),
           sqlc.arg(upstream_response_id),
           sqlc.arg(upstream_response_model),
           sqlc.arg(upstream_finish_reason),
           sqlc.arg(finish_class),
           sqlc.arg(status),
           sqlc.arg(upstream_status_code),
           sqlc.arg(upstream_request_id),
           sqlc.arg(error_code),
           sqlc.arg(error_message),
           sqlc.arg(internal_error_detail),
           sqlc.arg(response_started_at),
           sqlc.arg(final_usage_received),
           sqlc.arg(usage_mapping_version),
           sqlc.arg(started_at),
           sqlc.arg(completed_at)
       )
RETURNING
    id,
    request_record_id,
    attempt_index,
    provider_id,
    channel_id,
    adapter_key,
    upstream_model,
    upstream_protocol,
    upstream_response_id,
    upstream_response_model,
    upstream_finish_reason,
    finish_class,
    status,
    upstream_status_code,
    upstream_request_id,
    error_code,
    error_message,
    internal_error_detail,
    response_started_at,
    final_usage_received,
    usage_mapping_version,
    started_at,
    completed_at,
    created_at;

-- name: MarkRequestAttemptResponseStarted :one
-- MarkRequestAttemptResponseStarted 记录一次 attempt 的首次客户可见响应时间；重复调用保留第一次时间。
WITH updated AS (
    UPDATE request_attempts
        SET response_started_at = COALESCE(request_attempts.response_started_at, sqlc.arg(response_started_at))
        WHERE request_attempts.id = sqlc.arg(attempt_id)
          AND request_attempts.status IN ('running', 'succeeded')
        RETURNING request_attempts.*
)
SELECT *
FROM updated

UNION ALL

SELECT request_attempts.*
FROM request_attempts
WHERE request_attempts.id = sqlc.arg(attempt_id)
  AND request_attempts.response_started_at IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM updated);

-- name: MarkRequestAttemptSucceeded :one
-- MarkRequestAttemptSucceeded 将 running attempt 原子推进到 succeeded，重复 succeeded 返回第一次成功事实。
-- 重复成功写入不能覆盖 upstream response metadata。
WITH updated AS (
    UPDATE request_attempts
        SET status = 'succeeded',
            upstream_response_id = sqlc.arg(upstream_response_id),
            upstream_response_model = sqlc.arg(upstream_response_model),
            upstream_finish_reason = sqlc.arg(upstream_finish_reason),
            finish_class = sqlc.arg(finish_class),
            upstream_status_code = sqlc.arg(upstream_status_code),
            upstream_request_id = sqlc.arg(upstream_request_id),
            response_started_at = COALESCE(request_attempts.response_started_at, sqlc.narg(response_started_at)),
            final_usage_received = sqlc.arg(final_usage_received),
            usage_mapping_version = sqlc.arg(usage_mapping_version),
            completed_at = sqlc.arg(completed_at)
        WHERE request_attempts.id = sqlc.arg(attempt_id)
            AND request_attempts.status = 'running'
        RETURNING request_attempts.*
)
SELECT *
FROM updated

UNION ALL

SELECT request_attempts.*
FROM request_attempts
WHERE request_attempts.id = sqlc.arg(attempt_id)
  AND request_attempts.status = 'succeeded'
  AND NOT EXISTS (SELECT 1 FROM updated);

-- name: MarkRequestAttemptFailed :one
-- MarkRequestAttemptFailed 将 running attempt 原子推进到 failed，重复 failed 返回第一次失败事实。
-- 重复失败写入不能覆盖 error/upstream metadata。
WITH updated AS (
    UPDATE request_attempts
        SET status = 'failed',
            upstream_status_code = sqlc.arg(upstream_status_code),
            upstream_request_id = sqlc.arg(upstream_request_id),
            error_code = sqlc.arg(error_code),
            error_message = sqlc.arg(error_message),
            internal_error_detail = sqlc.arg(internal_error_detail),
            completed_at = sqlc.arg(completed_at)
        WHERE request_attempts.id = sqlc.arg(attempt_id)
            AND request_attempts.status = 'running'
        RETURNING request_attempts.*
)
SELECT *
FROM updated

UNION ALL

SELECT request_attempts.*
FROM request_attempts
WHERE request_attempts.id = sqlc.arg(attempt_id)
  AND request_attempts.status = 'failed'
  AND NOT EXISTS (SELECT 1 FROM updated);

-- name: MarkSettledRequestAttemptFailed :one
-- MarkSettledRequestAttemptFailed 将 running attempt 推进到 failed，但保留已结算上游事实（partial stream 上游中断）。
WITH updated AS (
    UPDATE request_attempts
        SET status = 'failed',
            upstream_response_id = sqlc.arg(upstream_response_id),
            upstream_response_model = sqlc.arg(upstream_response_model),
            upstream_finish_reason = sqlc.arg(upstream_finish_reason),
            finish_class = sqlc.arg(finish_class),
            upstream_status_code = sqlc.arg(upstream_status_code),
            upstream_request_id = sqlc.arg(upstream_request_id),
            error_code = sqlc.arg(error_code),
            error_message = sqlc.arg(error_message),
            internal_error_detail = sqlc.arg(internal_error_detail),
            response_started_at = COALESCE(request_attempts.response_started_at, sqlc.narg(response_started_at)),
            final_usage_received = sqlc.arg(final_usage_received),
            usage_mapping_version = sqlc.arg(usage_mapping_version),
            completed_at = sqlc.arg(completed_at)
        WHERE request_attempts.id = sqlc.arg(attempt_id)
            AND request_attempts.status = 'running'
        RETURNING request_attempts.*
)
SELECT *
FROM updated

UNION ALL

SELECT request_attempts.*
FROM request_attempts
WHERE request_attempts.id = sqlc.arg(attempt_id)
  AND request_attempts.status = 'failed'
  AND NOT EXISTS (SELECT 1 FROM updated);

-- name: MarkRequestAttemptCanceled :one
-- MarkRequestAttemptCanceled 将 running attempt 原子推进到 canceled，重复 canceled 返回第一次取消事实。
-- 重复取消写入不能覆盖 error metadata。
WITH updated AS (
    UPDATE request_attempts
        SET status = 'canceled',
            error_code = sqlc.arg(error_code),
            error_message = sqlc.arg(error_message),
            internal_error_detail = sqlc.arg(internal_error_detail),
            completed_at = sqlc.arg(completed_at)
        WHERE request_attempts.id = sqlc.arg(attempt_id)
            AND request_attempts.status = 'running'
        RETURNING request_attempts.*
)
SELECT *
FROM updated

UNION ALL

SELECT request_attempts.*
FROM request_attempts
WHERE request_attempts.id = sqlc.arg(attempt_id)
  AND request_attempts.status = 'canceled'
  AND NOT EXISTS (SELECT 1 FROM updated);

-- name: MarkSettledRequestAttemptCanceled :one
-- MarkSettledRequestAttemptCanceled 将 running attempt 推进到 canceled，但保留已结算上游事实（partial stream 客户端取消）。
WITH updated AS (
    UPDATE request_attempts
        SET status = 'canceled',
            upstream_response_id = sqlc.arg(upstream_response_id),
            upstream_response_model = sqlc.arg(upstream_response_model),
            upstream_finish_reason = sqlc.arg(upstream_finish_reason),
            finish_class = sqlc.arg(finish_class),
            upstream_status_code = sqlc.arg(upstream_status_code),
            upstream_request_id = sqlc.arg(upstream_request_id),
            error_code = sqlc.arg(error_code),
            error_message = sqlc.arg(error_message),
            internal_error_detail = sqlc.arg(internal_error_detail),
            response_started_at = COALESCE(request_attempts.response_started_at, sqlc.narg(response_started_at)),
            final_usage_received = sqlc.arg(final_usage_received),
            usage_mapping_version = sqlc.arg(usage_mapping_version),
            completed_at = sqlc.arg(completed_at)
        WHERE request_attempts.id = sqlc.arg(attempt_id)
            AND request_attempts.status = 'running'
        RETURNING request_attempts.*
)
SELECT *
FROM updated

UNION ALL

SELECT request_attempts.*
FROM request_attempts
WHERE request_attempts.id = sqlc.arg(attempt_id)
  AND request_attempts.status = 'canceled'
  AND NOT EXISTS (SELECT 1 FROM updated);

-- name: ListRequestAttemptsByRequest :many
-- ListRequestAttemptsByRequest 按请求 ID 列出完整上游尝试链路。
SELECT *
FROM request_attempts
WHERE request_record_id = sqlc.arg(request_record_id)
ORDER BY attempt_index;
