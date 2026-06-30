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
    COUNT(lat_ms) AS latency_sample,
    COALESCE(AVG(ttft_ms), 0)::float8 AS ttft_avg,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY ttft_ms), 0)::float8 AS ttft_p50,
    COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY ttft_ms), 0)::float8 AS ttft_p90,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY ttft_ms), 0)::float8 AS ttft_p95,
    COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY ttft_ms), 0)::float8 AS ttft_p99,
    COUNT(ttft_ms) AS ttft_sample
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
-- DashboardRadarTokens 在区间内汇总 token 分项（供缓存命中率与 token 总量卡）。
SELECT
    COALESCE(SUM(uncached_input_tokens), 0)::bigint AS uncached_input,
    COALESCE(SUM(cache_read_input_tokens), 0)::bigint AS cache_read_input,
    COALESCE(SUM(cache_write_5m_input_tokens), 0)::bigint AS cache_write_5m_input,
    COALESCE(SUM(cache_write_1h_input_tokens), 0)::bigint AS cache_write_1h_input,
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

-- name: DashboardTopErrors :many
-- DashboardTopErrors 汇总区间内失败请求的错误码分布（Top 10），供概览「失败原因」面板。
-- 仅统计 status='failed'（canceled 为客户端取消，不算平台失败）；error_code 空归一为 'unknown'。
-- failed_total 用窗口函数返回全部失败总数（在 LIMIT 前求值），供 service 计算占比。
SELECT
    COALESCE(NULLIF(error_code, ''), 'unknown')::text AS error_code,
    COUNT(*) AS total,
    SUM(COUNT(*)) OVER ()::bigint AS failed_total
FROM request_records
WHERE status = 'failed'
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY COALESCE(NULLIF(error_code, ''), 'unknown')
ORDER BY total DESC, error_code ASC
LIMIT 10;

-- name: DashboardBreakdownRoute :many
-- DashboardBreakdownRoute 按 API Key 绑定线路聚合区间请求（§3.1.8）：归属 = api_keys.route_id（线路必填，无默认回落）。
-- 附 token 合计 / 成本(USD) / P95 延迟；usage_records、cost_snapshots 与请求 1:1，LEFT JOIN 不放大行数。
WITH per_request AS (
    SELECT
        ak.route_id AS route_id,
        rt.name AS route_name,
        rt.status AS route_status,
        r.status,
        r.error_code,
        r.created_at,
        r.started_at,
        r.completed_at,
        COALESCE(
            ur.uncached_input_tokens + ur.cache_read_input_tokens
            + ur.cache_write_5m_input_tokens + ur.cache_write_1h_input_tokens
            + ur.output_tokens_total,
            0
        )::bigint AS tokens_total,
        COALESCE(
            CASE WHEN cs.currency = 'USD' THEN cs.total_cost_amount END,
            0
        )::numeric AS cost_usd,
        COALESCE(
            CASE WHEN le.entry_type = 'debit' AND le.currency = 'USD' THEN le.amount END,
            0
        )::numeric AS revenue_usd,
        CASE
            WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
            THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8
        END AS latency_ms
    FROM request_records r
    JOIN api_keys ak ON ak.id = r.api_key_id
    LEFT JOIN routes rt ON rt.id = ak.route_id
    LEFT JOIN usage_records ur ON ur.request_record_id = r.id
    LEFT JOIN cost_snapshots cs ON cs.request_record_id = r.id
    LEFT JOIN ledger_entries le ON le.request_record_id = r.id
    WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
)
SELECT
    route_id,
    route_name,
    route_status,
    COUNT(*) FILTER (WHERE status IN ('succeeded', 'failed', 'canceled')) AS terminal_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS succeeded_total,
    COUNT(*) FILTER (WHERE status = 'failed') AS failed_total,
    COALESCE(SUM(tokens_total), 0)::bigint AS tokens_total,
    COALESCE(SUM(revenue_usd), 0)::numeric AS revenue_usd,
    COALESCE(SUM(cost_usd), 0)::numeric AS cost_usd,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms), 0)::float8 AS latency_p95,
    (
        SELECT pr2.error_code
        FROM per_request pr2
        WHERE pr2.route_id IS NOT DISTINCT FROM pr.route_id
          AND pr2.error_code IS NOT NULL
        ORDER BY pr2.created_at DESC
        LIMIT 1
    ) AS recent_error_code
FROM per_request pr
GROUP BY route_id, route_name, route_status
ORDER BY terminal_total DESC
LIMIT 20;

