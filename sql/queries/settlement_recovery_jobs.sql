-- name: CreateSettlementRecoveryJob :one
-- CreateSettlementRecoveryJob 创建或读取一次请求的 settlement recovery job。
INSERT INTO settlement_recovery_jobs (
    user_id,
    request_record_id,
    attempt_id,
    reservation_id,
    response_model_id,
    model_id,
    provider_id,
    channel_id,
    upstream_response_model,
    upstream_status_code,
    upstream_request_id,
    usage_prompt_tokens,
    usage_completion_tokens,
    usage_total_tokens,
    usage_cached_tokens,
    usage_reasoning_tokens,
    usage_source,
    price_id,
    currency,
    pricing_unit,
    input_price,
    output_price,
    cached_input_price,
    reasoning_output_price,
    formula_version,
    estimated_amount,
    authorized_amount,
    status,
    next_run_at
)
VALUES (
           sqlc.arg(user_id),
           sqlc.arg(request_record_id),
           sqlc.arg(attempt_id),
           sqlc.arg(reservation_id),
           sqlc.arg(response_model_id),
           sqlc.arg(model_id),
           sqlc.arg(provider_id),
           sqlc.arg(channel_id),
           sqlc.arg(upstream_response_model),
           sqlc.arg(upstream_status_code),
           sqlc.arg(upstream_request_id),
           sqlc.arg(usage_prompt_tokens),
           sqlc.arg(usage_completion_tokens),
           sqlc.arg(usage_total_tokens),
           sqlc.arg(usage_cached_tokens),
           sqlc.arg(usage_reasoning_tokens),
           sqlc.arg(usage_source),
           sqlc.arg(price_id),
           sqlc.arg(currency),
           sqlc.arg(pricing_unit),
           sqlc.arg(input_price),
           sqlc.arg(output_price),
           sqlc.arg(cached_input_price),
           sqlc.arg(reasoning_output_price),
           sqlc.arg(formula_version),
           sqlc.arg(estimated_amount),
           sqlc.arg(authorized_amount),
           'pending',
           sqlc.arg(next_run_at)
       )
ON CONFLICT (request_record_id) DO UPDATE
SET updated_at = settlement_recovery_jobs.updated_at
WHERE settlement_recovery_jobs.user_id = EXCLUDED.user_id
  AND settlement_recovery_jobs.attempt_id = EXCLUDED.attempt_id
  AND settlement_recovery_jobs.reservation_id = EXCLUDED.reservation_id
  AND settlement_recovery_jobs.response_model_id = EXCLUDED.response_model_id
  AND settlement_recovery_jobs.model_id = EXCLUDED.model_id
  AND settlement_recovery_jobs.provider_id = EXCLUDED.provider_id
  AND settlement_recovery_jobs.channel_id = EXCLUDED.channel_id
  AND settlement_recovery_jobs.upstream_response_model = EXCLUDED.upstream_response_model
  AND settlement_recovery_jobs.upstream_status_code = EXCLUDED.upstream_status_code
  AND settlement_recovery_jobs.upstream_request_id IS NOT DISTINCT FROM EXCLUDED.upstream_request_id
  AND settlement_recovery_jobs.usage_prompt_tokens = EXCLUDED.usage_prompt_tokens
  AND settlement_recovery_jobs.usage_completion_tokens = EXCLUDED.usage_completion_tokens
  AND settlement_recovery_jobs.usage_total_tokens = EXCLUDED.usage_total_tokens
  AND settlement_recovery_jobs.usage_cached_tokens = EXCLUDED.usage_cached_tokens
  AND settlement_recovery_jobs.usage_reasoning_tokens = EXCLUDED.usage_reasoning_tokens
  AND settlement_recovery_jobs.usage_source = EXCLUDED.usage_source
  AND settlement_recovery_jobs.price_id = EXCLUDED.price_id
  AND settlement_recovery_jobs.currency = EXCLUDED.currency
  AND settlement_recovery_jobs.pricing_unit = EXCLUDED.pricing_unit
  AND settlement_recovery_jobs.input_price = EXCLUDED.input_price
  AND settlement_recovery_jobs.output_price = EXCLUDED.output_price
  AND settlement_recovery_jobs.cached_input_price IS NOT DISTINCT FROM EXCLUDED.cached_input_price
  AND settlement_recovery_jobs.reasoning_output_price IS NOT DISTINCT FROM EXCLUDED.reasoning_output_price
  AND settlement_recovery_jobs.formula_version = EXCLUDED.formula_version
  AND settlement_recovery_jobs.estimated_amount = EXCLUDED.estimated_amount
  AND settlement_recovery_jobs.authorized_amount = EXCLUDED.authorized_amount
RETURNING *;

-- name: GetSettlementRecoveryJobByRequest :one
-- GetSettlementRecoveryJobByRequest 按请求 ID 读取 settlement recovery job。
SELECT *
FROM settlement_recovery_jobs
WHERE request_record_id = sqlc.arg(request_record_id);

