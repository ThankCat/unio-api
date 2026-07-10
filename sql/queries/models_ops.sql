-- §3.4 模型商品控制台只读运维聚合。
-- 模型口径：request_records.requested_model_id(文本) = models.model_id。请求/性能为 request 粒度。
-- 成本按 cost_snapshots.model_id（数值 FK）归因；收入按 ledger_entries(debit) JOIN request 归因；仅 USD。
-- 可售/可用渠道：enabled 绑定 + 渠道 enabled + 有 enabled 价格（§3.4.8）。

-- name: ModelsOpsCounts :one
SELECT
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE status = 'enabled') AS enabled,
    COUNT(*) FILTER (WHERE status = 'disabled') AS disabled
FROM models;

-- name: ModelsOpsSellability :one
-- ModelsOpsSellability 统计可售模型数与「启用但无可用渠道」模型数。
WITH per_model AS (
    SELECT
        m.id,
        m.status,
        (
            SELECT COUNT(*)
            FROM channel_models cm
            JOIN channels c ON c.id = cm.channel_id
            WHERE cm.model_id = m.id AND cm.status = 'enabled' AND c.status = 'enabled'
              AND EXISTS (
                  SELECT 1 FROM channel_prices p
                  WHERE p.channel_id = cm.channel_id AND p.model_id = m.id AND p.status = 'enabled'
              )
        ) AS available
    FROM models m
)
SELECT
    COUNT(*) FILTER (WHERE status = 'enabled' AND available > 0) AS sellable,
    COUNT(*) FILTER (WHERE status = 'enabled' AND available = 0) AS no_channel
FROM per_model;

-- name: ModelsOpsPriceCompleteness :one
-- ModelsOpsPriceCompleteness 统计启用模型里有/无 enabled 价格的数量。
WITH per_model AS (
    SELECT
        m.id,
        EXISTS (
            SELECT 1 FROM channel_prices p WHERE p.model_id = m.id AND p.status = 'enabled'
        ) AS has_price
    FROM models m
    WHERE m.status = 'enabled'
)
SELECT
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE has_price) AS with_price
FROM per_model;

-- name: ModelsOpsRequestAggregate :one
SELECT
    COUNT(*) FILTER (WHERE status IN ('succeeded', 'failed')) AS terminal_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS succeeded_total
FROM request_records
WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz);

-- name: ModelsOpsMarginUSD :one
-- ModelsOpsMarginUSD 平台口径 USD 收入与成本（service 算毛利率）。
SELECT
    COALESCE((
        SELECT SUM(le.amount)
        FROM ledger_entries le
        WHERE le.entry_type = 'debit' AND le.currency = 'USD'
          AND (sqlc.narg('from_time')::timestamptz IS NULL OR le.created_at >= sqlc.narg('from_time')::timestamptz)
          AND (sqlc.narg('to_time')::timestamptz IS NULL OR le.created_at < sqlc.narg('to_time')::timestamptz)
    ), 0)::numeric AS revenue_usd,
    COALESCE((
        SELECT SUM(cs.total_cost_amount)
        FROM cost_snapshots cs
        WHERE cs.currency = 'USD'
          AND (sqlc.narg('from_time')::timestamptz IS NULL OR cs.created_at >= sqlc.narg('from_time')::timestamptz)
          AND (sqlc.narg('to_time')::timestamptz IS NULL OR cs.created_at < sqlc.narg('to_time')::timestamptz)
    ), 0)::numeric AS cost_usd;

-- name: ModelsOpsTable :many
-- ModelsOpsTable 模型商品运维主表（分页）：静态元数据 + 渠道/基准价；请求/毛利等指标在详情页聚合。
SELECT
    m.id,
    m.model_id,
    m.display_name,
    m.owned_by,
    m.status,
    m.created_at,
    m.max_output_tokens,
    m.context_window_tokens,
    (SELECT COUNT(*) FROM channel_models cm WHERE cm.model_id = m.id AND cm.status = 'enabled') AS bindings_total,
    (
        SELECT COUNT(*)
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id
        WHERE cm.model_id = m.id AND cm.status = 'enabled' AND c.status = 'enabled'
          AND EXISTS (
              SELECT 1 FROM channel_prices p
              WHERE p.channel_id = cm.channel_id AND p.model_id = m.id AND p.status = 'enabled'
          )
    ) AS bindings_available,
    (
        SELECT COUNT(*)
        FROM model_capabilities mc
        WHERE mc.model_id = m.id
          AND mc.support_level IN ('full', 'limited')
    ) AS capabilities_declared_count,
    EXISTS (SELECT 1 FROM channel_prices p WHERE p.model_id = m.id AND p.status = 'enabled') AS has_price,
    -- 基准售价（DEC-026 model_prices 当前生效行）：客户售价 = 基准 × 线路倍率；无基准时各列为 NULL（前端显示「缺价」）。
    -- CASE 包裹让 sqlc 把 base_currency 推断为可空（pgtype.Text）：LATERAL 无命中行时该列为 NULL，避免扫描进 string 报错。
    CASE WHEN base.currency IS NOT NULL THEN base.currency END AS base_currency,
    base.uncached_input_price AS base_uncached_input_price,
    base.cache_read_input_price AS base_cache_read_input_price,
    base.cache_write_5m_input_price AS base_cache_write_5m_input_price,
    base.cache_write_1h_input_price AS base_cache_write_1h_input_price,
    base.cache_write_30m_input_price AS base_cache_write_30m_input_price,
    base.output_price AS base_output_price,
    base.reasoning_output_price AS base_reasoning_output_price
