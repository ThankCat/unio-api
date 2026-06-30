-- §3.2 服务商聚合视图只读运维聚合。轻聚合：无 12 卡，表 + 4 Tab 抽屉。
-- provider 维度天然由 request_attempts.provider_id 归因（每次尝试记录 provider）。
-- 区间 [from,to) 半开；attempt 粒度性能/成功率；延迟由 completed_at-started_at 推导（毫秒）。

-- name: ProvidersOpsTable :many
-- ProvidersOpsTable 服务商运维主表（分页）：每 provider 渠道数 + attempt 聚合，最需处理优先。
WITH filtered_providers AS (
    SELECT p.id, p.slug, p.name, p.status, p.created_at
    FROM providers p
    WHERE (sqlc.narg('status')::text IS NULL OR p.status = sqlc.narg('status')::text)
      AND (sqlc.narg('search')::text IS NULL OR p.name ILIKE '%' || sqlc.narg('search')::text || '%' OR p.slug ILIKE '%' || sqlc.narg('search')::text || '%')
),
attempt_agg AS (
    SELECT
        fp.id,
        fp.slug,
        fp.name,
        fp.status,
        fp.created_at,
        (SELECT COUNT(*) FROM channels c WHERE c.provider_id = fp.id) AS channel_total,
        (SELECT COUNT(*) FROM channels c WHERE c.provider_id = fp.id AND c.status = 'enabled') AS channel_enabled,
        COUNT(a.id) AS attempt_total,
        COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS attempt_succeeded,
        COUNT(a.id) FILTER (WHERE a.error_code ILIKE '%timeout%' OR a.error_code = 'context_deadline_exceeded') AS timeout_total,
        COUNT(a.id) FILTER (WHERE a.status = 'succeeded' AND a.completed_at IS NOT NULL) AS latency_sample,
        COALESCE(AVG(CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
            THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg,
        COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
            CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
                 THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p50,
        COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY
            CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
                 THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p90,
        COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
            CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
                 THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
        COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY
            CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
                 THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p99,
        (MAX(a.completed_at) FILTER (WHERE a.status = 'succeeded'))::timestamptz AS last_success_at
    FROM filtered_providers fp
    LEFT JOIN request_attempts a
        ON a.provider_id = fp.id
        AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
        AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
    GROUP BY fp.id, fp.slug, fp.name, fp.status, fp.created_at
),
money_agg AS (
    SELECT
        r.final_provider_id AS provider_id,
        COALESCE(SUM(
            u.uncached_input_tokens + u.cache_read_input_tokens
            + u.cache_write_5m_input_tokens + u.cache_write_1h_input_tokens
            + u.output_tokens_total
        ), 0)::bigint AS tokens_total,
        COALESCE(SUM(le.amount) FILTER (WHERE le.entry_type = 'debit' AND le.currency = 'USD'), 0)::numeric AS revenue_usd,
        COALESCE(SUM(cs.total_cost_amount) FILTER (WHERE cs.currency = 'USD'), 0)::numeric AS cost_usd
    FROM request_records r
    LEFT JOIN usage_records u ON u.request_record_id = r.id
    LEFT JOIN cost_snapshots cs ON cs.request_record_id = r.id
    LEFT JOIN ledger_entries le ON le.request_record_id = r.id
    WHERE r.final_provider_id IS NOT NULL
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
    GROUP BY r.final_provider_id
),
tps_agg AS (
    SELECT
        a.provider_id,
        COALESCE(
            SUM(u.output_tokens_total)::float8 / NULLIF(SUM(
                CASE
                    WHEN a.completed_at IS NOT NULL
                    THEN EXTRACT(EPOCH FROM (a.completed_at - COALESCE(a.response_started_at, a.started_at)))
                END
            ), 0),
            0
        )::float8 AS avg_tps
    FROM request_attempts a
    JOIN usage_records u ON u.request_record_id = a.request_record_id
    WHERE a.status = 'succeeded'
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
    GROUP BY a.provider_id
)
SELECT
    a.id,
    a.slug,
    a.name,
    a.status,
    a.created_at,
    a.channel_total,
    a.channel_enabled,
    a.attempt_total,
    a.attempt_succeeded,
    a.timeout_total,
    a.latency_sample,
    a.latency_avg,
    a.latency_p50,
    a.latency_p90,
    a.latency_p95,
    a.latency_p99,
    a.last_success_at,
    COALESCE(m.tokens_total, 0)::bigint AS tokens_total,
    COALESCE(m.revenue_usd, 0)::numeric AS revenue_usd,
    COALESCE(m.cost_usd, 0)::numeric AS cost_usd,
    COALESCE(t.avg_tps, 0)::float8 AS avg_tps
FROM attempt_agg a
LEFT JOIN money_agg m ON m.provider_id = a.id
LEFT JOIN tps_agg t ON t.provider_id = a.id
ORDER BY
  CASE WHEN sqlc.narg('sort_field')::text = 'success_rate' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (a.attempt_succeeded::float8 / NULLIF(a.attempt_total, 0)) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'success_rate' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (a.attempt_succeeded::float8 / NULLIF(a.attempt_total, 0)) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'name' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN a.name END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'name' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN a.name END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'requests' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN a.attempt_total END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'requests' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN a.attempt_total END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'tokens' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COALESCE(m.tokens_total, 0) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'tokens' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COALESCE(m.tokens_total, 0) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'margin' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (COALESCE(m.revenue_usd, 0) - COALESCE(m.cost_usd, 0)) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'margin' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (COALESCE(m.revenue_usd, 0) - COALESCE(m.cost_usd, 0)) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN a.created_at END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN a.created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'channels' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN a.channel_enabled END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'channels' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN a.channel_enabled END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'latency' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN a.latency_avg END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'latency' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN a.latency_avg END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'tps' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COALESCE(t.avg_tps, 0) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'tps' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COALESCE(t.avg_tps, 0) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'timeout' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN a.timeout_total END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'timeout' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN a.timeout_total END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN a.status END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN a.status END ASC NULLS LAST,
  a.id
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
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded' AND a.completed_at IS NOT NULL) AS latency_sample,
    COALESCE(AVG(CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p50,
    COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p90,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p99
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
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded' AND a.completed_at IS NOT NULL) AS latency_sample,
    COALESCE(AVG(CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p50,
    COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p90,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p99
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
    COALESCE(AVG(CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg
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
