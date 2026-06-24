-- name: CreateSettlementRecoveryJob :one
-- CreateSettlementRecoveryJob 创建或读取一次请求的 settlement recovery job。
INSERT INTO settlement_recovery_jobs (
    user_id,
    request_record_id,
    attempt_id,
    reservation_id,
    response_protocol,
    response_id,
    response_model_id,
    model_id,
    provider_id,
    channel_id,
    upstream_protocol,
    upstream_response_id,
    upstream_model,
    finish_class,
    upstream_finish_reason,
    upstream_status_code,
    upstream_request_id,
    usage_uncached_input_tokens,
    usage_uncached_input_tokens_state,
    usage_cache_read_input_tokens,
    usage_cache_read_input_tokens_state,
    usage_cache_write_5m_input_tokens,
    usage_cache_write_5m_input_tokens_state,
    usage_cache_write_1h_input_tokens,
    usage_cache_write_1h_input_tokens_state,
    usage_output_tokens_total,
    usage_output_tokens_total_state,
    usage_reasoning_output_tokens,
    usage_reasoning_output_tokens_state,
    usage_server_web_search_requests,
    usage_server_web_fetch_requests,
    usage_source,
    usage_mapping_version,
    price_id,
    currency,
    pricing_unit,
    uncached_input_price,
    cache_read_input_price,
    cache_write_5m_input_price,
    cache_write_1h_input_price,
    output_price,
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
           sqlc.arg(response_protocol),
           sqlc.arg(response_id),
           sqlc.arg(response_model_id),
           sqlc.arg(model_id),
           sqlc.arg(provider_id),
           sqlc.arg(channel_id),
           sqlc.arg(upstream_protocol),
           sqlc.arg(upstream_response_id),
           sqlc.arg(upstream_model),
           sqlc.arg(finish_class),
           sqlc.arg(upstream_finish_reason),
           sqlc.arg(upstream_status_code),
           sqlc.arg(upstream_request_id),
           sqlc.arg(usage_uncached_input_tokens),
           sqlc.arg(usage_uncached_input_tokens_state),
           sqlc.arg(usage_cache_read_input_tokens),
           sqlc.arg(usage_cache_read_input_tokens_state),
           sqlc.arg(usage_cache_write_5m_input_tokens),
           sqlc.arg(usage_cache_write_5m_input_tokens_state),
           sqlc.arg(usage_cache_write_1h_input_tokens),
           sqlc.arg(usage_cache_write_1h_input_tokens_state),
           sqlc.arg(usage_output_tokens_total),
           sqlc.arg(usage_output_tokens_total_state),
           sqlc.arg(usage_reasoning_output_tokens),
           sqlc.arg(usage_reasoning_output_tokens_state),
           sqlc.arg(usage_server_web_search_requests),
           sqlc.arg(usage_server_web_fetch_requests),
           sqlc.arg(usage_source),
           sqlc.arg(usage_mapping_version),
           sqlc.arg(price_id),
           sqlc.arg(currency),
           sqlc.arg(pricing_unit),
           sqlc.arg(uncached_input_price),
           sqlc.arg(cache_read_input_price),
           sqlc.arg(cache_write_5m_input_price),
           sqlc.arg(cache_write_1h_input_price),
           sqlc.arg(output_price),
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
  AND settlement_recovery_jobs.response_protocol = EXCLUDED.response_protocol
  AND settlement_recovery_jobs.response_id = EXCLUDED.response_id
  AND settlement_recovery_jobs.response_model_id = EXCLUDED.response_model_id
  AND settlement_recovery_jobs.model_id = EXCLUDED.model_id
  AND settlement_recovery_jobs.provider_id = EXCLUDED.provider_id
  AND settlement_recovery_jobs.channel_id = EXCLUDED.channel_id
  AND settlement_recovery_jobs.upstream_protocol = EXCLUDED.upstream_protocol
  AND settlement_recovery_jobs.upstream_response_id = EXCLUDED.upstream_response_id
  AND settlement_recovery_jobs.upstream_model = EXCLUDED.upstream_model
  AND settlement_recovery_jobs.finish_class = EXCLUDED.finish_class
  AND settlement_recovery_jobs.upstream_finish_reason = EXCLUDED.upstream_finish_reason
  AND settlement_recovery_jobs.upstream_status_code = EXCLUDED.upstream_status_code
  AND settlement_recovery_jobs.upstream_request_id IS NOT DISTINCT FROM EXCLUDED.upstream_request_id
  AND settlement_recovery_jobs.usage_uncached_input_tokens = EXCLUDED.usage_uncached_input_tokens
  AND settlement_recovery_jobs.usage_uncached_input_tokens_state = EXCLUDED.usage_uncached_input_tokens_state
  AND settlement_recovery_jobs.usage_cache_read_input_tokens = EXCLUDED.usage_cache_read_input_tokens
  AND settlement_recovery_jobs.usage_cache_read_input_tokens_state = EXCLUDED.usage_cache_read_input_tokens_state
  AND settlement_recovery_jobs.usage_cache_write_5m_input_tokens = EXCLUDED.usage_cache_write_5m_input_tokens
  AND settlement_recovery_jobs.usage_cache_write_5m_input_tokens_state = EXCLUDED.usage_cache_write_5m_input_tokens_state
  AND settlement_recovery_jobs.usage_cache_write_1h_input_tokens = EXCLUDED.usage_cache_write_1h_input_tokens
  AND settlement_recovery_jobs.usage_cache_write_1h_input_tokens_state = EXCLUDED.usage_cache_write_1h_input_tokens_state
  AND settlement_recovery_jobs.usage_output_tokens_total = EXCLUDED.usage_output_tokens_total
  AND settlement_recovery_jobs.usage_output_tokens_total_state = EXCLUDED.usage_output_tokens_total_state
  AND settlement_recovery_jobs.usage_reasoning_output_tokens = EXCLUDED.usage_reasoning_output_tokens
  AND settlement_recovery_jobs.usage_reasoning_output_tokens_state = EXCLUDED.usage_reasoning_output_tokens_state
  AND settlement_recovery_jobs.usage_server_web_search_requests = EXCLUDED.usage_server_web_search_requests
  AND settlement_recovery_jobs.usage_server_web_fetch_requests = EXCLUDED.usage_server_web_fetch_requests
  AND settlement_recovery_jobs.usage_source = EXCLUDED.usage_source
  AND settlement_recovery_jobs.usage_mapping_version = EXCLUDED.usage_mapping_version
  AND settlement_recovery_jobs.price_id = EXCLUDED.price_id
  AND settlement_recovery_jobs.currency = EXCLUDED.currency
  AND settlement_recovery_jobs.pricing_unit = EXCLUDED.pricing_unit
  AND settlement_recovery_jobs.uncached_input_price = EXCLUDED.uncached_input_price
  AND settlement_recovery_jobs.cache_read_input_price IS NOT DISTINCT FROM EXCLUDED.cache_read_input_price
  AND settlement_recovery_jobs.cache_write_5m_input_price IS NOT DISTINCT FROM EXCLUDED.cache_write_5m_input_price
  AND settlement_recovery_jobs.cache_write_1h_input_price IS NOT DISTINCT FROM EXCLUDED.cache_write_1h_input_price
  AND settlement_recovery_jobs.output_price = EXCLUDED.output_price
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

-- name: GetDeadSettlementRecoveryJobWithRunningRequest :one
-- GetDeadSettlementRecoveryJobWithRunningRequest 取一条已 dead、但其请求仍停留在 running 的补偿任务，
-- 供 worker 收口：释放冻结余额（记风险敞口）并把请求原子推进到 failed，避免请求永远停在「进行中」。
-- 以「请求仍为 running」为闸门，幂等且可重放（已收口的请求不会再被选中）。
SELECT j.*
FROM settlement_recovery_jobs j
JOIN request_records r ON r.id = j.request_record_id
WHERE j.status = 'dead'
  AND r.status = 'running'
ORDER BY j.id ASC
LIMIT 1;

-- name: GetSettlementRecoveryJobByID :one
-- GetSettlementRecoveryJobByID 按主键读取单条 recovery job 完整事实（含 last_internal_error_detail）。
-- 不加锁，仅供 admin 只读详情端点使用；是否回显内部详情由 service/handler 控制（M8）。
SELECT *
FROM settlement_recovery_jobs
WHERE id = sqlc.arg(id);

-- name: ListSettlementRecoveryJobsPage :many
-- ListSettlementRecoveryJobsPage 按可选过滤分页倒序列出 recovery job（M8 运营任务台，只读）。
-- 安全红线：列表绝不 SELECT last_internal_error_detail（从存储层就脱敏）；金额走十进制字符串。
SELECT
    id,
    user_id,
    request_record_id,
    attempt_id,
    reservation_id,
    response_protocol,
    response_id,
    response_model_id,
    model_id,
    provider_id,
    channel_id,
    upstream_protocol,
    upstream_model,
    finish_class,
    upstream_status_code,
    currency,
    estimated_amount,
    authorized_amount,
    status,
    attempt_count,
    max_attempts,
    next_run_at,
    locked_by,
    locked_until,
    last_error_code,
    last_error_message,
    last_attempted_at,
    completed_at,
    created_at,
    updated_at
FROM settlement_recovery_jobs
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('user_id')::bigint IS NULL OR user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountSettlementRecoveryJobs :one
-- CountSettlementRecoveryJobs 返回与 ListSettlementRecoveryJobsPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM settlement_recovery_jobs
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('user_id')::bigint IS NULL OR user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz);

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