FROM models m
LEFT JOIN LATERAL (
    -- base: 模型当前生效的基准售价（mirror FindRouteCandidates 的 base LATERAL）；LEFT 保证无基准价的模型仍出现在列表。
    SELECT mp.currency, mp.uncached_input_price, mp.cache_read_input_price,
        mp.cache_write_5m_input_price, mp.cache_write_1h_input_price,
        mp.cache_write_30m_input_price,
        mp.output_price, mp.reasoning_output_price
    FROM model_prices mp
    WHERE mp.model_id = m.id
      AND mp.status = 'enabled'
      AND mp.effective_from <= now()
      AND (mp.effective_to IS NULL OR mp.effective_to > now())
    ORDER BY mp.effective_from DESC, mp.id DESC
    LIMIT 1
) base ON TRUE
WHERE (sqlc.narg('status')::text IS NULL OR m.status = sqlc.narg('status')::text)
  AND (sqlc.narg('search')::text IS NULL OR m.model_id ILIKE '%' || sqlc.narg('search')::text || '%' OR m.display_name ILIKE '%' || sqlc.narg('search')::text || '%')
ORDER BY
  CASE WHEN sqlc.narg('sort_field')::text = 'name' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN m.model_id END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'name' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN m.model_id END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'context' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN m.context_window_tokens END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'context' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN m.context_window_tokens END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'max_output' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN m.max_output_tokens END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'max_output' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN m.max_output_tokens END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'bindings' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(*)
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id
        WHERE cm.model_id = m.id AND cm.status = 'enabled' AND c.status = 'enabled'
          AND EXISTS (
              SELECT 1 FROM channel_prices p
              WHERE p.channel_id = cm.channel_id AND p.model_id = m.id AND p.status = 'enabled'
          )
    ) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'bindings' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(*)
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id
        WHERE cm.model_id = m.id AND cm.status = 'enabled' AND c.status = 'enabled'
          AND EXISTS (
              SELECT 1 FROM channel_prices p
              WHERE p.channel_id = cm.channel_id AND p.model_id = m.id AND p.status = 'enabled'
          )
    ) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN m.created_at END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN m.created_at END ASC NULLS LAST,
  m.model_id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ModelsOpsTableCount :one
SELECT COUNT(*) AS total
FROM models m
WHERE (sqlc.narg('status')::text IS NULL OR m.status = sqlc.narg('status')::text)
  AND (sqlc.narg('search')::text IS NULL OR m.model_id ILIKE '%' || sqlc.narg('search')::text || '%' OR m.display_name ILIKE '%' || sqlc.narg('search')::text || '%');

