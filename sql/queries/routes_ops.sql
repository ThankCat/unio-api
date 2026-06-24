-- §3.5 线路路由作战台只读运维聚合。
-- 归因（就近绑定 §3.1）：每条请求归属 COALESCE(api_keys.route_id, projects.default_route_id)。
-- NULL（无 Key 线路且无项目默认）= 内置桶，不计入任何具体线路。request 粒度。
-- fallback：同 request 有 >1 次 attempt 且最终成功；no_channel：error_code 命中无可用渠道码。

-- name: RoutesOpsCounts :one
SELECT
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE status = 'enabled') AS enabled,
    COUNT(*) FILTER (WHERE status = 'disabled') AS disabled,
    COUNT(*) FILTER (WHERE is_builtin) AS builtin
FROM routes;

-- name: RoutesOpsAttributeAggregate :one
-- RoutesOpsAttributeAggregate 在区间内对「已归因到某线路」的请求做整体聚合（供概览卡）。
WITH attributed AS (
    SELECT
        COALESCE(ak.route_id, p.default_route_id) AS route_id,
        r.status,
        r.error_code,
        r.started_at,
        r.completed_at,
        (SELECT COUNT(*) FROM request_attempts a WHERE a.request_record_id = r.id) AS attempt_count
    FROM request_records r
    JOIN api_keys ak ON ak.id = r.api_key_id
    JOIN projects p ON p.id = r.project_id
    WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
)
SELECT
    COUNT(*) FILTER (WHERE route_id IS NOT NULL AND status IN ('succeeded', 'failed', 'canceled')) AS terminal_total,
    COUNT(*) FILTER (WHERE route_id IS NOT NULL AND status = 'succeeded') AS succeeded_total,
    COUNT(*) FILTER (WHERE route_id IS NOT NULL AND status = 'succeeded' AND attempt_count > 1) AS fallback_total,
    COUNT(*) FILTER (WHERE route_id IS NOT NULL AND error_code IN ('no_available_channel', 'routing_no_available_channel')) AS no_channel_total,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN route_id IS NOT NULL AND status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95
FROM attributed;

