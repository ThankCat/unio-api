-- M9+ 概览运营雷达（§3.1 重构）只读聚合。全部纯只读、不引入新业务事实。
-- 约定同 dashboard.sql：金额走 NUMERIC（service 格式化为十进制字符串，绝不经 float）；
-- 时间区间 [from, to)（左闭右开）；可空 from_time/to_time（narg，NULL = 不过滤）。
-- 性能/延迟以 request_records 时间戳推导（无预存延迟列）：
--   延迟 = completed_at - started_at；TTFT = response_started_at - started_at（毫秒）。
-- percentile_cont 自动忽略 ORDER BY 中的 NULL 行，故用 CASE 把非目标行置 NULL。

-- name: DashboardRadarRequestPerf :one
-- DashboardRadarRequestPerf 在区间内一次性返回请求终态计数 + 超时数 + 延迟/TTFT 分位数（request 粒度）。
SELECT
    COUNT(*) FILTER (WHERE status IN ('succeeded', 'failed', 'canceled')) AS terminal_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS succeeded_total,
    COUNT(*) FILTER (WHERE status = 'failed') AS failed_total,
    COUNT(*) FILTER (WHERE status = 'canceled') AS canceled_total,
    COUNT(*) FILTER (WHERE status IN ('pending', 'running')) AS pending_total,
    COUNT(*) FILTER (WHERE error_code ILIKE '%timeout%' OR error_code = 'context_deadline_exceeded') AS timeout_total,
    COALESCE(AVG(lat_ms), 0)::float8 AS latency_avg,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY lat_ms), 0)::float8 AS latency_p50,
    COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY lat_ms), 0)::float8 AS latency_p90,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY lat_ms), 0)::float8 AS latency_p95,
    COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY lat_ms), 0)::float8 AS latency_p99,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY ttft_ms), 0)::float8 AS ttft_p50,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY ttft_ms), 0)::float8 AS ttft_p95
FROM (
    SELECT
        status,
        error_code,
        CASE
            WHEN status = 'succeeded' AND completed_at IS NOT NULL
            THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8
        END AS lat_ms,
        CASE
            WHEN response_started_at IS NOT NULL
            THEN (EXTRACT(EPOCH FROM (response_started_at - started_at)) * 1000)::float8
        END AS ttft_ms
    FROM request_records
    WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
) s;

-- name: DashboardRadarThroughput :one
-- DashboardRadarThroughput 汇总成功请求的输出 token 与生成耗时（秒），service 据此算 TPS。
-- 生成耗时优先用 completed_at - response_started_at（首 token 之后），缺 TTFT 时退回 started_at。
SELECT
    COALESCE(SUM(u.output_tokens_total), 0)::bigint AS output_tokens,
    COALESCE(SUM(
        CASE
            WHEN r.completed_at IS NOT NULL
            THEN EXTRACT(EPOCH FROM (r.completed_at - COALESCE(r.response_started_at, r.started_at)))
        END
    ), 0)::float8 AS generation_seconds
FROM request_records r
JOIN usage_records u ON u.request_record_id = r.id
WHERE r.status = 'succeeded'
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz);

-- name: DashboardRadarTokens :one
-- DashboardRadarTokens 在区间内汇总 token 分项（供缓存读取率/写入率与 token 总量卡）。
SELECT
    COALESCE(SUM(uncached_input_tokens), 0)::bigint AS uncached_input,
    COALESCE(SUM(cache_read_input_tokens), 0)::bigint AS cache_read_input,
    COALESCE(SUM(cache_write_5m_input_tokens + cache_write_1h_input_tokens), 0)::bigint AS cache_write_input,
    COALESCE(SUM(output_tokens_total), 0)::bigint AS output_tokens
FROM usage_records
WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz);

-- name: DashboardRadarSettlementBacklog :one
-- DashboardRadarSettlementBacklog 返回结算补偿任务积压（时点值，无时间过滤）：
-- active = pending+running（自动重试中），dead = 已耗尽需人工。
SELECT
    COUNT(*) FILTER (WHERE status IN ('pending', 'running')) AS active_total,
    COUNT(*) FILTER (WHERE status = 'dead') AS dead_total
FROM settlement_recovery_jobs;

-- name: DashboardRadarStatusWindow :one
-- DashboardRadarStatusWindow 在独立短窗口（如近 15min）聚合平台健康判定所需计数。
SELECT
    COUNT(*) FILTER (WHERE status IN ('succeeded', 'failed', 'canceled')) AS terminal_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS succeeded_total,
    COUNT(*) FILTER (WHERE error_code IN ('no_available_channel', 'routing_no_available_channel')) AS no_channel_total,
    COUNT(*) FILTER (WHERE error_code ILIKE '%timeout%' OR error_code = 'context_deadline_exceeded') AS timeout_total
FROM request_records
WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz);

