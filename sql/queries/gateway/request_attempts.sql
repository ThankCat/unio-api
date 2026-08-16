-- name: CreateRequestAttempt :one
-- CreateRequestAttempt 创建一次请求下的一次上游 channel 尝试记录。
-- request_guard 与孤儿收口对 request_records 的 FOR UPDATE 互斥，避免已判定死亡后又迟到插入 attempt。
WITH request_guard AS (
    SELECT request_records.id AS guarded_request_record_id
    FROM request_records
    WHERE request_records.id = sqlc.arg(request_record_id)
      AND request_records.status = 'running'
    FOR KEY SHARE
)
INSERT INTO request_attempts (
    request_record_id,
    permit_id,
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
    gateway_first_token_at,
    final_usage_received,
    usage_mapping_version,
    started_at,
    completed_at,
    provider_origin_revision,
    provider_status_revision,
    channel_config_revision,
    routing_candidate_index,
    upstream_endpoint,
    requested_service_tier
)
SELECT request_guard.guarded_request_record_id,
           sqlc.arg(permit_id),
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
           sqlc.arg(gateway_first_token_at),
           sqlc.arg(final_usage_received),
           sqlc.arg(usage_mapping_version),
           sqlc.arg(started_at),
           sqlc.arg(completed_at),
           sqlc.arg(provider_origin_revision),
           sqlc.arg(provider_status_revision),
           sqlc.arg(channel_config_revision),
           sqlc.arg(routing_candidate_index),
           sqlc.arg(upstream_endpoint),
           sqlc.narg(requested_service_tier)
FROM request_guard
RETURNING request_attempts.*;

-- name: HasRunningRequestAttempt :one
-- HasRunningRequestAttempt 判断请求是否仍有活跃 attempt；孤儿清扫在 request 行锁内使用该事实保护合法长流。
SELECT EXISTS (
    SELECT 1
    FROM request_attempts
    WHERE request_record_id = sqlc.arg(request_record_id)
      AND status = 'running'
)::boolean;

-- name: MarkRequestAttemptGatewayFirstToken :one
-- MarkRequestAttemptGatewayFirstToken 记录一次 attempt 的首次有效生成 Token 客户交付时间；重复调用保留第一次时间。
WITH updated AS (
    UPDATE request_attempts
        SET gateway_first_token_at = COALESCE(request_attempts.gateway_first_token_at, sqlc.arg(gateway_first_token_at))
        WHERE request_attempts.id = sqlc.arg(attempt_id)
          AND request_attempts.gateway_first_token_at IS NULL
        RETURNING request_attempts.*
)
SELECT *
FROM updated

UNION ALL

SELECT request_attempts.*
FROM request_attempts
WHERE request_attempts.id = sqlc.arg(attempt_id)
  AND request_attempts.gateway_first_token_at IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM updated);

-- name: RecordRequestAttemptUpstreamTiming :one
-- RecordRequestAttemptUpstreamTiming first-write-wins 地保存真实 transport/FirstToken/completion 边界，
-- 以及超时失败时的稳定超时阶段（§11.4：response_header|first_token|stream_idle|response_body）。
UPDATE request_attempts
SET upstream_started_at = COALESCE(request_attempts.upstream_started_at, sqlc.narg(upstream_started_at)),
    upstream_first_token_at = COALESCE(request_attempts.upstream_first_token_at, sqlc.narg(upstream_first_token_at)),
    upstream_completed_at = COALESCE(request_attempts.upstream_completed_at, sqlc.narg(upstream_completed_at)),
    upstream_timeout_phase = COALESCE(request_attempts.upstream_timeout_phase, sqlc.narg(upstream_timeout_phase))
WHERE request_attempts.id = sqlc.arg(attempt_id)
RETURNING *;

-- name: RecordRequestAttemptBreakerDisposition :one
-- RecordRequestAttemptBreakerDisposition 保留首次已确认的 Finish disposition，重复终态不得覆盖。
UPDATE request_attempts
SET breaker_provider_disposition = COALESCE(request_attempts.breaker_provider_disposition, sqlc.narg(breaker_provider_disposition)),
    breaker_channel_disposition = COALESCE(request_attempts.breaker_channel_disposition, sqlc.narg(breaker_channel_disposition))
WHERE request_attempts.id = sqlc.arg(attempt_id)
RETURNING *;

-- name: RecordRequestAttemptScoringSample :exec
-- Persist the exact P2 sample classification for Admin audit. OR keeps retries idempotent and
-- prevents a duplicate terminal callback from erasing an already recorded sample fact.
UPDATE request_attempts
SET ttft_scoring_sample = request_attempts.ttft_scoring_sample OR sqlc.arg(ttft_scoring_sample),
    error_scoring_sample = request_attempts.error_scoring_sample OR sqlc.arg(error_scoring_sample),
    error_scoring_failure = request_attempts.error_scoring_failure OR sqlc.arg(error_scoring_failure)
WHERE request_attempts.id = sqlc.arg(attempt_id);

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
            upstream_service_tier = sqlc.narg(upstream_service_tier),
            gateway_first_token_at = COALESCE(request_attempts.gateway_first_token_at, sqlc.narg(gateway_first_token_at)),
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
            upstream_service_tier = sqlc.narg(upstream_service_tier),
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
            upstream_service_tier = sqlc.narg(upstream_service_tier),
            error_code = sqlc.arg(error_code),
            error_message = sqlc.arg(error_message),
            internal_error_detail = sqlc.arg(internal_error_detail),
            gateway_first_token_at = COALESCE(request_attempts.gateway_first_token_at, sqlc.narg(gateway_first_token_at)),
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
            upstream_service_tier = sqlc.narg(upstream_service_tier),
            error_code = sqlc.arg(error_code),
            error_message = sqlc.arg(error_message),
            internal_error_detail = sqlc.arg(internal_error_detail),
            gateway_first_token_at = COALESCE(request_attempts.gateway_first_token_at, sqlc.narg(gateway_first_token_at)),
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