-- name: RoutesOpsTable :many
-- RoutesOpsTable 线路运维主表（分页）：归因请求指标 + fallback + 无可用渠道 + 绑定数。
WITH attributed AS (
    SELECT
        COALESCE(ak.route_id, p.default_route_id) AS route_id,
        r.status,
        r.error_code,
        r.started_at,
        r.completed_at,
        (SELECT COUNT(*) FROM request_attempts a WHERE a.request_record_id = r.id) AS attempt_count
    FROM request_records r
    JOIN api_keys ak ON ak.id = r.api_key_id
    JOIN projects p ON p.id = r.project_id
    WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
)
SELECT
    rt.id,
    rt.name,
    rt.mode,
    rt.pool_kind,
    rt.is_builtin,
    rt.status,
    rt.description,
    COUNT(ar.route_id) FILTER (WHERE ar.status IN ('succeeded', 'failed', 'canceled')) AS request_total,
    COUNT(ar.route_id) FILTER (WHERE ar.status = 'succeeded') AS request_succeeded,
    COUNT(ar.route_id) FILTER (WHERE ar.status = 'succeeded' AND ar.attempt_count > 1) AS fallback_total,
    COUNT(ar.route_id) FILTER (WHERE ar.error_code IN ('no_available_channel', 'routing_no_available_channel')) AS no_channel_total,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN ar.status = 'succeeded' AND ar.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (ar.completed_at - ar.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    (SELECT COUNT(*) FROM projects pp WHERE pp.default_route_id = rt.id) AS bound_projects,
    (SELECT COUNT(*) FROM api_keys kk WHERE kk.route_id = rt.id) AS bound_keys,
    (SELECT COUNT(*) FROM route_channels rc WHERE rc.route_id = rt.id) AS pool_channels
FROM routes rt
LEFT JOIN attributed ar ON ar.route_id = rt.id
WHERE (sqlc.narg('status')::text IS NULL OR rt.status = sqlc.narg('status')::text)
  AND (sqlc.narg('search')::text IS NULL OR rt.name ILIKE '%' || sqlc.narg('search')::text || '%')
GROUP BY rt.id, rt.name, rt.mode, rt.pool_kind, rt.is_builtin, rt.status, rt.description
ORDER BY
    (COUNT(ar.route_id) FILTER (WHERE ar.status = 'succeeded')::float8 / NULLIF(COUNT(ar.route_id) FILTER (WHERE ar.status IN ('succeeded','failed','canceled')), 0)) ASC NULLS LAST,
    COUNT(ar.route_id) DESC,
    rt.id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: RoutesOpsTableCount :one
SELECT COUNT(*) AS total
FROM routes rt
WHERE (sqlc.narg('status')::text IS NULL OR rt.status = sqlc.narg('status')::text)
  AND (sqlc.narg('search')::text IS NULL OR rt.name ILIKE '%' || sqlc.narg('search')::text || '%');

-- name: RouteOpsDetail :one
-- RouteOpsDetail 单线路抽屉概览：归因请求指标 + fallback + 无可用渠道。
WITH attributed AS (
    SELECT
        r.status,
        r.error_code,
        r.started_at,
        r.completed_at,
        (SELECT COUNT(*) FROM request_attempts a WHERE a.request_record_id = r.id) AS attempt_count
    FROM request_records r
    JOIN api_keys ak ON ak.id = r.api_key_id
    JOIN projects p ON p.id = r.project_id
    WHERE COALESCE(ak.route_id, p.default_route_id) = sqlc.arg('route_id')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
)
SELECT
    COUNT(*) FILTER (WHERE status IN ('succeeded', 'failed', 'canceled')) AS request_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS request_succeeded,
    COUNT(*) FILTER (WHERE status = 'succeeded' AND attempt_count > 1) AS fallback_total,
    COUNT(*) FILTER (WHERE error_code IN ('no_available_channel', 'routing_no_available_channel')) AS no_channel_total,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p50,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95
FROM attributed;

-- name: RouteOpsChannelPool :many
-- RouteOpsChannelPool 线路显式渠道池成员（pool_kind='explicit'）+ 渠道健康（抽屉渠道池 Tab）。
SELECT
    c.id AS channel_id,
    c.name AS channel_name,
    c.status AS channel_status,
    c.priority,
    p.name AS provider_name
FROM route_channels rc
JOIN channels c ON c.id = rc.channel_id
JOIN providers p ON p.id = c.provider_id
WHERE rc.route_id = sqlc.arg('route_id')
ORDER BY c.priority, c.id;

-- name: RouteOpsBoundProjects :many
-- RouteOpsBoundProjects 默认线路指向本线路的项目（抽屉绑定 Tab，P0）。
SELECT pj.id, pj.name, pj.user_id
FROM projects pj
WHERE pj.default_route_id = sqlc.arg('route_id')
ORDER BY pj.id
LIMIT 200;

-- name: RouteOpsBoundKeys :many
-- RouteOpsBoundKeys 绑定本线路的 API Key（抽屉绑定 Tab，P0）。状态由时间戳派生。
SELECT k.id, k.name, k.project_id, k.disabled_at, k.revoked_at, k.expires_at
FROM api_keys k
WHERE k.route_id = sqlc.arg('route_id')
ORDER BY k.id
LIMIT 200;

-- name: RouteOpsPerformanceTimeseries :many
WITH attributed AS (
    SELECT r.created_at, r.status, r.started_at, r.completed_at
    FROM request_records r
    JOIN api_keys ak ON ak.id = r.api_key_id
    JOIN projects p ON p.id = r.project_id
    WHERE COALESCE(ak.route_id, p.default_route_id) = sqlc.arg('route_id')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
)
SELECT
    date_trunc(sqlc.arg('unit')::text, created_at)::timestamptz AS bucket,
    COUNT(*) FILTER (WHERE status IN ('succeeded', 'failed', 'canceled')) AS request_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS request_succeeded,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95
FROM attributed
GROUP BY bucket
ORDER BY bucket;

-- name: RouteOpsModels :many
-- RouteOpsModels 本线路下各模型表现（抽屉模型 Tab，精简 §1.8）。
WITH attributed AS (
    SELECT r.requested_model_id, r.status
    FROM request_records r
    JOIN api_keys ak ON ak.id = r.api_key_id
    JOIN projects p ON p.id = r.project_id
    WHERE COALESCE(ak.route_id, p.default_route_id) = sqlc.arg('route_id')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
)
SELECT
    requested_model_id AS model_id,
    COUNT(*) FILTER (WHERE status IN ('succeeded', 'failed', 'canceled')) AS request_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS request_succeeded
FROM attributed
GROUP BY requested_model_id
ORDER BY request_total DESC
LIMIT 50;

-- name: RouteOpsRequests :many
-- RouteOpsRequests 本线路最近请求（抽屉请求 Tab，分页）。
SELECT
    r.request_id,
    r.created_at,
    r.status,
    r.requested_model_id,
    r.final_channel_id,
    CASE WHEN r.completed_at IS NOT NULL THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8 END AS latency_ms
FROM request_records r
JOIN api_keys ak ON ak.id = r.api_key_id
JOIN projects p ON p.id = r.project_id
WHERE COALESCE(ak.route_id, p.default_route_id) = sqlc.arg('route_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY r.created_at DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: RouteOpsRequestsCount :one
SELECT COUNT(*) AS total
FROM request_records r
JOIN api_keys ak ON ak.id = r.api_key_id
JOIN projects p ON p.id = r.project_id
WHERE COALESCE(ak.route_id, p.default_route_id) = sqlc.arg('route_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz);
