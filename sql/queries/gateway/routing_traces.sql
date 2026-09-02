-- name: UpsertRoutingDecisionTrace :exec
-- 每个进入路由规划的请求恰好一条 trace：规划开始写 partial，生命周期结束幂等升级为 complete（§13.1）。
-- partial 不得覆盖已有的 complete：进程异常留下的 partial 是有意义的「尚未收口」，
-- 但一条已经收口的 trace 不能被后续 partial 写回退。
INSERT INTO routing_decision_traces (
    request_record_id, mode, requested_model_id, protocol, endpoint,
    pool_size, algorithm_version,
    sticky_key_present, sticky_before_channel_id, sticky_before_version,
    sticky_action, sticky_reason, sticky_after_channel_id, sticky_after_version,
    trace_status, schema_version, eligible_count, baseline_order, actual_scan_order,
    attempted_channel_ids, selected_channel_id, fallback_count, final_result,
    capacity_wait_ms, capacity_wait_result, trace_payload,
    attempted_account_ids, selected_account_id
) VALUES (
    sqlc.arg(request_record_id), sqlc.arg(mode),
    sqlc.arg(requested_model_id), sqlc.arg(protocol), sqlc.arg(endpoint),
    sqlc.arg(pool_size), sqlc.arg(algorithm_version),
    sqlc.arg(sticky_key_present), sqlc.narg(sticky_before_channel_id),
    sqlc.narg(sticky_before_version), sqlc.narg(sticky_action), sqlc.narg(sticky_reason),
    sqlc.narg(sticky_after_channel_id), sqlc.narg(sticky_after_version),
    sqlc.arg(trace_status), sqlc.arg(schema_version), sqlc.arg(eligible_count),
    sqlc.arg(baseline_order), sqlc.arg(actual_scan_order), sqlc.arg(attempted_channel_ids),
    sqlc.narg(selected_channel_id), sqlc.arg(fallback_count), sqlc.narg(final_result),
    sqlc.narg(capacity_wait_ms), sqlc.narg(capacity_wait_result), sqlc.arg(trace_payload),
    sqlc.arg(attempted_account_ids), sqlc.narg(selected_account_id)
)
ON CONFLICT (request_record_id) DO UPDATE SET
    pool_size = EXCLUDED.pool_size,
    sticky_key_present = EXCLUDED.sticky_key_present,
    sticky_before_channel_id = EXCLUDED.sticky_before_channel_id,
    sticky_before_version = EXCLUDED.sticky_before_version,
    sticky_action = EXCLUDED.sticky_action,
    sticky_reason = EXCLUDED.sticky_reason,
    sticky_after_channel_id = EXCLUDED.sticky_after_channel_id,
    sticky_after_version = EXCLUDED.sticky_after_version,
    -- complete 是终态：一旦收口就不再被 partial 覆盖回去。
    trace_status = CASE
        WHEN routing_decision_traces.trace_status = 'complete' THEN 'complete'
        ELSE EXCLUDED.trace_status
    END,
    schema_version = GREATEST(routing_decision_traces.schema_version, EXCLUDED.schema_version),
    eligible_count = EXCLUDED.eligible_count,
    baseline_order = EXCLUDED.baseline_order,
    actual_scan_order = EXCLUDED.actual_scan_order,
    attempted_channel_ids = EXCLUDED.attempted_channel_ids,
    attempted_account_ids = EXCLUDED.attempted_account_ids,
    selected_account_id = COALESCE(EXCLUDED.selected_account_id, routing_decision_traces.selected_account_id),
    selected_channel_id = COALESCE(EXCLUDED.selected_channel_id, routing_decision_traces.selected_channel_id),
    fallback_count = EXCLUDED.fallback_count,
    final_result = COALESCE(EXCLUDED.final_result, routing_decision_traces.final_result),
    capacity_wait_ms = COALESCE(EXCLUDED.capacity_wait_ms, routing_decision_traces.capacity_wait_ms),
    capacity_wait_result = COALESCE(EXCLUDED.capacity_wait_result, routing_decision_traces.capacity_wait_result),
    trace_payload = EXCLUDED.trace_payload,
    updated_at = now();

