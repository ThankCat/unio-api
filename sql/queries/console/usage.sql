-- Console 用量统计：只查当前用户、且账本 USD 净扣费大于 0 的请求。
-- 口径与 console/requests.sql 一致，只补两件请求中心做不到的事：
-- 按时间分桶的趋势，以及按模型/密钥的分组排行。
--
-- 账本一律用 JOIN LATERAL + idx_ledger_entries_request_record_id 做索引点查，
-- 不要改回「charges CTE 再 JOIN windowed」：两个 CTE 的行数在 generic plan 下都会被
-- 估成几十行，优化器随即选嵌套循环，1 万行请求会退化成上亿次比较（实测 8.5s）。
-- LATERAL 的代价是固定的 N 次索引查找（1 万行约 13ms），不受参数估算影响。

-- name: ListConsoleUsageTimeseries :many
-- 按 bucket（minute/hour/day/week/month/quarter/year）分桶的用量与费用。
-- from_time/to_time 是实际数据统计窗；series_from/series_to 是需要补齐的展示窗。
-- 空桶由 generate_series 补齐，允许请求卡片展示尚未到达的未来桶。
WITH windowed AS MATERIALIZED (
    SELECT
        r.id,
        r.created_at,
        r.stream,
        r.started_at,
        r.completed_at,
        r.requested_model_id,
        r.api_key_id,
        r.endpoint,
        r.request_id,
        r.client_ip
    FROM request_records r
    WHERE r.user_id = sqlc.arg(user_id)
      AND r.created_at >= sqlc.arg(from_time)::timestamptz
      AND r.created_at < sqlc.arg(to_time)::timestamptz
),
billed AS MATERIALIZED (
    SELECT
        (
            date_trunc(
                sqlc.arg(bucket)::text,
                w.created_at AT TIME ZONE sqlc.arg(tz)::text
            ) AT TIME ZONE sqlc.arg(tz)::text
        ) AS bucket_start,
        w.started_at,
        w.completed_at,
        ch.charge_usd,
        COALESCE(ur.uncached_input_tokens, 0) AS uncached_input_tokens,
        COALESCE(ur.cache_read_input_tokens, 0) AS cache_read_input_tokens,
        (
            COALESCE(ur.cache_creation_5m_input_tokens, 0)
            + COALESCE(ur.cache_creation_1h_input_tokens, 0)
            + COALESCE(ur.cache_creation_30m_input_tokens, 0)
        ) AS cache_creation_input_tokens,
        COALESCE(ur.output_tokens_total, 0) AS output_tokens,
        COALESCE(ur.uncached_input_tokens, 0)::numeric
            * COALESCE(ps.uncached_input_price, 0) / 1000000 AS uncached_input_charge_usd,
        (
            GREATEST(
                COALESCE(ur.output_tokens_total, 0) - COALESCE(ur.reasoning_output_tokens, 0),
                0
            )::numeric * COALESCE(ps.output_price, 0)
            + COALESCE(ur.reasoning_output_tokens, 0)::numeric
                * COALESCE(ps.reasoning_output_price, ps.output_price, 0)
        ) / 1000000 AS output_charge_usd,
        COALESCE(ur.cache_read_input_tokens, 0)::numeric
            * COALESCE(ps.cache_read_input_price, ps.uncached_input_price, 0)
            / 1000000 AS cache_read_charge_usd,
        (
            COALESCE(ur.cache_creation_5m_input_tokens, 0)::numeric
                * COALESCE(ps.cache_creation_5m_input_price, ps.uncached_input_price, 0)
            + COALESCE(ur.cache_creation_1h_input_tokens, 0)::numeric
                * COALESCE(ps.cache_creation_1h_input_price, ps.uncached_input_price, 0)
            + COALESCE(ur.cache_creation_30m_input_tokens, 0)::numeric
                * COALESCE(ps.cache_creation_30m_input_price, ps.uncached_input_price, 0)
        ) / 1000000 AS cache_creation_charge_usd,
        -- 缓存读若按未缓存价计费要多花多少，再减去缓存创建相对未缓存价的溢价。
        (
            COALESCE(ur.cache_read_input_tokens, 0)::numeric
                * (
                    COALESCE(ps.uncached_input_price, 0)
                    - COALESCE(ps.cache_read_input_price, ps.uncached_input_price, 0)
                )
            - (
                COALESCE(ur.cache_creation_5m_input_tokens, 0)::numeric
                    * (
                        COALESCE(ps.cache_creation_5m_input_price, ps.uncached_input_price, 0)
                        - COALESCE(ps.uncached_input_price, 0)
                    )
                + COALESCE(ur.cache_creation_1h_input_tokens, 0)::numeric
                    * (
                        COALESCE(ps.cache_creation_1h_input_price, ps.uncached_input_price, 0)
                        - COALESCE(ps.uncached_input_price, 0)
                    )
                + COALESCE(ur.cache_creation_30m_input_tokens, 0)::numeric
                    * (
                        COALESCE(ps.cache_creation_30m_input_price, ps.uncached_input_price, 0)
                        - COALESCE(ps.uncached_input_price, 0)
                    )
            )
        ) / 1000000 AS cache_saved_usd
    FROM windowed w
    JOIN usage_records ur ON ur.request_record_id = w.id
    LEFT JOIN price_snapshots ps ON ps.request_record_id = w.id
    LEFT JOIN api_keys ak ON ak.id = w.api_key_id
    LEFT JOIN models m ON m.model_id = w.requested_model_id
    JOIN LATERAL (
        SELECT SUM(
            CASE
                WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
                WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
                ELSE 0
            END
        ) AS charge_usd
        FROM ledger_entries le
        WHERE le.request_record_id = w.id AND le.currency = 'USD'
    ) ch ON ch.charge_usd > 0
    WHERE (
          COALESCE(cardinality(sqlc.narg(api_key_ids)::bigint[]), 0) = 0
          OR w.api_key_id = ANY(sqlc.narg(api_key_ids)::bigint[])
      )
      AND (
          COALESCE(cardinality(sqlc.narg(model_ids)::text[]), 0) = 0
          OR w.requested_model_id = ANY(sqlc.narg(model_ids)::text[])
      )
      AND (
          COALESCE(cardinality(sqlc.narg(endpoints)::text[]), 0) = 0
          OR w.endpoint = ANY(sqlc.narg(endpoints)::text[])
      )
      AND (
          COALESCE(cardinality(sqlc.narg(stream_types)::text[]), 0) = 0
          OR (w.stream AND 'stream' = ANY(sqlc.narg(stream_types)::text[]))
          OR ((NOT w.stream) AND 'sync' = ANY(sqlc.narg(stream_types)::text[]))
      )
      AND (
          sqlc.narg(q)::text IS NULL
          OR btrim(sqlc.narg(q)::text) = ''
          OR w.requested_model_id ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
          OR COALESCE(m.display_name, '') ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
          OR w.request_id ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
          OR COALESCE(w.client_ip, '') ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
      )
),
grouped AS (
    SELECT
        b.bucket_start,
        COUNT(*)::bigint AS request_count,
        SUM(
            b.uncached_input_tokens + b.cache_read_input_tokens
            + b.cache_creation_input_tokens + b.output_tokens
        )::bigint AS token_count,
        SUM(b.uncached_input_tokens)::bigint AS uncached_input_token_count,
        SUM(b.cache_read_input_tokens)::bigint AS cache_read_token_count,
        SUM(b.cache_creation_input_tokens)::bigint AS cache_creation_token_count,
        SUM(b.output_tokens)::bigint AS output_token_count,
        SUM(b.charge_usd)::numeric AS charge_usd,
        SUM(b.uncached_input_charge_usd)::numeric AS uncached_input_charge_usd,
        SUM(b.output_charge_usd)::numeric AS output_charge_usd,
        SUM(b.cache_read_charge_usd)::numeric AS cache_read_charge_usd,
        SUM(b.cache_creation_charge_usd)::numeric AS cache_creation_charge_usd,
        SUM(b.cache_saved_usd)::numeric AS cache_saved_usd,
        -- 请求中心的耗时卡也要按桶出热力条，顺带在这里算掉，省一次全表扫描。
        COALESCE(
            AVG(EXTRACT(EPOCH FROM (b.completed_at - b.started_at)) * 1000)
                FILTER (WHERE b.completed_at IS NOT NULL AND b.started_at IS NOT NULL),
            0
        )::float8 AS average_latency_ms
    FROM billed b
    GROUP BY b.bucket_start
),
bucket_starts AS (
    SELECT series.local_start
    FROM generate_series(
        date_trunc(
            sqlc.arg(bucket)::text,
            sqlc.arg(series_from)::timestamptz AT TIME ZONE sqlc.arg(tz)::text
        ),
        date_trunc(
            sqlc.arg(bucket)::text,
            sqlc.arg(series_to)::timestamptz AT TIME ZONE sqlc.arg(tz)::text
        ),
        CASE sqlc.arg(bucket)::text
            WHEN 'minute' THEN interval '1 minute'
            WHEN 'hour' THEN interval '1 hour'
            WHEN 'day' THEN interval '1 day'
            WHEN 'week' THEN interval '1 week'
            WHEN 'month' THEN interval '1 month'
            WHEN 'quarter' THEN interval '3 months'
            WHEN 'year' THEN interval '1 year'
            ELSE interval '1 day'
        END
    ) AS series(local_start)
    WHERE series.local_start < (
        sqlc.arg(series_to)::timestamptz AT TIME ZONE sqlc.arg(tz)::text
    )
),
buckets AS (
    SELECT
        local_start,
        local_start + CASE sqlc.arg(bucket)::text
            WHEN 'minute' THEN interval '1 minute'
            WHEN 'hour' THEN interval '1 hour'
            WHEN 'day' THEN interval '1 day'
            WHEN 'week' THEN interval '1 week'
            WHEN 'month' THEN interval '1 month'
            WHEN 'quarter' THEN interval '3 months'
            WHEN 'year' THEN interval '1 year'
            ELSE interval '1 day'
        END AS local_end
    FROM bucket_starts
)
SELECT
    (bk.local_start AT TIME ZONE sqlc.arg(tz)::text)::timestamptz AS bucket_start,
    (bk.local_end AT TIME ZONE sqlc.arg(tz)::text)::timestamptz AS bucket_end,
    COALESCE(g.request_count, 0)::bigint AS request_count,
    COALESCE(g.token_count, 0)::bigint AS token_count,
    COALESCE(g.uncached_input_token_count, 0)::bigint AS uncached_input_token_count,
    COALESCE(g.cache_read_token_count, 0)::bigint AS cache_read_token_count,
    COALESCE(g.cache_creation_token_count, 0)::bigint AS cache_creation_token_count,
    COALESCE(g.output_token_count, 0)::bigint AS output_token_count,
    COALESCE(g.charge_usd, 0)::numeric AS charge_usd,
    COALESCE(g.uncached_input_charge_usd, 0)::numeric AS uncached_input_charge_usd,
    COALESCE(g.output_charge_usd, 0)::numeric AS output_charge_usd,
    COALESCE(g.cache_read_charge_usd, 0)::numeric AS cache_read_charge_usd,
    COALESCE(g.cache_creation_charge_usd, 0)::numeric AS cache_creation_charge_usd,
    COALESCE(g.cache_saved_usd, 0)::numeric AS cache_saved_usd,
    COALESCE(g.average_latency_ms, 0)::float8 AS average_latency_ms