-- name: DashboardRadarBadChannels :many
-- DashboardRadarBadChannels 返回区间内有尝试的渠道里「最差」的若干条（精简列，§1.8）：
-- 渠道 + 健康（service 据成功率分桶）+ 成功率 + 最近错误码。完整列表去渠道页。
SELECT
    c.id AS channel_id,
    c.name,
    c.status,
    COUNT(a.id) AS attempt_total,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS attempt_succeeded,
    (
        SELECT a2.error_code
        FROM request_attempts a2
        WHERE a2.channel_id = c.id
          AND a2.error_code IS NOT NULL
          AND (sqlc.narg('from_time')::timestamptz IS NULL OR a2.created_at >= sqlc.narg('from_time')::timestamptz)
          AND (sqlc.narg('to_time')::timestamptz IS NULL OR a2.created_at < sqlc.narg('to_time')::timestamptz)
        ORDER BY a2.created_at DESC
        LIMIT 1
    ) AS recent_error_code
FROM channels c
LEFT JOIN request_attempts a
    ON a.channel_id = c.id
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY c.id, c.name, c.status
HAVING COUNT(a.id) > 0
ORDER BY (COUNT(a.id) FILTER (WHERE a.status = 'succeeded')::float8 / NULLIF(COUNT(a.id), 0)) ASC NULLS LAST,
         COUNT(a.id) DESC
LIMIT 10;

-- name: DashboardBreakdownRoute :many
-- DashboardBreakdownRoute 按「就近绑定」归属线路聚合区间请求（§3.1.8）：
-- api_keys.route_id ?? projects.default_route_id ?? 内置桶（route_id 为 NULL）。
SELECT
    COALESCE(ak.route_id, p.default_route_id) AS route_id,
    rt.name AS route_name,
    COUNT(*) FILTER (WHERE r.status IN ('succeeded', 'failed', 'canceled')) AS terminal_total,
    COUNT(*) FILTER (WHERE r.status = 'succeeded') AS succeeded_total
FROM request_records r
JOIN api_keys ak ON ak.id = r.api_key_id
JOIN projects p ON p.id = r.project_id
LEFT JOIN routes rt ON rt.id = COALESCE(ak.route_id, p.default_route_id)
WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY COALESCE(ak.route_id, p.default_route_id), rt.name
ORDER BY terminal_total DESC
LIMIT 20;

-- name: DashboardBreakdownChannel :many
-- DashboardBreakdownChannel 按最终渠道聚合区间请求（精简 Top）。
SELECT
    r.final_channel_id AS channel_id,
    c.name AS channel_name,
    c.status AS channel_status,
    COUNT(*) FILTER (WHERE r.status IN ('succeeded', 'failed', 'canceled')) AS terminal_total,
    COUNT(*) FILTER (WHERE r.status = 'succeeded') AS succeeded_total
FROM request_records r
LEFT JOIN channels c ON c.id = r.final_channel_id
WHERE r.final_channel_id IS NOT NULL
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY r.final_channel_id, c.name, c.status
ORDER BY terminal_total DESC
LIMIT 20;

-- name: DashboardBreakdownModel :many
-- DashboardBreakdownModel 按对外请求模型聚合区间请求（精简 Top）。
SELECT
    r.requested_model_id AS model_id,
    COUNT(*) FILTER (WHERE r.status IN ('succeeded', 'failed', 'canceled')) AS terminal_total,
    COUNT(*) FILTER (WHERE r.status = 'succeeded') AS succeeded_total
FROM request_records r
WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY r.requested_model_id
ORDER BY terminal_total DESC
LIMIT 20;

-- name: DashboardPerformanceTimeseries :many
-- DashboardPerformanceTimeseries 按时间桶聚合 P95 延迟 / P95 TTFT / TPS（性能趋势图）。
-- request_records 与 usage_records 为 1:1（usage.request_record_id UNIQUE），JOIN 不放大行数。
SELECT
    date_trunc(sqlc.arg('unit')::text, r.created_at)::timestamptz AS bucket,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE
            WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
            THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8
        END), 0)::float8 AS latency_p95,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE
            WHEN r.response_started_at IS NOT NULL
            THEN (EXTRACT(EPOCH FROM (r.response_started_at - r.started_at)) * 1000)::float8
        END), 0)::float8 AS ttft_p95,
    COALESCE(SUM(u.output_tokens_total) FILTER (WHERE r.status = 'succeeded'), 0)::bigint AS output_tokens,
    COALESCE(SUM(
        CASE
            WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
            THEN EXTRACT(EPOCH FROM (r.completed_at - COALESCE(r.response_started_at, r.started_at)))
        END
    ), 0)::float8 AS generation_seconds
FROM request_records r
LEFT JOIN usage_records u ON u.request_record_id = r.id
WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY bucket
ORDER BY bucket;
