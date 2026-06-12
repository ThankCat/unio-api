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
    capability_check_result,
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
    capability_check_result,
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

-- name: MarkRequestCapabilityCheckResult :exec
-- MarkRequestCapabilityCheckResult 写入本次请求的 capability 闸门判定结论审计（阶段 12 observe）。
-- 纯审计字段，与状态机解耦：任意非终态/终态都可写一次，不改 status。
UPDATE request_records
SET capability_check_result = sqlc.arg(capability_check_result)::text,
    updated_at = now()
WHERE id = sqlc.arg(request_record_id);

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

-- name: ListRequestRecordsPage :many
-- ListRequestRecordsPage 供 admin 只读查询台（M6）按过滤条件分页倒序列出请求记录。
-- 所有过滤项为 NULL 时不过滤；列表故意不 SELECT internal_error_detail（从 SQL 层脱敏，
-- 内部错误详情只在详情端点按 ?include_internal 显式开关返回）。
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
    capability_check_result,
    error_code,
    error_message,
    delivery_status,
    response_started_at,
    response_completed_at,
    started_at,
    completed_at,
    created_at,
    updated_at
FROM request_records
WHERE (sqlc.narg('user_id')::bigint IS NULL OR user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('project_id')::bigint IS NULL OR project_id = sqlc.narg('project_id')::bigint)
  AND (sqlc.narg('api_key_id')::bigint IS NULL OR api_key_id = sqlc.narg('api_key_id')::bigint)
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('model')::text IS NULL OR requested_model_id ILIKE '%' || sqlc.narg('model')::text || '%')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountRequestRecords :one
-- CountRequestRecords 返回与 ListRequestRecordsPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM request_records
WHERE (sqlc.narg('user_id')::bigint IS NULL OR user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('project_id')::bigint IS NULL OR project_id = sqlc.narg('project_id')::bigint)
  AND (sqlc.narg('api_key_id')::bigint IS NULL OR api_key_id = sqlc.narg('api_key_id')::bigint)
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('model')::text IS NULL OR requested_model_id ILIKE '%' || sqlc.narg('model')::text || '%')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz);

-- name: GetRequestRecordByRequestID :one
-- GetRequestRecordByRequestID 按对外 request_id 读取单条请求记录完整事实（含 internal_error_detail）。
-- 不加锁，仅供 admin 只读详情端点使用；是否回显内部详情由 service/handler 控制。
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
    capability_check_result,
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
WHERE request_id = sqlc.arg(request_id);