FROM buckets bk
LEFT JOIN grouped g
    ON g.bucket_start = (bk.local_start AT TIME ZONE sqlc.arg(tz)::text)::timestamptz
ORDER BY bucket_start;

-- name: SummarizeConsoleUsageWindow :one
-- 单个时间窗的用量汇总，供卡片当期/上期各取一次。
-- 只算用量与费用，不含耗时分位数：耗时归请求中心，这里不重复付出 percentile 的代价。
WITH windowed AS MATERIALIZED (
    SELECT
        r.id,
        r.stream,
        r.requested_model_id,
        r.api_key_id,
        r.endpoint,
        r.request_id,
        r.client_ip
    FROM request_records r
    WHERE r.user_id = sqlc.arg(user_id)
      AND (sqlc.narg(from_time)::timestamptz IS NULL OR r.created_at >= sqlc.narg(from_time)::timestamptz)
      AND (sqlc.narg(to_time)::timestamptz IS NULL OR r.created_at < sqlc.narg(to_time)::timestamptz)
)
SELECT
    COUNT(*)::bigint AS request_count,
    COALESCE(SUM(
        COALESCE(ur.uncached_input_tokens, 0)
        + COALESCE(ur.cache_read_input_tokens, 0)
        + COALESCE(ur.cache_creation_5m_input_tokens, 0)
        + COALESCE(ur.cache_creation_1h_input_tokens, 0)
        + COALESCE(ur.cache_creation_30m_input_tokens, 0)
        + COALESCE(ur.output_tokens_total, 0)
    ), 0)::bigint AS token_count,
    COALESCE(SUM(COALESCE(ur.uncached_input_tokens, 0)), 0)::bigint AS uncached_input_token_count,
    COALESCE(SUM(COALESCE(ur.cache_read_input_tokens, 0)), 0)::bigint AS cache_read_token_count,
    COALESCE(SUM(
        COALESCE(ur.cache_creation_5m_input_tokens, 0)
        + COALESCE(ur.cache_creation_1h_input_tokens, 0)
        + COALESCE(ur.cache_creation_30m_input_tokens, 0)
    ), 0)::bigint AS cache_creation_token_count,
    COALESCE(SUM(COALESCE(ur.output_tokens_total, 0)), 0)::bigint AS output_token_count,
    COALESCE(SUM(ch.charge_usd), 0)::numeric AS charge_usd,
    COALESCE(SUM(
        COALESCE(ur.uncached_input_tokens, 0)::numeric
        * COALESCE(ps.uncached_input_price, 0) / 1000000
    ), 0)::numeric AS uncached_input_charge_usd,
    COALESCE(SUM(
        GREATEST(
            COALESCE(ur.output_tokens_total, 0) - COALESCE(ur.reasoning_output_tokens, 0),
            0
        )::numeric * COALESCE(ps.output_price, 0) / 1000000
        + COALESCE(ur.reasoning_output_tokens, 0)::numeric
            * COALESCE(ps.reasoning_output_price, ps.output_price, 0) / 1000000
    ), 0)::numeric AS output_charge_usd,
    COALESCE(SUM(
        COALESCE(ur.cache_read_input_tokens, 0)::numeric
        * COALESCE(ps.cache_read_input_price, ps.uncached_input_price, 0) / 1000000
    ), 0)::numeric AS cache_read_charge_usd,
    COALESCE(SUM(
        COALESCE(ur.cache_creation_5m_input_tokens, 0)::numeric
            * COALESCE(ps.cache_creation_5m_input_price, ps.uncached_input_price, 0) / 1000000
        + COALESCE(ur.cache_creation_1h_input_tokens, 0)::numeric
            * COALESCE(ps.cache_creation_1h_input_price, ps.uncached_input_price, 0) / 1000000
        + COALESCE(ur.cache_creation_30m_input_tokens, 0)::numeric
            * COALESCE(ps.cache_creation_30m_input_price, ps.uncached_input_price, 0) / 1000000
    ), 0)::numeric AS cache_creation_charge_usd,
    COALESCE(SUM(
        (
            COALESCE(ur.uncached_input_tokens, 0)::numeric
                * COALESCE(ps.uncached_input_price, 0)
            + GREATEST(
                COALESCE(ur.output_tokens_total, 0) - COALESCE(ur.reasoning_output_tokens, 0),
                0
            )::numeric * COALESCE(ps.output_price, 0)
            + COALESCE(ur.reasoning_output_tokens, 0)::numeric
                * COALESCE(ps.reasoning_output_price, ps.output_price, 0)
            + COALESCE(ur.cache_read_input_tokens, 0)::numeric
                * COALESCE(ps.cache_read_input_price, ps.uncached_input_price, 0)
            + COALESCE(ur.cache_creation_5m_input_tokens, 0)::numeric
                * COALESCE(ps.cache_creation_5m_input_price, ps.uncached_input_price, 0)
            + COALESCE(ur.cache_creation_1h_input_tokens, 0)::numeric
                * COALESCE(ps.cache_creation_1h_input_price, ps.uncached_input_price, 0)
            + COALESCE(ur.cache_creation_30m_input_tokens, 0)::numeric
                * COALESCE(ps.cache_creation_30m_input_price, ps.uncached_input_price, 0)
        ) / 1000000
        / COALESCE(NULLIF(ps.sale_discount, 0), 1)
    ), 0)::numeric AS list_charge_usd,
    COALESCE(SUM(
        (
            COALESCE(ur.cache_read_input_tokens, 0)::numeric
                * (
                    COALESCE(ps.uncached_input_price, 0)
                    - COALESCE(ps.cache_read_input_price, ps.uncached_input_price, 0)
                )
            - (
                COALESCE(ur.cache_creation_5m_input_tokens, 0)::numeric
                    * (
                        COALESCE(ps.cache_creation_5m_input_price, ps.uncached_input_price, 0)
                        - COALESCE(ps.uncached_input_price, 0)
                    )
                + COALESCE(ur.cache_creation_1h_input_tokens, 0)::numeric
                    * (
                        COALESCE(ps.cache_creation_1h_input_price, ps.uncached_input_price, 0)
                        - COALESCE(ps.uncached_input_price, 0)
                    )
                + COALESCE(ur.cache_creation_30m_input_tokens, 0)::numeric
                    * (
                        COALESCE(ps.cache_creation_30m_input_price, ps.uncached_input_price, 0)
                        - COALESCE(ps.uncached_input_price, 0)
                    )
            )
        ) / 1000000
    ), 0)::numeric AS cache_saved_usd
