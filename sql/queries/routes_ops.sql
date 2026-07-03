-- §3.5 线路路由作战台只读运维聚合。
-- 归因（线路必填 §3.1）：每条请求归属其 API Key 绑定的 api_keys.route_id（线路必填，无默认回落）。
-- request 粒度。fallback：同 request 有 >1 次 attempt 且最终成功；no_channel：error_code 命中无可用渠道码。

-- name: RoutesOpsCounts :one
SELECT
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE status = 'enabled') AS enabled,
    COUNT(*) FILTER (WHERE status = 'disabled') AS disabled
FROM routes;

-- name: RoutesOpsAttributeAggregate :one
-- RoutesOpsAttributeAggregate 在区间内对「已归因到某线路」的请求做整体聚合（供概览卡）。
WITH attributed AS (
    SELECT
        ak.route_id AS route_id,
        r.status,
        r.error_code,
        r.started_at,
        r.completed_at,
        (SELECT COUNT(*) FROM request_attempts a WHERE a.request_record_id = r.id) AS attempt_count
    FROM request_records r
    JOIN api_keys ak ON ak.id = r.api_key_id
    WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
)
SELECT
    COUNT(*) FILTER (WHERE route_id IS NOT NULL AND status IN ('succeeded', 'failed')) AS terminal_total,
    COUNT(*) FILTER (WHERE route_id IS NOT NULL AND status = 'succeeded') AS succeeded_total,
    COUNT(*) FILTER (WHERE route_id IS NOT NULL AND status = 'succeeded' AND attempt_count > 1) AS fallback_total,
    COUNT(*) FILTER (WHERE route_id IS NOT NULL AND error_code IN ('no_available_channel', 'routing_no_available_channel')) AS no_channel_total,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN route_id IS NOT NULL AND status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95
FROM attributed;

-- name: RoutesOpsTable :many
-- RoutesOpsTable 线路运维主表（分页）：静态配置 + 绑定/池/可达模型数；请求指标在详情页聚合。
SELECT
    rt.id,
    rt.name,
    rt.mode,
    rt.pool_kind,
    rt.status,
    rt.description,
    rt.price_ratio,
    rt.rpm_limit,
    rt.tpm_limit,
    rt.rpd_limit,
    rt.created_at,
    (SELECT COUNT(*) FROM api_keys kk WHERE kk.route_id = rt.id) AS bound_keys,
    (SELECT COUNT(*) FROM route_channels rc WHERE rc.route_id = rt.id) AS pool_channels,
    (
        SELECT COUNT(DISTINCT m.id)
        FROM models m
        JOIN channel_models cm ON cm.model_id = m.id AND cm.status = 'enabled'
        JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled'
        WHERE EXISTS (
            SELECT 1 FROM channel_prices p
            WHERE p.channel_id = cm.channel_id AND p.model_id = cm.model_id AND p.status = 'enabled'
        )
        AND (
            rt.pool_kind = 'all'
            OR cm.channel_id IN (SELECT channel_id FROM route_channels WHERE route_id = rt.id)
        )
    ) AS models_count
FROM routes rt
WHERE (sqlc.narg('status')::text IS NULL OR rt.status = sqlc.narg('status')::text)
  AND (sqlc.narg('search')::text IS NULL OR rt.name ILIKE '%' || sqlc.narg('search')::text || '%')
