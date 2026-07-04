-- §3.3 渠道作战台只读运维聚合。全部只读。
-- 口径：渠道性能/成功率/错误以 request_attempts（attempt 粒度，每次尝试命中一条渠道）为准；
-- TPS/token 因无 per-attempt usage，按 request_records.final_channel_id 归因（最终成功渠道）。
-- 区间 [from,to) 半开；narg 可空（NULL 不过滤）。延迟由 completed_at-started_at 推导（毫秒）。

-- name: ChannelsOpsCounts :one
-- ChannelsOpsCounts 返回渠道总数 / 启用 / 停用（时点）。
SELECT
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE status = 'enabled') AS enabled,
    COUNT(*) FILTER (WHERE status = 'disabled') AS disabled
FROM channels;

-- name: ChannelsOpsAttemptAggregate :one
-- ChannelsOpsAttemptAggregate 在区间内汇总全渠道 attempt 指标（成功率/超时/延迟分位）。
-- attempt_total 口径为「合格 attempt」= succeeded + failed（排除 running 未终态与 canceled 客户端取消，
-- 与运行时熔断器 IsChannelFaultError 一致，不把客户端取消/在途算作渠道结果）。
SELECT
    COUNT(*) FILTER (WHERE status = 'succeeded' OR fault_party = 'upstream') AS attempt_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS attempt_succeeded,
    COUNT(*) FILTER (WHERE status = 'failed' AND (error_code ILIKE '%timeout%' OR error_code = 'context_deadline_exceeded')) AS timeout_total,
    COUNT(*) FILTER (WHERE status = 'succeeded' AND completed_at IS NOT NULL) AS latency_sample,
    COALESCE(AVG(CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p50,
    COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p90,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p99
FROM request_attempts
WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz);

-- name: ChannelsOpsThroughput :one
-- ChannelsOpsThroughput 按最终成功渠道归因汇总输出 token 与生成耗时（供平台 TPS 卡）。
SELECT
    COALESCE(SUM(u.output_tokens_total), 0)::bigint AS output_tokens,
    COALESCE(SUM(
        CASE WHEN r.completed_at IS NOT NULL
             THEN EXTRACT(EPOCH FROM (r.completed_at - COALESCE(r.response_started_at, r.started_at))) END
    ), 0)::float8 AS generation_seconds
FROM request_records r
JOIN usage_records u ON u.request_record_id = r.id
WHERE r.status = 'succeeded'
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz);

-- name: ChannelsOpsHealthDistribution :one
-- ChannelsOpsHealthDistribution 按区间内 attempt 成功率对每条渠道分桶并计数（healthy/degraded/unhealthy/no_data）。
WITH per_channel AS (
    SELECT
        c.id,
        COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream') AS total,
        COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS succeeded
    FROM channels c
    LEFT JOIN request_attempts a
        ON a.channel_id = c.id
        AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
        AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
    GROUP BY c.id
)
SELECT
    COUNT(*) FILTER (WHERE total = 0) AS no_data,
    COUNT(*) FILTER (WHERE total > 0 AND succeeded::float8 / total >= 0.95) AS healthy,
    COUNT(*) FILTER (WHERE total > 0 AND succeeded::float8 / total >= 0.80 AND succeeded::float8 / total < 0.95) AS degraded,
    COUNT(*) FILTER (WHERE total > 0 AND succeeded::float8 / total < 0.80) AS unhealthy
FROM per_channel;

-- name: ChannelsOpsRecentError :many
-- ChannelsOpsRecentError 返回区间内最近一条渠道错误（用 :many LIMIT 1 规避无行报错）。
SELECT a.error_code, c.name AS channel_name, a.created_at
FROM request_attempts a
JOIN channels c ON c.id = a.channel_id
WHERE a.status = 'failed' AND a.fault_party = 'upstream' AND a.error_code IS NOT NULL
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY a.created_at DESC
LIMIT 1;

-- name: ChannelsOpsPriceCoverage :one
-- ChannelsOpsPriceCoverage 统计启用绑定的售价/成本完整度（售价必填，成本可空）。
WITH bindings AS (
    SELECT
        cm.channel_id,
        cm.model_id,
        EXISTS (
            SELECT 1 FROM channel_prices p
            WHERE p.channel_id = cm.channel_id AND p.model_id = cm.model_id AND p.status = 'enabled'
        ) AS has_price,
        EXISTS (
            SELECT 1 FROM channel_prices p
            WHERE p.channel_id = cm.channel_id AND p.model_id = cm.model_id AND p.status = 'enabled'
              AND p.uncached_input_cost IS NOT NULL AND p.output_cost IS NOT NULL
        ) AS has_cost
    FROM channel_models cm
    WHERE cm.status = 'enabled'
)
SELECT
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE has_price) AS with_price,
    COUNT(*) FILTER (WHERE has_cost) AS with_cost