FROM windowed w
JOIN usage_records ur ON ur.request_record_id = w.id
LEFT JOIN price_snapshots ps ON ps.request_record_id = w.id
LEFT JOIN api_keys ak ON ak.id = w.api_key_id
LEFT JOIN models m ON m.model_id = w.requested_model_id
JOIN LATERAL (
    SELECT SUM(
        CASE
            WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
            WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
            ELSE 0
        END
    ) AS charge_usd
    FROM ledger_entries le
    WHERE le.request_record_id = w.id AND le.currency = 'USD'
) ch ON ch.charge_usd > 0
WHERE (
      COALESCE(cardinality(sqlc.narg(api_key_ids)::bigint[]), 0) = 0
      OR w.api_key_id = ANY(sqlc.narg(api_key_ids)::bigint[])
  )
  AND (
      COALESCE(cardinality(sqlc.narg(model_ids)::text[]), 0) = 0
      OR w.requested_model_id = ANY(sqlc.narg(model_ids)::text[])
  )
  AND (
      COALESCE(cardinality(sqlc.narg(endpoints)::text[]), 0) = 0
      OR w.endpoint = ANY(sqlc.narg(endpoints)::text[])
  )
  AND (
      COALESCE(cardinality(sqlc.narg(stream_types)::text[]), 0) = 0
      OR (w.stream AND 'stream' = ANY(sqlc.narg(stream_types)::text[]))
      OR ((NOT w.stream) AND 'sync' = ANY(sqlc.narg(stream_types)::text[]))
  )
  AND (
      sqlc.narg(q)::text IS NULL
      OR btrim(sqlc.narg(q)::text) = ''
      OR w.requested_model_id ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
      OR COALESCE(m.display_name, '') ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
      OR w.request_id ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
      OR COALESCE(w.client_ip, '') ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
  );

