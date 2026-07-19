-- name: UpsertRoutingDecisionTrace :exec
INSERT INTO routing_decision_traces (
    request_record_id, route_id, mode, requested_model_id, protocol, operation,
    pool_size, candidate_count, sticky_channel_id, sticky_pinned, sticky_invalid,
    capacity_degraded, all_capacity_zero, margin_guard_triggered, abnormal,
    abnormal_reasons, candidate_scores, selected_order, fallback_chain,
    algorithm_version, sampled
) VALUES (
    sqlc.arg(request_record_id), sqlc.arg(route_id), sqlc.arg(mode),
    sqlc.arg(requested_model_id), sqlc.arg(protocol), sqlc.arg(operation),
    sqlc.arg(pool_size), sqlc.arg(candidate_count), sqlc.narg(sticky_channel_id),
    sqlc.arg(sticky_pinned), sqlc.arg(sticky_invalid), sqlc.arg(capacity_degraded),
    sqlc.arg(all_capacity_zero), sqlc.arg(margin_guard_triggered), sqlc.arg(abnormal),
    sqlc.arg(abnormal_reasons), sqlc.arg(candidate_scores), sqlc.arg(selected_order),
    sqlc.arg(fallback_chain), sqlc.arg(algorithm_version), sqlc.arg(sampled)
)
ON CONFLICT (request_record_id) DO UPDATE SET
    pool_size = EXCLUDED.pool_size,
    candidate_count = EXCLUDED.candidate_count,
    sticky_channel_id = EXCLUDED.sticky_channel_id,
    sticky_pinned = EXCLUDED.sticky_pinned,
    sticky_invalid = EXCLUDED.sticky_invalid,
    capacity_degraded = EXCLUDED.capacity_degraded,
    all_capacity_zero = EXCLUDED.all_capacity_zero,
    margin_guard_triggered = EXCLUDED.margin_guard_triggered,
    abnormal = routing_decision_traces.abnormal OR EXCLUDED.abnormal,
    abnormal_reasons = ARRAY(SELECT DISTINCT unnest(routing_decision_traces.abnormal_reasons || EXCLUDED.abnormal_reasons)),
    candidate_scores = EXCLUDED.candidate_scores,
    selected_order = EXCLUDED.selected_order,
    fallback_chain = EXCLUDED.fallback_chain,
    sampled = routing_decision_traces.sampled OR EXCLUDED.sampled,
    updated_at = now();

-- name: DeleteExpiredRoutingDecisionTraces :execrows
DELETE FROM routing_decision_traces target
WHERE target.created_at < sqlc.arg(cutoff)
  AND target.id IN (
      SELECT expired.id FROM routing_decision_traces expired
      WHERE expired.created_at < sqlc.arg(cutoff)
      ORDER BY expired.created_at
      LIMIT sqlc.arg(batch_limit)
      FOR UPDATE SKIP LOCKED
  );
