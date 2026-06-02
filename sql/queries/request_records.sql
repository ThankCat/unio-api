-- name: CreateRequestRecord :one
-- CreateRequestRecord 创建一次用户可见的 Unio API 请求记录。
INSERT INTO request_records (
    request_id,
    user_id,
    project_id,
    api_key_id,
    requested_model_id,
    ingress_protocol,
    operation,
    response_model_id,
    response_protocol,
    response_id,
    stream,
    status,
    final_provider_id,
    final_channel_id,
    error_code,
    error_message,
    internal_error_detail,
    delivery_status,
    response_started_at,
    response_completed_at,
    started_at,
    completed_at
)
VALUES (
           sqlc.arg(request_id),
           sqlc.arg(user_id),
           sqlc.arg(project_id),
           sqlc.arg(api_key_id),
           sqlc.arg(requested_model_id),
           sqlc.arg(ingress_protocol),
           sqlc.arg(operation),
           sqlc.arg(response_model_id),
           sqlc.arg(response_protocol),
           sqlc.arg(response_id),
           sqlc.arg(stream),
           sqlc.arg(status),
           sqlc.arg(final_provider_id),
           sqlc.arg(final_channel_id),
           sqlc.arg(error_code),
           sqlc.arg(error_message),
           sqlc.arg(internal_error_detail),
           sqlc.arg(delivery_status),
           sqlc.arg(response_started_at),
           sqlc.arg(response_completed_at),
           sqlc.arg(started_at),
           sqlc.arg(completed_at)
       )
RETURNING
    id,
    request_id,
    user_id,
    project_id,
    api_key_id,
    requested_model_id,
    ingress_protocol,
    operation,
    response_model_id,
    response_protocol,
    response_id,
    stream,
    status,
    final_provider_id,
    final_channel_id,
    error_code,
    error_message,
    internal_error_detail,
    delivery_status,
    response_started_at,
    response_completed_at,
    started_at,
    completed_at,
    created_at,
    updated_at;

-- name: GetRequestRecordForUpdate :one
-- GetRequestRecordForUpdate 锁定请求记录，串行化同一个 request 的并发结算。
-- running 表示本次可以继续首次 settlement；succeeded 表示可能是幂等重放。
SELECT
    id,
    request_id,
    user_id,
    project_id,
    api_key_id,
    requested_model_id,
    ingress_protocol,
    operation,
    response_model_id,
    response_protocol,
    response_id,
    stream,
    status,
    final_provider_id,
    final_channel_id,
    error_code,
    error_message,
    internal_error_detail,
    delivery_status,
    response_started_at,
    response_completed_at,
    started_at,
    completed_at,
    created_at,
    updated_at
FROM request_records
WHERE id = sqlc.arg(request_record_id)
    FOR UPDATE;

-- name: MarkRequestRunning :one
-- MarkRequestRunning 将 pending 请求原子推进到 running，重复 running 返回原事实。
-- succeeded/failed/canceled 是终态，不会被重新打开。
WITH updated AS (
    UPDATE request_records
        SET status = 'running',
            updated_at = now()
        WHERE request_records.id = sqlc.arg(request_record_id)
            AND request_records.status = 'pending'
        RETURNING request_records.*
)
SELECT *
FROM updated

UNION ALL

SELECT request_records.*
FROM request_records
WHERE request_records.id = sqlc.arg(request_record_id)
  AND request_records.status = 'running'
  AND NOT EXISTS (SELECT 1 FROM updated);

-- name: MarkRequestSucceeded :one
-- MarkRequestSucceeded 将 running 请求原子推进到 succeeded，重复 succeeded 返回第一次成功事实。
-- 重复 settlement 不能覆盖 response_model/provider/channel/completed_at。
WITH updated AS (
    UPDATE request_records
        SET status = 'succeeded',
            response_model_id = sqlc.arg(response_model_id),
            response_protocol = sqlc.arg(response_protocol),
            response_id = sqlc.arg(response_id),
            final_provider_id = sqlc.arg(final_provider_id),
            final_channel_id = sqlc.arg(final_channel_id),
            completed_at = sqlc.arg(completed_at),
            updated_at = now()
        WHERE request_records.id = sqlc.arg(request_record_id)
            AND request_records.status = 'running'
        RETURNING request_records.*
)
SELECT *
FROM updated

UNION ALL

SELECT request_records.*
FROM request_records
WHERE request_records.id = sqlc.arg(request_record_id)
  AND request_records.status = 'succeeded'
  AND NOT EXISTS (SELECT 1 FROM updated);

-- name: MarkRequestFailed :one
-- MarkRequestFailed 将 running 请求原子推进到 failed，重复 failed 返回第一次失败事实。
-- 重复失败写入不能覆盖 error_code/error_message/internal_error_detail/completed_at。
WITH updated AS (
    UPDATE request_records
        SET status = 'failed',
            error_code = sqlc.arg(error_code),
            error_message = sqlc.arg(error_message),
            internal_error_detail = sqlc.arg(internal_error_detail),
            completed_at = sqlc.arg(completed_at),
            updated_at = now()
        WHERE request_records.id = sqlc.arg(request_record_id)
            AND request_records.status = 'running'
        RETURNING request_records.*
)
SELECT *
FROM updated

UNION ALL

SELECT request_records.*
FROM request_records
WHERE request_records.id = sqlc.arg(request_record_id)
  AND request_records.status = 'failed'
  AND NOT EXISTS (SELECT 1 FROM updated);

-- name: MarkRequestCanceled :one
-- MarkRequestCanceled 将 running 请求原子推进到 canceled，重复 canceled 返回第一次取消事实。
-- 重复取消写入不能覆盖 error_code/error_message/internal_error_detail/completed_at。
WITH updated AS (
    UPDATE request_records
        SET status = 'canceled',
            error_code = sqlc.arg(error_code),
            error_message = sqlc.arg(error_message),
            internal_error_detail = sqlc.arg(internal_error_detail),
            completed_at = sqlc.arg(completed_at),
            updated_at = now()
        WHERE request_records.id = sqlc.arg(request_record_id)
            AND request_records.status = 'running'
        RETURNING request_records.*
)
SELECT *
FROM updated

UNION ALL

SELECT request_records.*
FROM request_records
WHERE request_records.id = sqlc.arg(request_record_id)
  AND request_records.status = 'canceled'
  AND NOT EXISTS (SELECT 1 FROM updated);