FROM bindings;

-- name: ChannelsOpsTable :many
-- ChannelsOpsTable 渠道运维主表（分页）：每渠道 attempt 指标 + 绑定模型数 + 最近错误，默认最需处理优先。
SELECT
    c.id,
    c.name,
    c.status,
    c.protocol,
    c.adapter_key,
    c.base_url,
    c.priority,
    c.timeout_ms,
    c.credential,
    c.rpm_limit,
    c.tpm_limit,
    c.rpd_limit,
    c.created_at,
    c.last_tested_at,
    c.last_test_ok,
    c.last_test_latency_ms,
    c.last_test_error,
    c.credential_valid,
    pr.name AS provider_name,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream') AS attempt_total,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS attempt_succeeded,
    COUNT(a.id) FILTER (WHERE a.status = 'failed' AND (a.error_code ILIKE '%timeout%' OR a.error_code = 'context_deadline_exceeded')) AS timeout_total,
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
    (SELECT COUNT(*) FROM channel_models cm WHERE cm.channel_id = c.id AND cm.status = 'enabled') AS bound_models,
    (SELECT COUNT(*) FROM route_channels rc WHERE rc.channel_id = c.id) AS bound_routes,
    (
        SELECT a2.error_code FROM request_attempts a2
        WHERE a2.channel_id = c.id AND a2.status = 'failed' AND a2.fault_party = 'upstream' AND a2.error_code IS NOT NULL
          AND (sqlc.narg('from_time')::timestamptz IS NULL OR a2.created_at >= sqlc.narg('from_time')::timestamptz)
          AND (sqlc.narg('to_time')::timestamptz IS NULL OR a2.created_at < sqlc.narg('to_time')::timestamptz)
        ORDER BY a2.created_at DESC LIMIT 1
    ) AS recent_error_code
FROM channels c
JOIN providers pr ON pr.id = c.provider_id
LEFT JOIN request_attempts a
    ON a.channel_id = c.id
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
WHERE (sqlc.narg('status')::text IS NULL OR c.status = sqlc.narg('status')::text)
  AND (sqlc.narg('provider_id')::bigint IS NULL OR c.provider_id = sqlc.narg('provider_id')::bigint)
  AND (sqlc.narg('search')::text IS NULL OR c.name ILIKE '%' || sqlc.narg('search')::text || '%')