-- name: ListConsoleUsageByModel :many
-- 明细排行：按请求模型分组，按消费降序。协议与单价取该模型最近一条扣费请求。
WITH windowed AS MATERIALIZED (
    SELECT
        r.id,
        r.created_at,
        r.stream,
        r.requested_model_id,
        r.ingress_protocol,
        r.api_key_id,
        r.endpoint,
        r.request_id,
        r.client_ip
    FROM request_records r
    WHERE r.user_id = sqlc.arg(user_id)
      AND (sqlc.narg(from_time)::timestamptz IS NULL OR r.created_at >= sqlc.narg(from_time)::timestamptz)
      AND (sqlc.narg(to_time)::timestamptz IS NULL OR r.created_at < sqlc.narg(to_time)::timestamptz)
),
billed AS MATERIALIZED (
    SELECT
        w.id,
        w.created_at,
        w.requested_model_id,
        w.ingress_protocol,
        ch.charge_usd,
        (
            COALESCE(ur.uncached_input_tokens, 0)
            + COALESCE(ur.cache_read_input_tokens, 0)
            + COALESCE(ur.cache_creation_5m_input_tokens, 0)
            + COALESCE(ur.cache_creation_1h_input_tokens, 0)
            + COALESCE(ur.cache_creation_30m_input_tokens, 0)
            + COALESCE(ur.output_tokens_total, 0)
        ) AS total_tokens
    FROM windowed w
    JOIN usage_records ur ON ur.request_record_id = w.id
    LEFT JOIN api_keys ak ON ak.id = w.api_key_id
    LEFT JOIN models m ON m.model_id = w.requested_model_id
    JOIN LATERAL (
        SELECT SUM(
            CASE
                WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
                WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
                ELSE 0
            END
        ) AS charge_usd
        FROM ledger_entries le
        WHERE le.request_record_id = w.id AND le.currency = 'USD'
    ) ch ON ch.charge_usd > 0
    WHERE (
          COALESCE(cardinality(sqlc.narg(api_key_ids)::bigint[]), 0) = 0
          OR w.api_key_id = ANY(sqlc.narg(api_key_ids)::bigint[])
      )
      AND (
          COALESCE(cardinality(sqlc.narg(model_ids)::text[]), 0) = 0
          OR w.requested_model_id = ANY(sqlc.narg(model_ids)::text[])
      )
      AND (
          COALESCE(cardinality(sqlc.narg(endpoints)::text[]), 0) = 0
          OR w.endpoint = ANY(sqlc.narg(endpoints)::text[])
      )
      AND (
          COALESCE(cardinality(sqlc.narg(stream_types)::text[]), 0) = 0
          OR (w.stream AND 'stream' = ANY(sqlc.narg(stream_types)::text[]))
          OR ((NOT w.stream) AND 'sync' = ANY(sqlc.narg(stream_types)::text[]))
      )
      AND (
          sqlc.narg(q)::text IS NULL
          OR btrim(sqlc.narg(q)::text) = ''
          OR w.requested_model_id ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
          OR COALESCE(m.display_name, '') ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
          OR w.request_id ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
          OR COALESCE(w.client_ip, '') ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
      )
),
totals AS (
    SELECT
        requested_model_id,
        COUNT(*)::bigint AS request_count,
        SUM(total_tokens)::bigint AS token_count,
        SUM(charge_usd)::numeric AS charge_usd
    FROM billed
    GROUP BY requested_model_id
),
latest AS (
    SELECT DISTINCT ON (b.requested_model_id)
        b.requested_model_id,
        b.ingress_protocol,
        ps.uncached_input_price,
        ps.output_price
    FROM billed b
    LEFT JOIN price_snapshots ps ON ps.request_record_id = b.id
    ORDER BY b.requested_model_id, b.created_at DESC, b.id DESC
)
SELECT
    t.requested_model_id AS group_id,
    COALESCE(NULLIF(m.display_name, ''), t.requested_model_id) AS group_name,
    t.request_count,
    t.token_count,
    t.charge_usd,
    COALESCE(l.ingress_protocol, '') AS ingress_protocol,
    l.uncached_input_price AS input_price_per_1m,
    l.output_price AS output_price_per_1m
