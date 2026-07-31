-- name: UpsertRoutingDecisionTrace :exec
-- 每个进入路由规划的请求恰好一条 trace：规划开始写 partial，生命周期结束幂等升级为 complete（§13.1）。
-- partial 不得覆盖已有的 complete：进程异常留下的 partial 是有意义的「尚未收口」，
-- 但一条已经收口的 trace 不能被后续 partial 写回退。
INSERT INTO routing_decision_traces (
    request_record_id, route_id, mode, requested_model_id, protocol, endpoint,
    pool_size, algorithm_version,
    sticky_key_present, sticky_before_channel_id, sticky_before_version,
    sticky_action, sticky_reason, sticky_after_channel_id, sticky_after_version,
    trace_status, schema_version, eligible_count, baseline_order, actual_scan_order,
    attempted_channel_ids, selected_channel_id, fallback_count, final_result,
    capacity_wait_ms, capacity_wait_result, trace_payload
) VALUES (
    sqlc.arg(request_record_id), sqlc.arg(route_id), sqlc.arg(mode),
    sqlc.arg(requested_model_id), sqlc.arg(protocol), sqlc.arg(endpoint),
    sqlc.arg(pool_size), sqlc.arg(algorithm_version),
    sqlc.arg(sticky_key_present), sqlc.narg(sticky_before_channel_id),
    sqlc.narg(sticky_before_version), sqlc.narg(sticky_action), sqlc.narg(sticky_reason),
    sqlc.narg(sticky_after_channel_id), sqlc.narg(sticky_after_version),
    sqlc.arg(trace_status), sqlc.arg(schema_version), sqlc.arg(eligible_count),
    sqlc.arg(baseline_order), sqlc.arg(actual_scan_order), sqlc.arg(attempted_channel_ids),
    sqlc.narg(selected_channel_id), sqlc.arg(fallback_count), sqlc.narg(final_result),
    sqlc.narg(capacity_wait_ms), sqlc.narg(capacity_wait_result), sqlc.arg(trace_payload)
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
    selected_channel_id = COALESCE(EXCLUDED.selected_channel_id, routing_decision_traces.selected_channel_id),
    fallback_count = EXCLUDED.fallback_count,
    final_result = COALESCE(EXCLUDED.final_result, routing_decision_traces.final_result),
    capacity_wait_ms = COALESCE(EXCLUDED.capacity_wait_ms, routing_decision_traces.capacity_wait_ms),
    capacity_wait_result = COALESCE(EXCLUDED.capacity_wait_result, routing_decision_traces.capacity_wait_result),
    trace_payload = EXCLUDED.trace_payload,
    updated_at = now();
