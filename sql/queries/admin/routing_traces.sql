-- name: ListRouteRoutingDecisionTraces :many
SELECT t.*, r.request_id, r.status AS request_status, r.final_channel_id
FROM routing_decision_traces t
JOIN request_records r ON r.id = t.request_record_id
WHERE t.route_id = sqlc.arg(route_id)
ORDER BY t.created_at DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountRouteRoutingDecisionTraces :one
SELECT COUNT(*) FROM routing_decision_traces WHERE route_id = sqlc.arg(route_id);

-- name: GetRoutingDecisionTraceByRequestID :one
SELECT t.*, r.request_id, r.status AS request_status, r.final_channel_id
FROM routing_decision_traces t
JOIN request_records r ON r.id = t.request_record_id
WHERE r.request_id = sqlc.arg(request_id)
LIMIT 1;