FROM totals t
LEFT JOIN models m ON m.model_id = t.requested_model_id
LEFT JOIN latest l ON l.requested_model_id = t.requested_model_id
ORDER BY t.charge_usd DESC, t.requested_model_id ASC
LIMIT sqlc.arg(row_limit);

-- name: ListConsoleUsageByAPIKey :many
-- 明细排行：按 API 密钥分组，按消费降序。
WITH windowed AS MATERIALIZED (
    SELECT
        r.id,
        r.stream,
        r.requested_model_id,
        r.api_key_id,
        r.endpoint,
        r.request_id,
        r.client_ip
    FROM request_records r
    WHERE r.user_id = sqlc.arg(user_id)
      AND (sqlc.narg(from_time)::timestamptz IS NULL OR r.created_at >= sqlc.narg(from_time)::timestamptz)
      AND (sqlc.narg(to_time)::timestamptz IS NULL OR r.created_at < sqlc.narg(to_time)::timestamptz)
)
SELECT
    w.api_key_id AS group_id,
    COALESCE(NULLIF(ak.name, ''), ak.key_prefix, '') AS group_name,
    COUNT(*)::bigint AS request_count,
    SUM(
        COALESCE(ur.uncached_input_tokens, 0)
        + COALESCE(ur.cache_read_input_tokens, 0)
        + COALESCE(ur.cache_creation_5m_input_tokens, 0)
        + COALESCE(ur.cache_creation_1h_input_tokens, 0)
        + COALESCE(ur.cache_creation_30m_input_tokens, 0)
        + COALESCE(ur.output_tokens_total, 0)
    )::bigint AS token_count,
    SUM(ch.charge_usd)::numeric AS charge_usd
