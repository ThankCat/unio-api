-- name: CreateSettlementRecoveryJob :one
-- CreateSettlementRecoveryJob 创建或读取一次请求的 settlement recovery job。
-- 先锁定仍为 running 的 request，使创建任务与孤儿清扫 finalizer 共享同一行锁；若清扫已先收口请求，
-- 本语句等待后重新判断 status 并返回 no rows，不允许在余额释放后补插 recovery job。
WITH locked_request AS (
    SELECT id
    FROM request_records
    WHERE id = sqlc.arg(request_record_id)
      AND status = 'running'
    FOR UPDATE
)
INSERT INTO settlement_recovery_jobs (
    user_id,
    request_record_id,
    attempt_id,
    reservation_id,
    response_protocol,
    response_id,
    response_model_id,
    request_final_status,
    attempt_final_status,
    settlement_error_code,
    settlement_error_message,
    settlement_internal_error_detail,
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
    usage_cache_write_30m_input_tokens,
    usage_cache_write_30m_input_tokens_state,
    usage_output_tokens_total,
    usage_output_tokens_total_state,
    usage_reasoning_output_tokens,
    usage_reasoning_output_tokens_state,
    usage_server_web_search_requests,
    usage_server_web_fetch_requests,
    usage_source,
    usage_mapping_version,
    price_id,
    cost_base_model_price_id,
    channel_cost_multiplier_id,
    channel_recharge_factor_id,
    currency,
    pricing_unit,
    uncached_input_price,
    cache_read_input_price,
    cache_write_5m_input_price,
    cache_write_1h_input_price,
    cache_write_30m_input_price,
    output_price,
    reasoning_output_price,
    formula_version,
    price_ratio,
    long_context_enabled,
    long_context_threshold,
    long_context_input_multiplier,
    long_context_output_multiplier,
    requested_service_tier,
    actual_service_tier,
    settled_service_tier,
    upstream_service_tier,
    service_tier_resolution,
    model_price_service_tier_id,
    channel_price_service_tier_id,
    estimated_amount,
    authorized_amount,
    max_attempts,
    status,
    next_run_at
)
SELECT
           sqlc.arg(user_id),
           sqlc.arg(request_record_id),
           sqlc.arg(attempt_id),
           sqlc.arg(reservation_id),
           sqlc.arg(response_protocol),
           sqlc.arg(response_id),
           sqlc.arg(response_model_id),
           sqlc.arg(request_final_status),
           sqlc.arg(attempt_final_status),
           sqlc.arg(settlement_error_code),
           sqlc.arg(settlement_error_message),
           sqlc.arg(settlement_internal_error_detail),
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
           sqlc.arg(usage_cache_write_30m_input_tokens),
           sqlc.arg(usage_cache_write_30m_input_tokens_state),
           sqlc.arg(usage_output_tokens_total),
           sqlc.arg(usage_output_tokens_total_state),
           sqlc.arg(usage_reasoning_output_tokens),
           sqlc.arg(usage_reasoning_output_tokens_state),
           sqlc.arg(usage_server_web_search_requests),
           sqlc.arg(usage_server_web_fetch_requests),
           sqlc.arg(usage_source),
           sqlc.arg(usage_mapping_version),
           sqlc.narg(price_id),
           sqlc.narg(cost_base_model_price_id),
           sqlc.narg(channel_cost_multiplier_id),
           sqlc.narg(channel_recharge_factor_id),
           sqlc.arg(currency),
           sqlc.arg(pricing_unit),
           sqlc.arg(uncached_input_price),
           sqlc.arg(cache_read_input_price),
           sqlc.arg(cache_write_5m_input_price),
           sqlc.arg(cache_write_1h_input_price),
           sqlc.arg(cache_write_30m_input_price),
           sqlc.arg(output_price),
           sqlc.arg(reasoning_output_price),
           sqlc.arg(formula_version),
           sqlc.arg(price_ratio),
           sqlc.arg(long_context_enabled),
           sqlc.narg(long_context_threshold),
           sqlc.narg(long_context_input_multiplier),
           sqlc.narg(long_context_output_multiplier),
           sqlc.narg(requested_service_tier),
           sqlc.narg(actual_service_tier),
           sqlc.narg(settled_service_tier),
           sqlc.narg(upstream_service_tier),
           sqlc.narg(service_tier_resolution),
           sqlc.narg(model_price_service_tier_id),
           sqlc.narg(channel_price_service_tier_id),
           sqlc.arg(estimated_amount),
           sqlc.arg(authorized_amount),
           sqlc.arg(max_attempts),
           'pending',
           sqlc.arg(next_run_at)