GROUP BY c.id, c.name, c.status, c.protocol, c.adapter_key, c.base_url, c.priority, c.timeout_ms, c.credential, c.rpm_limit, c.tpm_limit, c.rpd_limit, c.created_at, c.last_tested_at, c.last_test_ok, c.last_test_latency_ms, c.last_test_error, c.credential_valid, pr.name
ORDER BY
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'success_rate') IN ('', 'success_rate') AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (COUNT(a.id) FILTER (WHERE a.status = 'succeeded')::float8 / NULLIF(COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream'), 0)) END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'success_rate') IN ('', 'success_rate') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (COUNT(a.id) FILTER (WHERE a.status = 'succeeded')::float8 / NULLIF(COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream'), 0)) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'name' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN c.name END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'name' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN c.name END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'requests' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream') END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'requests' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream') END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN c.status END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN c.status END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN c.created_at END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN c.created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'latency' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COALESCE(AVG(CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'latency' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COALESCE(AVG(CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'timeout' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COUNT(a.id) FILTER (WHERE a.status = 'failed' AND (a.error_code ILIKE '%timeout%' OR a.error_code = 'context_deadline_exceeded')) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'timeout' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COUNT(a.id) FILTER (WHERE a.status = 'failed' AND (a.error_code ILIKE '%timeout%' OR a.error_code = 'context_deadline_exceeded')) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'bound_models' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (SELECT COUNT(*) FROM channel_models cm WHERE cm.channel_id = c.id AND cm.status = 'enabled') END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'bound_models' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (SELECT COUNT(*) FROM channel_models cm WHERE cm.channel_id = c.id AND cm.status = 'enabled') END ASC NULLS LAST,
  c.id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ChannelsOpsTableCount :one
-- ChannelsOpsTableCount 与 ChannelsOpsTable 同过滤条件下的渠道总数。
SELECT COUNT(*) AS total
FROM channels c
WHERE (sqlc.narg('status')::text IS NULL OR c.status = sqlc.narg('status')::text)
  AND (sqlc.narg('provider_id')::bigint IS NULL OR c.provider_id = sqlc.narg('provider_id')::bigint)
  AND (sqlc.narg('search')::text IS NULL OR c.name ILIKE '%' || sqlc.narg('search')::text || '%');

-- name: ChannelOpsDetail :one
-- ChannelOpsDetail 单渠道（抽屉概览）attempt 指标。attempt_total 口径同上：合格 attempt（succeeded+failed）。
SELECT
    COUNT(*) FILTER (WHERE status = 'succeeded' OR fault_party = 'upstream') AS attempt_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS attempt_succeeded,
    COUNT(*) FILTER (WHERE status = 'failed' AND (error_code ILIKE '%timeout%' OR error_code = 'context_deadline_exceeded')) AS timeout_total,
    COUNT(*) FILTER (WHERE status = 'succeeded' AND completed_at IS NOT NULL) AS latency_sample,
    COALESCE(AVG(CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p50,
    COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p90,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p99,
    (MAX(completed_at) FILTER (WHERE status = 'succeeded'))::timestamptz AS last_success_at,
    (MAX(completed_at) FILTER (WHERE status = 'failed' AND fault_party = 'upstream'))::timestamptz AS last_failure_at
FROM request_attempts
WHERE channel_id = sqlc.arg('channel_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz);

-- name: ChannelOpsPerformanceTimeseries :many
-- ChannelOpsPerformanceTimeseries 单渠道按时间桶的 attempt 量/成功/平均延迟（抽屉性能 Tab）。
SELECT
    date_trunc(sqlc.arg('unit')::text, created_at)::timestamptz AS bucket,
    COUNT(*) FILTER (WHERE status = 'succeeded' OR fault_party = 'upstream') AS attempt_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS attempt_succeeded,
    COALESCE(AVG(CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg
FROM request_attempts
WHERE channel_id = sqlc.arg('channel_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY bucket
ORDER BY bucket;

-- name: ChannelOpsErrors :many
-- ChannelOpsErrors 单渠道错误明细（抽屉错误 Tab，分页）。携带 request_id 便于跳证据中心。
SELECT
    a.created_at,
    a.upstream_model,
    a.error_code,
    a.upstream_status_code,
    a.error_message,
    r.request_id
FROM request_attempts a
JOIN request_records r ON r.id = a.request_record_id
WHERE a.channel_id = sqlc.arg('channel_id')
  AND a.status = 'failed'
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY a.created_at DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ChannelOpsErrorsCount :one
SELECT COUNT(*) AS total
FROM request_attempts a
WHERE a.channel_id = sqlc.arg('channel_id')
  AND a.status = 'failed'
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz);

-- name: ChannelOpsModels :many
-- ChannelOpsModels 单渠道绑定模型 + attempt 指标（抽屉模型 Tab，完整列 §1.8）。
-- attempt 无 model_id，按 upstream_model 关联绑定。
SELECT
    cm.model_id,
    m.model_id AS model_ref,
    m.display_name,
    cm.upstream_model,
    cm.status,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream') AS attempt_total,
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
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p99,
    EXISTS (
        SELECT 1 FROM channel_prices p
        WHERE p.channel_id = sqlc.arg('channel_id') AND p.model_id = cm.model_id AND p.status = 'enabled'
    ) AS has_price
FROM channel_models cm
JOIN models m ON m.id = cm.model_id
LEFT JOIN request_attempts a
    ON a.channel_id = cm.channel_id
    AND a.upstream_model = cm.upstream_model
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
WHERE cm.channel_id = sqlc.arg('channel_id')
GROUP BY cm.model_id, m.model_id, m.display_name, cm.upstream_model, cm.status
ORDER BY attempt_total DESC, m.model_id;

-- name: ChannelOpsRoutes :many
-- ChannelOpsRoutes 引用该渠道的显式线路池（抽屉线路 Tab）。
SELECT rt.id, rt.name, rt.mode, rt.pool_kind, rt.status, rt.price_ratio
FROM route_channels rc
JOIN routes rt ON rt.id = rc.route_id
WHERE rc.channel_id = sqlc.arg('channel_id')
ORDER BY rt.id;

-- name: ChannelOpsSuccessBuckets :many
-- ChannelOpsSuccessBuckets 单渠道最近 10 分钟 attempt 成功率桶（与概览渠道表现一致）。
SELECT
    date_bin('10 minutes'::interval, a.created_at, '1970-01-01 00:00:00+00'::timestamptz)::timestamptz AS bucket,
    COUNT(a.id)::bigint AS terminal_total,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded')::bigint AS succeeded_total,
    COALESCE(COUNT(a.id) FILTER (WHERE a.status = 'succeeded')::float8 / NULLIF(COUNT(a.id), 0), 0)::float8 AS success_rate
FROM request_attempts a
WHERE a.channel_id = sqlc.arg('channel_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY bucket
ORDER BY bucket DESC
LIMIT 144;