FROM windowed w
JOIN usage_records ur ON ur.request_record_id = w.id
LEFT JOIN api_keys ak ON ak.id = w.api_key_id
LEFT JOIN models m ON m.model_id = w.requested_model_id
JOIN LATERAL (
    SELECT SUM(
        CASE
            WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
            WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
            ELSE 0
        END
    ) AS charge_usd
    FROM ledger_entries le
    WHERE le.request_record_id = w.id AND le.currency = 'USD'
) ch ON ch.charge_usd > 0
WHERE (
      COALESCE(cardinality(sqlc.narg(api_key_ids)::bigint[]), 0) = 0
      OR w.api_key_id = ANY(sqlc.narg(api_key_ids)::bigint[])
  )
  AND (
      COALESCE(cardinality(sqlc.narg(model_ids)::text[]), 0) = 0
      OR w.requested_model_id = ANY(sqlc.narg(model_ids)::text[])
  )
  AND (
      COALESCE(cardinality(sqlc.narg(endpoints)::text[]), 0) = 0
      OR w.endpoint = ANY(sqlc.narg(endpoints)::text[])
  )
  AND (
      COALESCE(cardinality(sqlc.narg(stream_types)::text[]), 0) = 0
      OR (w.stream AND 'stream' = ANY(sqlc.narg(stream_types)::text[]))
      OR ((NOT w.stream) AND 'sync' = ANY(sqlc.narg(stream_types)::text[]))
  )
  AND (
      sqlc.narg(q)::text IS NULL
      OR btrim(sqlc.narg(q)::text) = ''
      OR w.requested_model_id ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
      OR COALESCE(m.display_name, '') ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
      OR w.request_id ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
      OR COALESCE(w.client_ip, '') ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
  )