-- name: ModelRuntimePool :many
-- ModelRuntimePool 返回全部未归档渠道及其数据库硬过滤事实，供选路诊断解释
-- 「这条渠道为什么没进候选」。
--
-- 它故意不做任何过滤：范围就是全部渠道。诊断的价值恰恰在于把被排除的渠道也列出来，
-- 并给出被排除的具体原因（渠道停用 / 凭据无效 / 协议不匹配 / 未绑定该模型 / 缺价格等）。
-- 过滤后的候选集由 FindModelCandidates 负责，两者互为对照。
SELECT
    c.id AS channel_id,
    c.name AS channel_name,
    c.status AS channel_status,
    c.credential_valid,
    (c.credential <> '')::boolean AS has_credential,
    (p.origin <> '')::boolean AS has_origin,
    c.protocols,
    c.adapter_key,
    c.priority,
    c.concurrency_limit,
    c.config_revision AS channel_config_revision,
    c.capacity_revision AS channel_capacity_revision,
    p.origin,
    p.origin_revision AS provider_origin_revision,
    p.status_revision AS provider_status_revision,
    p.id AS provider_id,
    p.name AS provider_name,
    p.status AS provider_status,
    COALESCE(m.id, 0)::bigint AS model_db_id,
    (m.id IS NOT NULL)::boolean AS model_exists,
    COALESCE(m.status, '')::text AS model_status,
    COALESCE(cm.status, '')::text AS binding_status,
    (base.id IS NOT NULL)::boolean AS has_model_price,
    COALESCE((cost.id IS NOT NULL OR mult.id IS NOT NULL), false)::boolean AS has_channel_cost,
    COALESCE(base.id, 0)::bigint AS model_price_id,
    COALESCE(base.currency, '')::text AS base_currency,
    COALESCE(base.pricing_unit, '')::text AS base_pricing_unit,
    base.uncached_input_price,
    base.cache_read_input_price,
    base.cache_creation_5m_input_price,
    base.cache_creation_1h_input_price,
    base.cache_creation_30m_input_price,
    base.output_price,
    base.reasoning_output_price,
    COALESCE(cost.id, 0)::bigint AS channel_price_id,
    COALESCE(cost.currency, '')::text AS cost_currency,
    COALESCE(cost.pricing_unit, '')::text AS cost_pricing_unit,
    cost.uncached_input_cost,
    cost.cache_read_input_cost,
    cost.cache_creation_5m_input_cost,
    cost.cache_creation_1h_input_cost,
    cost.cache_creation_30m_input_cost,
    cost.output_cost,
    cost.reasoning_output_cost,
    COALESCE(mult.id, 0)::bigint AS channel_cost_multiplier_id,
    mult.multiplier AS cost_multiplier,
    COALESCE(recharge.id, 0)::bigint AS provider_recharge_rate_id,
    recharge.rate AS provider_recharge_rate
FROM channels c
JOIN providers p ON p.id = c.provider_id
LEFT JOIN models m
  ON NULLIF(sqlc.arg(model_id)::text, '') IS NOT NULL
 AND m.model_id = sqlc.arg(model_id)::text
LEFT JOIN channel_models cm ON cm.channel_id = c.id AND cm.model_id = m.id
LEFT JOIN LATERAL (
    SELECT mp.id, mp.currency, mp.pricing_unit,
           mp.uncached_input_price, mp.cache_read_input_price,
           mp.cache_creation_5m_input_price, mp.cache_creation_1h_input_price,
           mp.cache_creation_30m_input_price, mp.output_price, mp.reasoning_output_price
    FROM model_prices mp
    WHERE mp.model_id = m.id
      AND mp.status = 'enabled'
      AND mp.effective_from <= sqlc.arg(at_time)
      AND (mp.effective_to IS NULL OR mp.effective_to > sqlc.arg(at_time))
    ORDER BY mp.effective_from DESC, mp.id DESC
    LIMIT 1
) base ON TRUE
LEFT JOIN LATERAL (
    SELECT cp.id, cp.currency, cp.pricing_unit,
           cp.uncached_input_cost, cp.cache_read_input_cost,
           cp.cache_creation_5m_input_cost, cp.cache_creation_1h_input_cost,
           cp.cache_creation_30m_input_cost, cp.output_cost, cp.reasoning_output_cost
    FROM channel_prices cp
    WHERE cp.channel_id = c.id
      AND cp.model_id = m.id
      AND cp.status = 'enabled'
      AND cp.effective_from <= sqlc.arg(at_time)
      AND (cp.effective_to IS NULL OR cp.effective_to > sqlc.arg(at_time))
    ORDER BY cp.effective_from DESC, cp.id DESC
    LIMIT 1
) cost ON TRUE
LEFT JOIN LATERAL (
    SELECT ccm.id, ccm.multiplier
    FROM channel_cost_multipliers ccm
    WHERE ccm.channel_id = c.id
      AND (ccm.model_id = m.id OR ccm.model_id IS NULL)
      AND ccm.status = 'enabled'
      AND ccm.effective_from <= sqlc.arg(at_time)
      AND (ccm.effective_to IS NULL OR ccm.effective_to > sqlc.arg(at_time))
    ORDER BY (ccm.model_id IS NULL) ASC, ccm.effective_from DESC, ccm.id DESC
    LIMIT 1
) mult ON TRUE
LEFT JOIN LATERAL (
    SELECT prr.id, prr.rate
    FROM provider_recharge_rates prr
    WHERE prr.provider_id = p.id
      AND prr.status = 'enabled'
      AND prr.effective_from <= sqlc.arg(at_time)
      AND (prr.effective_to IS NULL OR prr.effective_to > sqlc.arg(at_time))
    ORDER BY prr.effective_from DESC, prr.id DESC
    LIMIT 1
) recharge ON TRUE
WHERE c.status <> 'archived'
ORDER BY c.priority, c.id;