-- name: DashboardBreakdownProvider :many
-- DashboardBreakdownProvider 按服务商 attempt 聚合成功率/延迟；Token/金额仍按最终请求账务事实聚合。
WITH attempt_agg AS (
    SELECT
        p.id AS provider_id,
        p.name AS provider_name,
        p.status AS provider_status,
        COUNT(a.id) AS terminal_total,
        COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS succeeded_total,
        COUNT(a.id) FILTER (WHERE a.status = 'failed') AS failed_total,
        COUNT(DISTINCT a.channel_id) AS channel_count,
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
    FROM providers p
    JOIN request_attempts a
      ON a.provider_id = p.id
     AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
     AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
    GROUP BY p.id, p.name, p.status
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
    a.provider_id,
    a.provider_name,
    a.provider_status,
    a.terminal_total,
    a.succeeded_total,
    a.failed_total,
    a.channel_count,
    COALESCE(m.tokens_total, 0)::bigint AS tokens_total,
    COALESCE(m.revenue_usd, 0)::numeric AS revenue_usd,
    COALESCE(m.cost_usd, 0)::numeric AS cost_usd,
    a.latency_sample,
    a.latency_avg,
    a.latency_p50,
    a.latency_p90,
    a.latency_p95,
    a.latency_p99,
    COALESCE(t.avg_tps, 0)::float8 AS avg_tps
FROM attempt_agg a
LEFT JOIN money_agg m ON m.provider_id = a.provider_id
LEFT JOIN tps_agg t ON t.provider_id = a.provider_id
ORDER BY a.terminal_total DESC
LIMIT 20;

-- name: DashboardBreakdownChannel :many
-- DashboardBreakdownChannel 按渠道 attempt 聚合成功率/延迟；Token/金额仍按最终请求账务事实聚合。
WITH attempt_agg AS (
    SELECT
        c.id AS channel_id,
        c.name AS channel_name,
        c.status AS channel_status,
        COUNT(a.id) AS terminal_total,
        COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS succeeded_total,
        COUNT(a.id) FILTER (WHERE a.status = 'failed') AS failed_total,
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
    JOIN request_attempts a
      ON a.channel_id = c.id
     AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
     AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
    GROUP BY c.id, c.name, c.status
),
money_agg AS (
    SELECT
        r.final_channel_id AS channel_id,
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
    WHERE r.final_channel_id IS NOT NULL
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
    GROUP BY r.final_channel_id
),
tps_agg AS (
    SELECT
        a.channel_id,
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
    GROUP BY a.channel_id
),
latest_error AS (
    SELECT DISTINCT ON (a.channel_id)
        a.channel_id,
        a.error_code
    FROM request_attempts a
    WHERE a.error_code IS NOT NULL
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
    ORDER BY a.channel_id, a.created_at DESC
)
SELECT
    a.channel_id,
    a.channel_name,
    a.channel_status,
    a.terminal_total,
    a.succeeded_total,
    a.failed_total,
    COALESCE(m.tokens_total, 0)::bigint AS tokens_total,
    COALESCE(m.revenue_usd, 0)::numeric AS revenue_usd,
    COALESCE(m.cost_usd, 0)::numeric AS cost_usd,
    a.latency_sample,
    a.latency_avg,
    a.latency_p50,
    a.latency_p90,
    a.latency_p95,
    a.latency_p99,
    COALESCE(t.avg_tps, 0)::float8 AS avg_tps,
    le.error_code AS recent_error_code
FROM attempt_agg a
LEFT JOIN money_agg m ON m.channel_id = a.channel_id
LEFT JOIN tps_agg t ON t.channel_id = a.channel_id
LEFT JOIN latest_error le ON le.channel_id = a.channel_id
ORDER BY a.terminal_total DESC
LIMIT 20;

-- name: DashboardChannelSuccessBuckets :many
-- DashboardChannelSuccessBuckets 返回「渠道表现」Top 20 渠道的最近 10 分钟成功率桶。
WITH top_channels AS (
    SELECT
        a.channel_id,
        COUNT(a.id) AS terminal_total
    FROM request_attempts a
    WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
    GROUP BY a.channel_id
    ORDER BY terminal_total DESC
    LIMIT 20
),
bucketed AS (
    SELECT
        a.channel_id,
        date_bin('10 minutes'::interval, a.created_at, '1970-01-01 00:00:00+00'::timestamptz)::timestamptz AS bucket,
        COUNT(a.id)::bigint AS terminal_total,
        COUNT(a.id) FILTER (WHERE a.status = 'succeeded')::bigint AS succeeded_total
    FROM request_attempts a
    JOIN top_channels tc ON tc.channel_id = a.channel_id
    WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
    GROUP BY a.channel_id, date_bin('10 minutes'::interval, a.created_at, '1970-01-01 00:00:00+00'::timestamptz)
),
ranked AS (
    SELECT
        h.*,
        row_number() OVER (PARTITION BY h.channel_id ORDER BY h.bucket DESC) AS recency_rank
    FROM bucketed h
)
SELECT
    channel_id,
    bucket,
    terminal_total,
    succeeded_total,
    COALESCE(succeeded_total::float8 / NULLIF(terminal_total, 0), 0)::float8 AS success_rate
FROM ranked
WHERE recency_rank <= 144
ORDER BY channel_id, bucket;

-- name: DashboardBreakdownModel :many
-- DashboardBreakdownModel 按对外请求模型聚合区间请求（精简 Top），附 token / 成本(USD) / P95 延迟。
SELECT
    r.requested_model_id AS model_id,
    COUNT(*) FILTER (WHERE r.status IN ('succeeded', 'failed', 'canceled')) AS terminal_total,
    COUNT(*) FILTER (WHERE r.status = 'succeeded') AS succeeded_total,
    COUNT(*) FILTER (WHERE r.status = 'failed') AS failed_total,
    COALESCE(SUM(
        u.uncached_input_tokens + u.cache_read_input_tokens
        + u.cache_write_5m_input_tokens + u.cache_write_1h_input_tokens
        + u.output_tokens_total
    ), 0)::bigint AS tokens_total,
    COALESCE(SUM(le.amount) FILTER (WHERE le.entry_type = 'debit' AND le.currency = 'USD'), 0)::numeric AS revenue_usd,
    COALESCE(SUM(cs.total_cost_amount) FILTER (WHERE cs.currency = 'USD'), 0)::numeric AS cost_usd,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE
            WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
            THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8
        END), 0)::float8 AS latency_p95
FROM request_records r
LEFT JOIN usage_records u ON u.request_record_id = r.id
LEFT JOIN cost_snapshots cs ON cs.request_record_id = r.id
LEFT JOIN ledger_entries le ON le.request_record_id = r.id
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