FROM locked_request
ON CONFLICT (request_record_id) DO UPDATE
SET updated_at = settlement_recovery_jobs.updated_at
WHERE settlement_recovery_jobs.user_id = EXCLUDED.user_id
  AND settlement_recovery_jobs.attempt_id = EXCLUDED.attempt_id
  AND settlement_recovery_jobs.reservation_id = EXCLUDED.reservation_id
  AND settlement_recovery_jobs.response_protocol = EXCLUDED.response_protocol
  AND settlement_recovery_jobs.response_id = EXCLUDED.response_id
  AND settlement_recovery_jobs.response_model_id = EXCLUDED.response_model_id
  AND settlement_recovery_jobs.request_final_status = EXCLUDED.request_final_status
  AND settlement_recovery_jobs.attempt_final_status = EXCLUDED.attempt_final_status
  AND settlement_recovery_jobs.settlement_error_code = EXCLUDED.settlement_error_code
  AND settlement_recovery_jobs.settlement_error_message = EXCLUDED.settlement_error_message
  AND settlement_recovery_jobs.settlement_internal_error_detail = EXCLUDED.settlement_internal_error_detail
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
  AND settlement_recovery_jobs.usage_cache_write_30m_input_tokens = EXCLUDED.usage_cache_write_30m_input_tokens
  AND settlement_recovery_jobs.usage_cache_write_30m_input_tokens_state = EXCLUDED.usage_cache_write_30m_input_tokens_state
  AND settlement_recovery_jobs.usage_output_tokens_total = EXCLUDED.usage_output_tokens_total
  AND settlement_recovery_jobs.usage_output_tokens_total_state = EXCLUDED.usage_output_tokens_total_state
  AND settlement_recovery_jobs.usage_reasoning_output_tokens = EXCLUDED.usage_reasoning_output_tokens
  AND settlement_recovery_jobs.usage_reasoning_output_tokens_state = EXCLUDED.usage_reasoning_output_tokens_state
  AND settlement_recovery_jobs.usage_server_web_search_requests = EXCLUDED.usage_server_web_search_requests
  AND settlement_recovery_jobs.usage_server_web_fetch_requests = EXCLUDED.usage_server_web_fetch_requests
  AND settlement_recovery_jobs.usage_source = EXCLUDED.usage_source
  AND settlement_recovery_jobs.usage_mapping_version = EXCLUDED.usage_mapping_version
  AND settlement_recovery_jobs.price_id IS NOT DISTINCT FROM EXCLUDED.price_id
  AND settlement_recovery_jobs.cost_base_model_price_id IS NOT DISTINCT FROM EXCLUDED.cost_base_model_price_id
  AND settlement_recovery_jobs.channel_cost_multiplier_id IS NOT DISTINCT FROM EXCLUDED.channel_cost_multiplier_id
  AND settlement_recovery_jobs.channel_recharge_factor_id IS NOT DISTINCT FROM EXCLUDED.channel_recharge_factor_id
  AND settlement_recovery_jobs.currency = EXCLUDED.currency
  AND settlement_recovery_jobs.pricing_unit = EXCLUDED.pricing_unit
  AND settlement_recovery_jobs.uncached_input_price = EXCLUDED.uncached_input_price
  AND settlement_recovery_jobs.cache_read_input_price IS NOT DISTINCT FROM EXCLUDED.cache_read_input_price
  AND settlement_recovery_jobs.cache_write_5m_input_price IS NOT DISTINCT FROM EXCLUDED.cache_write_5m_input_price
  AND settlement_recovery_jobs.cache_write_1h_input_price IS NOT DISTINCT FROM EXCLUDED.cache_write_1h_input_price
  AND settlement_recovery_jobs.cache_write_30m_input_price IS NOT DISTINCT FROM EXCLUDED.cache_write_30m_input_price
  AND settlement_recovery_jobs.output_price = EXCLUDED.output_price
  AND settlement_recovery_jobs.reasoning_output_price IS NOT DISTINCT FROM EXCLUDED.reasoning_output_price
  AND settlement_recovery_jobs.formula_version = EXCLUDED.formula_version
  AND settlement_recovery_jobs.price_ratio IS NOT DISTINCT FROM EXCLUDED.price_ratio
  AND settlement_recovery_jobs.long_context_enabled = EXCLUDED.long_context_enabled
  AND settlement_recovery_jobs.long_context_threshold IS NOT DISTINCT FROM EXCLUDED.long_context_threshold
  AND settlement_recovery_jobs.long_context_input_multiplier IS NOT DISTINCT FROM EXCLUDED.long_context_input_multiplier
  AND settlement_recovery_jobs.long_context_output_multiplier IS NOT DISTINCT FROM EXCLUDED.long_context_output_multiplier
  AND settlement_recovery_jobs.requested_service_tier IS NOT DISTINCT FROM EXCLUDED.requested_service_tier
  AND settlement_recovery_jobs.actual_service_tier IS NOT DISTINCT FROM EXCLUDED.actual_service_tier
  AND settlement_recovery_jobs.settled_service_tier IS NOT DISTINCT FROM EXCLUDED.settled_service_tier
  AND settlement_recovery_jobs.upstream_service_tier IS NOT DISTINCT FROM EXCLUDED.upstream_service_tier
  AND settlement_recovery_jobs.service_tier_resolution IS NOT DISTINCT FROM EXCLUDED.service_tier_resolution
  AND settlement_recovery_jobs.model_price_service_tier_id IS NOT DISTINCT FROM EXCLUDED.model_price_service_tier_id
  AND settlement_recovery_jobs.channel_price_service_tier_id IS NOT DISTINCT FROM EXCLUDED.channel_price_service_tier_id
  AND settlement_recovery_jobs.estimated_amount = EXCLUDED.estimated_amount
  AND settlement_recovery_jobs.authorized_amount = EXCLUDED.authorized_amount
RETURNING *;
