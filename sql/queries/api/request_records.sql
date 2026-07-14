-- name: CreateRequestRecord :one
-- CreateRequestRecord 创建一次用户可见的 Unio API 请求记录。
INSERT INTO request_records (
    request_id,
    user_id,
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
    route_id,
    reasoning_effort,
    reasoning_budget_tokens,
    client_ip
)
VALUES (
           sqlc.arg(request_id),
           sqlc.arg(user_id),
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
           sqlc.arg(completed_at),
           sqlc.narg(route_id),
           sqlc.narg(reasoning_effort),
           sqlc.narg(reasoning_budget_tokens),
           sqlc.narg(client_ip)
       )
RETURNING
    id,
    request_id,
    user_id,
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
    updated_at,
    route_id,
    reasoning_effort,
    reasoning_budget_tokens,
    client_ip;

-- name: GetRequestRecordForUpdate :one
-- GetRequestRecordForUpdate 锁定请求记录，串行化同一个 request 的并发结算。
-- running 表示本次可以继续首次 settlement；succeeded 表示可能是幂等重放。
SELECT
    id,
    request_id,
    user_id,
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
    updated_at,
    route_id,
    reasoning_effort,
    reasoning_budget_tokens,
    client_ip
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

-- name: MarkRequestResponseStarted :one
-- MarkRequestResponseStarted 记录首次客户可见响应时间，并把交付状态从 not_started 推进到 in_progress。
-- 重复调用保留第一次时间，且不回退已更靠后的交付状态。首字节时 delivery 与 response_started_at 同写。
WITH updated AS (
    UPDATE request_records
        SET response_started_at = COALESCE(request_records.response_started_at, sqlc.arg(response_started_at)),
            delivery_status = CASE
                WHEN request_records.delivery_status = 'not_started' THEN 'in_progress'
                ELSE request_records.delivery_status
            END,
            updated_at = now()
        WHERE request_records.id = sqlc.arg(request_record_id)
          AND request_records.status IN ('running', 'succeeded')
        RETURNING request_records.*
)
SELECT *
FROM updated

UNION ALL

SELECT request_records.*
FROM request_records
WHERE request_records.id = sqlc.arg(request_record_id)
  AND request_records.response_started_at IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM updated);

-- name: MarkRequestDeliveryCompleted :one
-- MarkRequestDeliveryCompleted 在响应完整交付后把交付状态推进到 completed，并同语句落地
-- response_completed_at，满足 ck_request_records_delivery_completed_at。仅从 not_started/in_progress
-- 推进；重复调用返回当前 completed 行（幂等，最佳努力审计）。
WITH updated AS (
    UPDATE request_records
        SET delivery_status = 'completed',
            response_completed_at = sqlc.arg(response_completed_at),
            updated_at = now()
        WHERE request_records.id = sqlc.arg(request_record_id)
            AND request_records.delivery_status IN ('not_started', 'in_progress')
        RETURNING request_records.*
)
SELECT *
FROM updated

UNION ALL

SELECT request_records.*
FROM request_records
WHERE request_records.id = sqlc.arg(request_record_id)
  AND request_records.delivery_status = 'completed'
  AND NOT EXISTS (SELECT 1 FROM updated);

-- name: MarkRequestDeliveryInterrupted :one
-- MarkRequestDeliveryInterrupted 在交付中断（客户端取消、上游中断、尾部错误）时把交付状态推进到
-- interrupted。response_completed_at 保持 NULL。仅从 not_started/in_progress 推进；重复调用返回当前
-- interrupted 行（幂等，最佳努力审计）。
WITH updated AS (
    UPDATE request_records
        SET delivery_status = 'interrupted',
            updated_at = now()
        WHERE request_records.id = sqlc.arg(request_record_id)
            AND request_records.delivery_status IN ('not_started', 'in_progress')
        RETURNING request_records.*
)
SELECT *
FROM updated

UNION ALL

SELECT request_records.*
FROM request_records
WHERE request_records.id = sqlc.arg(request_record_id)
  AND request_records.delivery_status = 'interrupted'
  AND NOT EXISTS (SELECT 1 FROM updated);

-- name: MarkRequestSucceeded :one
-- MarkRequestSucceeded 将 running 请求原子推进到 succeeded，重复 succeeded 返回第一次成功事实。
-- 重复 settlement 不能覆盖 response_model/provider/channel/completed_at。
-- response_completed_at 属于交付状态机（仅在 delivery_status='completed' 时落地，见
-- ck_request_records_delivery_completed_at），结算阶段不写，避免违反约束导致结算失败。
WITH updated AS (
    UPDATE request_records
        SET status = 'succeeded',
            response_model_id = sqlc.arg(response_model_id),
            response_protocol = sqlc.arg(response_protocol),
            response_id = sqlc.arg(response_id),
            final_provider_id = sqlc.arg(final_provider_id),
            final_channel_id = sqlc.arg(final_channel_id),
            response_started_at = COALESCE(request_records.response_started_at, sqlc.narg(response_started_at)),
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

-- name: MarkSettledRequestFailed :one
-- MarkSettledRequestFailed 将 running 请求推进到 failed，但保留已结算响应事实（partial stream 上游中断）。
WITH updated AS (
    UPDATE request_records
        SET status = 'failed',
            response_model_id = sqlc.arg(response_model_id),
            response_protocol = sqlc.arg(response_protocol),
            response_id = sqlc.arg(response_id),
            final_provider_id = sqlc.arg(final_provider_id),
            final_channel_id = sqlc.arg(final_channel_id),
            error_code = sqlc.arg(error_code),
            error_message = sqlc.arg(error_message),
            internal_error_detail = sqlc.arg(internal_error_detail),
            response_started_at = COALESCE(request_records.response_started_at, sqlc.narg(response_started_at)),
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

-- name: MarkSettledRequestCanceled :one
-- MarkSettledRequestCanceled 将 running 请求推进到 canceled，但保留已结算响应事实（partial stream 客户端取消）。
-- 与 MarkRequestCanceled 不同：该路径已经写入 usage/price/ledger，只是客户主动中断交付。
WITH updated AS (
    UPDATE request_records
        SET status = 'canceled',
            response_model_id = sqlc.arg(response_model_id),
            response_protocol = sqlc.arg(response_protocol),
            response_id = sqlc.arg(response_id),
            final_provider_id = sqlc.arg(final_provider_id),
            final_channel_id = sqlc.arg(final_channel_id),
            error_code = sqlc.arg(error_code),
            error_message = sqlc.arg(error_message),
            internal_error_detail = sqlc.arg(internal_error_detail),
            response_started_at = COALESCE(request_records.response_started_at, sqlc.narg(response_started_at)),
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