ORDER BY
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'name') IN ('', 'name') AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN rt.name END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'name') IN ('', 'name') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN rt.name END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN rt.created_at END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN rt.created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'bindings' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(*) FROM api_keys kk WHERE kk.route_id = rt.id
    ) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'bindings' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(*) FROM api_keys kk WHERE kk.route_id = rt.id
    ) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'pool_channels' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(*) FROM route_channels rc WHERE rc.route_id = rt.id
    ) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'pool_channels' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(*) FROM route_channels rc WHERE rc.route_id = rt.id
    ) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'models' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(DISTINCT m.id)
        FROM models m
        JOIN channel_models cm ON cm.model_id = m.id AND cm.status = 'enabled'
        JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled'
        WHERE EXISTS (
            SELECT 1 FROM channel_prices p
            WHERE p.channel_id = cm.channel_id AND p.model_id = cm.model_id AND p.status = 'enabled'
        )
        AND (
            rt.pool_kind = 'all'
            OR cm.channel_id IN (SELECT channel_id FROM route_channels WHERE route_id = rt.id)
        )
    ) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'models' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(DISTINCT m.id)
        FROM models m
        JOIN channel_models cm ON cm.model_id = m.id AND cm.status = 'enabled'
        JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled'
        WHERE EXISTS (
            SELECT 1 FROM channel_prices p
            WHERE p.channel_id = cm.channel_id AND p.model_id = cm.model_id AND p.status = 'enabled'
        )
        AND (
            rt.pool_kind = 'all'
            OR cm.channel_id IN (SELECT channel_id FROM route_channels WHERE route_id = rt.id)
        )
    ) END ASC NULLS LAST,
  rt.name
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
    WHERE ak.route_id = sqlc.arg('route_id')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
)
SELECT
    COUNT(*) FILTER (WHERE status IN ('succeeded', 'failed')) AS request_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS request_succeeded,
    COUNT(*) FILTER (WHERE status = 'succeeded' AND attempt_count > 1) AS fallback_total,
    COUNT(*) FILTER (WHERE error_code IN ('no_available_channel', 'routing_no_available_channel')) AS no_channel_total,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p50,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    (SELECT rt.status FROM routes rt WHERE rt.id = sqlc.arg('route_id')) AS route_status
FROM attributed;

-- name: RouteOpsReachableModels :many
-- RouteOpsReachableModels 线路可达模型（有启用绑定且有价格的 distinct 模型）。
SELECT
    m.model_id,
    m.display_name
FROM models m
JOIN channel_models cm ON cm.model_id = m.id AND cm.status = 'enabled'
JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled'
JOIN routes rt ON rt.id = sqlc.arg('route_id')
WHERE EXISTS (
    SELECT 1 FROM channel_prices p
    WHERE p.channel_id = cm.channel_id AND p.model_id = cm.model_id AND p.status = 'enabled'
)
AND (
    rt.pool_kind = 'all'
    OR cm.channel_id IN (SELECT channel_id FROM route_channels WHERE route_id = rt.id)
)
GROUP BY m.id, m.model_id, m.display_name
ORDER BY m.model_id
LIMIT 500;

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

-- name: RouteOpsBoundUsers :many
-- RouteOpsBoundUsers 拥有「绑定本线路的 API Key」的用户（去重，抽屉绑定 Tab，P0）。
SELECT DISTINCT u.id, u.email, u.display_name
FROM users u
JOIN api_keys k ON k.user_id = u.id
WHERE k.route_id = sqlc.arg('route_id')
ORDER BY u.id
LIMIT 200;

-- name: RouteOpsBoundKeys :many
-- RouteOpsBoundKeys 绑定本线路的 API Key（抽屉绑定 Tab，P0）。状态由时间戳派生。
SELECT k.id, k.name, k.user_id, k.disabled_at, k.revoked_at, k.expires_at
FROM api_keys k
WHERE k.route_id = sqlc.arg('route_id')
ORDER BY k.id
LIMIT 200;

-- name: RouteOpsPerformanceTimeseries :many
WITH attributed AS (
    SELECT r.created_at, r.status, r.started_at, r.completed_at
    FROM request_records r
    JOIN api_keys ak ON ak.id = r.api_key_id
    WHERE ak.route_id = sqlc.arg('route_id')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
)
SELECT
    date_trunc(sqlc.arg('unit')::text, created_at)::timestamptz AS bucket,
    COUNT(*) FILTER (WHERE status IN ('succeeded', 'failed')) AS request_total,
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
    WHERE ak.route_id = sqlc.arg('route_id')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
)
SELECT
    requested_model_id AS model_id,
    COUNT(*) FILTER (WHERE status IN ('succeeded', 'failed')) AS request_total,
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
WHERE ak.route_id = sqlc.arg('route_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY r.created_at DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: RouteOpsRequestsCount :one
SELECT COUNT(*) AS total
FROM request_records r
JOIN api_keys ak ON ak.id = r.api_key_id
WHERE ak.route_id = sqlc.arg('route_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz);