-- name: ModelOpsDetail :one
-- ModelOpsDetail 单模型详情概览：请求/成功率/延迟/token/缓存/TPS/毛利（USD）。
SELECT
    COUNT(r.id) FILTER (WHERE r.status IN ('succeeded', 'failed')) AS request_total,
    COUNT(r.id) FILTER (WHERE r.status = 'succeeded') AS request_succeeded,
    COALESCE(AVG(CASE WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p50,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    COALESCE(SUM(u.output_tokens_total) FILTER (WHERE r.status = 'succeeded'), 0)::bigint AS output_tokens,
    COALESCE(SUM(u.uncached_input_tokens + u.cache_read_input_tokens + u.cache_write_5m_input_tokens + u.cache_write_1h_input_tokens + u.cache_write_30m_input_tokens), 0)::bigint AS input_tokens,
    COALESCE(SUM(u.cache_read_input_tokens), 0)::bigint AS cache_read_tokens,
    COALESCE(SUM(u.cache_write_5m_input_tokens + u.cache_write_1h_input_tokens + u.cache_write_30m_input_tokens), 0)::bigint AS cache_write_tokens,
    COALESCE(SUM(
        CASE WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
             THEN EXTRACT(EPOCH FROM (r.completed_at - COALESCE(r.response_started_at, r.started_at))) END
    ), 0)::float8 AS generation_seconds,
    COALESCE((
        SELECT SUM(le.amount)
        FROM ledger_entries le
        JOIN request_records rr ON rr.id = le.request_record_id
        JOIN models m2 ON m2.model_id = rr.requested_model_id
        WHERE le.entry_type = 'debit' AND le.currency = 'USD' AND m2.id = sqlc.arg('model_id')
          AND (sqlc.narg('from_time')::timestamptz IS NULL OR le.created_at >= sqlc.narg('from_time')::timestamptz)
          AND (sqlc.narg('to_time')::timestamptz IS NULL OR le.created_at < sqlc.narg('to_time')::timestamptz)
    ), 0)::numeric AS revenue_usd,
    COALESCE((
        SELECT SUM(cs.total_cost_amount)
        FROM cost_snapshots cs
        WHERE cs.model_id = sqlc.arg('model_id') AND cs.currency = 'USD'
          AND (sqlc.narg('from_time')::timestamptz IS NULL OR cs.created_at >= sqlc.narg('from_time')::timestamptz)
          AND (sqlc.narg('to_time')::timestamptz IS NULL OR cs.created_at < sqlc.narg('to_time')::timestamptz)
    ), 0)::numeric AS cost_usd,
    (SELECT COUNT(*) FROM channel_models cm WHERE cm.model_id = sqlc.arg('model_id') AND cm.status = 'enabled') AS bindings_total,
    (
        SELECT COUNT(*)
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id
        WHERE cm.model_id = sqlc.arg('model_id') AND cm.status = 'enabled' AND c.status = 'enabled'
          AND EXISTS (
              SELECT 1 FROM channel_prices p
              WHERE p.channel_id = cm.channel_id AND p.model_id = cm.model_id AND p.status = 'enabled'
          )
    ) AS bindings_available,
    (SELECT status FROM models WHERE id = sqlc.arg('model_id')) AS model_status
FROM request_records r
JOIN models m ON m.model_id = r.requested_model_id
LEFT JOIN usage_records u ON u.request_record_id = r.id
WHERE m.id = sqlc.arg('model_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz);

-- name: ModelOpsChannels :many
-- ModelOpsChannels 单模型的承载渠道（绑定）+ attempt 指标（抽屉渠道 Tab，§3.4 最关键）。
SELECT
    c.id AS channel_id,
    c.name AS channel_name,
    c.status AS channel_status,
    cm.status AS binding_status,
    cm.upstream_model,
    c.priority,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream') AS attempt_total,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS attempt_succeeded,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    EXISTS (
        SELECT 1 FROM channel_prices p
        WHERE p.channel_id = c.id AND p.model_id = sqlc.arg('model_id') AND p.status = 'enabled'
    ) AS has_price,
    price.uncached_input_cost AS input_cost,
    price.output_cost AS output_cost
FROM channel_models cm
JOIN channels c ON c.id = cm.channel_id
LEFT JOIN LATERAL (
    SELECT p.uncached_input_cost, p.output_cost
    FROM channel_prices p
    WHERE p.channel_id = c.id
      AND p.model_id = sqlc.arg('model_id')
      AND p.status = 'enabled'
      AND p.effective_from <= now()
      AND (p.effective_to IS NULL OR p.effective_to > now())
    ORDER BY p.effective_from DESC, p.id DESC
    LIMIT 1
) price ON TRUE
LEFT JOIN request_attempts a
    ON a.channel_id = cm.channel_id
    AND a.upstream_model = cm.upstream_model
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
WHERE cm.model_id = sqlc.arg('model_id')
GROUP BY c.id, c.name, c.status, cm.status, cm.upstream_model, c.priority,
    price.uncached_input_cost, price.output_cost
ORDER BY attempt_total DESC, c.priority, c.id;

-- name: ModelOpsPerformanceTimeseries :many
SELECT
    date_trunc(sqlc.arg('unit')::text, r.created_at)::timestamptz AS bucket,
    COUNT(*) FILTER (WHERE r.status IN ('succeeded', 'failed')) AS request_total,
    COUNT(*) FILTER (WHERE r.status = 'succeeded') AS request_succeeded,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95
FROM request_records r
JOIN models m ON m.model_id = r.requested_model_id
WHERE m.id = sqlc.arg('model_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY bucket
ORDER BY bucket;

-- name: ModelOpsRequests :many
-- ModelOpsRequests 单模型最近请求（抽屉请求 Tab，分页）。
SELECT
    r.request_id,
    r.created_at,
    r.status,
    r.error_code,
    r.final_channel_id,
    CASE WHEN r.completed_at IS NOT NULL THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8 END AS latency_ms
FROM request_records r
JOIN models m ON m.model_id = r.requested_model_id
WHERE m.id = sqlc.arg('model_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY r.created_at DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ModelOpsRequestsCount :one
SELECT COUNT(*) AS total
FROM request_records r
JOIN models m ON m.model_id = r.requested_model_id
WHERE m.id = sqlc.arg('model_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz);
