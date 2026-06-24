-- §3.2 服务商聚合视图只读运维聚合。轻聚合：无 12 卡，表 + 4 Tab 抽屉。
-- provider 维度天然由 request_attempts.provider_id 归因（每次尝试记录 provider）。
-- 区间 [from,to) 半开；attempt 粒度性能/成功率；延迟由 completed_at-started_at 推导（毫秒）。

-- name: ProvidersOpsTable :many
-- ProvidersOpsTable 服务商运维主表（分页）：每 provider 渠道数 + attempt 聚合，最需处理优先。
SELECT
    p.id,
    p.slug,
    p.name,
    p.status,
    (SELECT COUNT(*) FROM channels c WHERE c.provider_id = p.id) AS channel_total,
    (SELECT COUNT(*) FROM channels c WHERE c.provider_id = p.id AND c.status = 'enabled') AS channel_enabled,
    COUNT(a.id) AS attempt_total,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS attempt_succeeded,
    COUNT(a.id) FILTER (WHERE a.error_code ILIKE '%timeout%' OR a.error_code = 'context_deadline_exceeded') AS timeout_total,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    (MAX(a.completed_at) FILTER (WHERE a.status = 'succeeded'))::timestamptz AS last_success_at
FROM providers p
LEFT JOIN request_attempts a
    ON a.provider_id = p.id
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
WHERE (sqlc.narg('status')::text IS NULL OR p.status = sqlc.narg('status')::text)
  AND (sqlc.narg('search')::text IS NULL OR p.name ILIKE '%' || sqlc.narg('search')::text || '%' OR p.slug ILIKE '%' || sqlc.narg('search')::text || '%')
GROUP BY p.id, p.slug, p.name, p.status
ORDER BY
    (COUNT(a.id) FILTER (WHERE a.status = 'succeeded')::float8 / NULLIF(COUNT(a.id), 0)) ASC NULLS LAST,
    COUNT(a.id) DESC,
    p.id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ProvidersOpsTableCount :one
SELECT COUNT(*) AS total
FROM providers p
WHERE (sqlc.narg('status')::text IS NULL OR p.status = sqlc.narg('status')::text)
  AND (sqlc.narg('search')::text IS NULL OR p.name ILIKE '%' || sqlc.narg('search')::text || '%' OR p.slug ILIKE '%' || sqlc.narg('search')::text || '%');

-- name: ProviderOpsDetail :one
-- ProviderOpsDetail 单服务商抽屉概览：渠道数 + attempt 聚合。
SELECT
    (SELECT COUNT(*) FROM channels c WHERE c.provider_id = sqlc.arg('provider_id')) AS channel_total,
    (SELECT COUNT(*) FROM channels c WHERE c.provider_id = sqlc.arg('provider_id') AND c.status = 'enabled') AS channel_enabled,
    COUNT(a.id) AS attempt_total,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS attempt_succeeded,
    COUNT(a.id) FILTER (WHERE a.error_code ILIKE '%timeout%' OR a.error_code = 'context_deadline_exceeded') AS timeout_total,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p50,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95
FROM request_attempts a
WHERE a.provider_id = sqlc.arg('provider_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz);

-- name: ProviderOpsChannels :many
-- ProviderOpsChannels 单服务商下渠道精简子列表 + attempt 指标（抽屉渠道 Tab）。
SELECT
    c.id,
    c.name,
    c.base_url,
    c.status,
    COUNT(a.id) AS attempt_total,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS attempt_succeeded,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95
FROM channels c
LEFT JOIN request_attempts a
    ON a.channel_id = c.id
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
WHERE c.provider_id = sqlc.arg('provider_id')
GROUP BY c.id, c.name, c.base_url, c.status
ORDER BY attempt_total DESC, c.id;

-- name: ProviderOpsPerformanceTimeseries :many
-- ProviderOpsPerformanceTimeseries 单服务商 attempt 趋势（抽屉性能 Tab）。
SELECT
    date_trunc(sqlc.arg('unit')::text, created_at)::timestamptz AS bucket,
    COUNT(*) AS attempt_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS attempt_succeeded,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95
FROM request_attempts
WHERE provider_id = sqlc.arg('provider_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY bucket
ORDER BY bucket;

-- name: ProviderOpsErrors :many
-- ProviderOpsErrors 单服务商错误明细（抽屉错误 Tab，分页）。
SELECT
    a.created_at,
    c.name AS channel_name,
    a.upstream_model,
    a.error_code,
    a.upstream_status_code,
    r.request_id
FROM request_attempts a
JOIN request_records r ON r.id = a.request_record_id
JOIN channels c ON c.id = a.channel_id
WHERE a.provider_id = sqlc.arg('provider_id')
  AND a.status IN ('failed', 'canceled')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY a.created_at DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ProviderOpsErrorsCount :one
SELECT COUNT(*) AS total
FROM request_attempts a
WHERE a.provider_id = sqlc.arg('provider_id')
  AND a.status IN ('failed', 'canceled')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz);