GROUP BY w.api_key_id, COALESCE(NULLIF(ak.name, ''), ak.key_prefix, '')
ORDER BY charge_usd DESC, group_id ASC
LIMIT sqlc.arg(row_limit);

-- name: ListConsoleUsageTrendByGroup :many
-- 按「时间桶 × 分组」的二维聚合，给趋势图做堆叠。
-- dimension 取 model / api_key；窗口内消费排前 top_n 的分组各占一段，
-- 其余合并成 __other__ —— 趋势图上十几种颜色没法读，合并的阈值放在 SQL 里，
-- 免得前端把上百个分组的整段序列都拉过去再丢掉。
-- 空桶不在这里补：分组维度下补空桶会产生 桶数 × 分组数 行，交给 service 层按桶对齐。
WITH windowed AS MATERIALIZED (
    SELECT
        r.id,
        r.created_at,
        r.stream,
        r.requested_model_id,
        r.api_key_id,
        r.endpoint,
        r.request_id,
        r.client_ip
    FROM request_records r
    WHERE r.user_id = sqlc.arg(user_id)
      AND r.created_at >= sqlc.arg(from_time)::timestamptz
      AND r.created_at < sqlc.arg(to_time)::timestamptz
),
billed AS MATERIALIZED (
    SELECT
        (
            date_trunc(
                sqlc.arg(bucket)::text,
                w.created_at AT TIME ZONE sqlc.arg(tz)::text
            ) AT TIME ZONE sqlc.arg(tz)::text
        ) AS bucket_start,
        CASE sqlc.arg(dimension)::text
            WHEN 'api_key' THEN COALESCE(w.api_key_id::text, '')
            ELSE w.requested_model_id
        END AS group_id,
        CASE sqlc.arg(dimension)::text
            WHEN 'api_key' THEN COALESCE(NULLIF(ak.name, ''), w.api_key_id::text, '')
            ELSE COALESCE(NULLIF(m.display_name, ''), w.requested_model_id)
        END AS group_name,
        ch.charge_usd,
        (
            COALESCE(ur.uncached_input_tokens, 0)
            + COALESCE(ur.cache_read_input_tokens, 0)
            + COALESCE(ur.cache_creation_5m_input_tokens, 0)
            + COALESCE(ur.cache_creation_1h_input_tokens, 0)
            + COALESCE(ur.cache_creation_30m_input_tokens, 0)
            + COALESCE(ur.output_tokens_total, 0)
        ) AS total_tokens
    FROM windowed w
    JOIN usage_records ur ON ur.request_record_id = w.id
    LEFT JOIN api_keys ak ON ak.id = w.api_key_id
    LEFT JOIN models m ON m.model_id = w.requested_model_id
    JOIN LATERAL (
        SELECT SUM(
            CASE
                WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
                WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
                ELSE 0
            END
        ) AS charge_usd
        FROM ledger_entries le
        WHERE le.request_record_id = w.id AND le.currency = 'USD'
    ) ch ON ch.charge_usd > 0
    WHERE (
          COALESCE(cardinality(sqlc.narg(api_key_ids)::bigint[]), 0) = 0
          OR w.api_key_id = ANY(sqlc.narg(api_key_ids)::bigint[])
      )
      AND (
          COALESCE(cardinality(sqlc.narg(model_ids)::text[]), 0) = 0
          OR w.requested_model_id = ANY(sqlc.narg(model_ids)::text[])
      )
      AND (
          COALESCE(cardinality(sqlc.narg(endpoints)::text[]), 0) = 0
          OR w.endpoint = ANY(sqlc.narg(endpoints)::text[])
      )
      AND (
          COALESCE(cardinality(sqlc.narg(stream_types)::text[]), 0) = 0
          OR (w.stream AND 'stream' = ANY(sqlc.narg(stream_types)::text[]))
          OR ((NOT w.stream) AND 'sync' = ANY(sqlc.narg(stream_types)::text[]))
      )
),
ranked AS (
    SELECT b.group_id
    FROM billed b
    GROUP BY b.group_id
    ORDER BY SUM(b.charge_usd) DESC, b.group_id ASC
    LIMIT sqlc.arg(top_n)
),
folded AS (
    SELECT
        b.bucket_start,
        CASE WHEN r.group_id IS NULL THEN '__other__' ELSE b.group_id END AS group_id,
        CASE WHEN r.group_id IS NULL THEN '__other__' ELSE b.group_name END AS group_name,
        b.charge_usd,
        b.total_tokens
    FROM billed b
    LEFT JOIN ranked r ON r.group_id = b.group_id
)
SELECT
    f.bucket_start::timestamptz AS bucket_start,
    f.group_id::text AS group_id,
    -- 同一分组在窗口内可能改过名（密钥改名、模型换 display_name），取其一即可。
    MIN(f.group_name)::text AS group_name,
    COUNT(*)::bigint AS request_count,
    SUM(f.total_tokens)::bigint AS token_count,
    SUM(f.charge_usd)::numeric AS charge_usd
FROM folded f
GROUP BY f.bucket_start, f.group_id
ORDER BY f.bucket_start, SUM(f.charge_usd) DESC, f.group_id;

-- name: ListConsoleUsageFilterModels :many
-- 模型筛选项：当前用户实际扣费请求上出现过的模型。
SELECT DISTINCT
    r.requested_model_id AS model_id,
    COALESCE(NULLIF(m.display_name, ''), r.requested_model_id) AS display_name
FROM request_records r
JOIN usage_records ur ON ur.request_record_id = r.id
LEFT JOIN models m ON m.model_id = r.requested_model_id
JOIN LATERAL (
    SELECT SUM(
        CASE
            WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
            WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
            ELSE 0
        END
    ) AS charge_usd
    FROM ledger_entries le
    WHERE le.request_record_id = r.id AND le.currency = 'USD'
) ch ON ch.charge_usd > 0
WHERE r.user_id = sqlc.arg(user_id)
ORDER BY display_name, model_id;