-- name: ClaimNextSettlementRecoveryJob :one
-- ClaimNextSettlementRecoveryJob claim 一条到期 pending 或锁过期 running 的 recovery job。
WITH candidate AS (
    SELECT id
    FROM settlement_recovery_jobs
    WHERE settlement_recovery_jobs.attempt_count < settlement_recovery_jobs.max_attempts
      AND (
          (
              settlement_recovery_jobs.status = 'pending'
                  AND settlement_recovery_jobs.next_run_at <= sqlc.arg(now_at)
              )
              OR
          (
              settlement_recovery_jobs.status = 'running'
                  AND settlement_recovery_jobs.locked_until <= sqlc.arg(now_at)
              )
      )
    ORDER BY settlement_recovery_jobs.next_run_at ASC, settlement_recovery_jobs.id ASC
        FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE settlement_recovery_jobs
SET
    status = 'running',
    attempt_count = attempt_count + 1,
    locked_by = sqlc.arg(locked_by),
    locked_until = sqlc.arg(locked_until),
    last_attempted_at = sqlc.arg(now_at),
    updated_at = sqlc.arg(now_at)
WHERE settlement_recovery_jobs.id = (SELECT candidate.id FROM candidate)
RETURNING *;

-- name: MarkSettlementRecoveryJobSucceeded :one
-- MarkSettlementRecoveryJobSucceeded 将 pending/running recovery job 标记为 succeeded。
WITH updated AS (
    UPDATE settlement_recovery_jobs
    SET
        status = 'succeeded',
        locked_by = NULL,
        locked_until = NULL,
        completed_at = sqlc.arg(completed_at),
        updated_at = sqlc.arg(completed_at)
    WHERE settlement_recovery_jobs.id = sqlc.arg(id)
      AND settlement_recovery_jobs.status IN ('pending', 'running')
    RETURNING *
)
SELECT *
FROM updated

UNION ALL

SELECT settlement_recovery_jobs.*
FROM settlement_recovery_jobs
WHERE settlement_recovery_jobs.id = sqlc.arg(id)
  AND settlement_recovery_jobs.status = 'succeeded'
  AND NOT EXISTS (SELECT 1 FROM updated);

-- name: MarkSettlementRecoveryJobRetry :one
-- MarkSettlementRecoveryJobRetry 将 running recovery job 退回 pending 并设置下次重试时间。
UPDATE settlement_recovery_jobs
SET
    status = 'pending',
    locked_by = NULL,
    locked_until = NULL,
    next_run_at = sqlc.arg(next_run_at),
    last_error_code = sqlc.arg(last_error_code),
    last_error_message = sqlc.arg(last_error_message),
    last_internal_error_detail = sqlc.arg(last_internal_error_detail),
    updated_at = sqlc.arg(updated_at)
WHERE settlement_recovery_jobs.id = sqlc.arg(id)
  AND settlement_recovery_jobs.status = 'running'
  AND settlement_recovery_jobs.locked_by = sqlc.arg(locked_by)
  AND settlement_recovery_jobs.locked_until = sqlc.arg(locked_until)
  AND settlement_recovery_jobs.attempt_count = sqlc.arg(attempt_count)
  AND settlement_recovery_jobs.attempt_count < settlement_recovery_jobs.max_attempts
RETURNING *;

-- name: MarkSettlementRecoveryJobDead :one
-- MarkSettlementRecoveryJobDead 将 running recovery job 标记为 dead，等待后台人工处理。
UPDATE settlement_recovery_jobs
SET
    status = 'dead',
    locked_by = NULL,
    locked_until = NULL,
    last_error_code = sqlc.arg(last_error_code),
    last_error_message = sqlc.arg(last_error_message),
    last_internal_error_detail = sqlc.arg(last_internal_error_detail),
    completed_at = sqlc.arg(completed_at),
    updated_at = sqlc.arg(completed_at)
WHERE settlement_recovery_jobs.id = sqlc.arg(id)
  AND settlement_recovery_jobs.status = 'running'
  AND settlement_recovery_jobs.locked_by = sqlc.arg(locked_by)
  AND settlement_recovery_jobs.locked_until = sqlc.arg(locked_until)
  AND settlement_recovery_jobs.attempt_count = sqlc.arg(attempt_count)
RETURNING *;

-- name: MarkExhaustedSettlementRecoveryJobDead :one
-- MarkExhaustedSettlementRecoveryJobDead 将到期且已耗尽自动尝试次数的 recovery job 标记为 dead。
WITH candidate AS (
    SELECT id
    FROM settlement_recovery_jobs
    WHERE settlement_recovery_jobs.attempt_count >= settlement_recovery_jobs.max_attempts
      AND (
          (
              settlement_recovery_jobs.status = 'pending'
                  AND settlement_recovery_jobs.next_run_at <= sqlc.arg(now_at)
              )
              OR
          (
              settlement_recovery_jobs.status = 'running'
                  AND settlement_recovery_jobs.locked_until <= sqlc.arg(now_at)
              )
      )
    ORDER BY settlement_recovery_jobs.next_run_at ASC, settlement_recovery_jobs.id ASC
        FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE settlement_recovery_jobs
SET
    status = 'dead',
    locked_by = NULL,
    locked_until = NULL,
    last_error_code = sqlc.arg(last_error_code),
    last_error_message = sqlc.arg(last_error_message),
    last_internal_error_detail = sqlc.arg(last_internal_error_detail),
    completed_at = sqlc.arg(completed_at),
    updated_at = sqlc.arg(completed_at)
WHERE settlement_recovery_jobs.id = (SELECT candidate.id FROM candidate)
RETURNING *;
