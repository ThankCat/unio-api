-- name: GetRoutingDecisionTraceByRequestID :one
SELECT
    t.id,
    t.request_record_id,
    t.route_id,
    t.mode,
    t.requested_model_id,
    t.protocol,
    t.endpoint,
    t.trace_status,
    t.schema_version,
    t.algorithm_version,
    t.pool_size,
    t.eligible_count,
    t.baseline_order,
    t.actual_scan_order,
    t.attempted_channel_ids,
    t.selected_channel_id,
    t.fallback_count,
    t.final_result,
    t.sticky_key_present,
    t.sticky_before_channel_id,
    t.sticky_before_version,
    t.sticky_action,
    t.sticky_reason,
    t.sticky_after_channel_id,
    t.sticky_after_version,
    t.capacity_wait_ms,
    t.capacity_wait_result,
    t.trace_payload,
    t.created_at,
    t.updated_at,
    r.request_id,
    r.status AS request_status,
    r.final_channel_id
FROM routing_decision_traces t
JOIN request_records r ON r.id = t.request_record_id
WHERE r.request_id = sqlc.arg(request_id)
LIMIT 1;
