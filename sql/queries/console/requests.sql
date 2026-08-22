-- Console 客户请求日志：只查当前用户、且账本 USD 净扣费大于 0 的请求。
-- 不返回状态、渠道、服务商、平台成本、密钥明文或内部错误。

-- name: ListConsoleBilledRequests :many
-- 先过滤分页，再只对当前页 JOIN 展示字段。
WITH filtered_page AS (
    SELECT
        r.id,
        COUNT(*) OVER()::bigint AS total_count
    FROM request_records r
    JOIN usage_records ur ON ur.request_record_id = r.id
    LEFT JOIN api_keys ak ON ak.id = r.api_key_id
    LEFT JOIN models m ON m.model_id = r.requested_model_id
    WHERE r.user_id = sqlc.arg(user_id)
      AND (
          SELECT COALESCE(SUM(
              CASE
                  WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
                  WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
                  ELSE 0
              END
          ), 0)
          FROM ledger_entries le
          WHERE le.request_record_id = r.id AND le.currency = 'USD'
      ) > 0
      AND (
          COALESCE(cardinality(sqlc.narg(route_ids)::bigint[]), 0) = 0
          OR COALESCE(r.route_id, ak.route_id) = ANY(sqlc.narg(route_ids)::bigint[])
      )
      AND (
          COALESCE(cardinality(sqlc.narg(api_key_ids)::bigint[]), 0) = 0
          OR r.api_key_id = ANY(sqlc.narg(api_key_ids)::bigint[])
      )
      AND (
          COALESCE(cardinality(sqlc.narg(endpoints)::text[]), 0) = 0
          OR r.endpoint = ANY(sqlc.narg(endpoints)::text[])
      )
      AND (
          COALESCE(cardinality(sqlc.narg(stream_types)::text[]), 0) = 0
          OR (r.stream AND 'stream' = ANY(sqlc.narg(stream_types)::text[]))
          OR ((NOT r.stream) AND 'sync' = ANY(sqlc.narg(stream_types)::text[]))
      )
      AND (
          sqlc.narg(q)::text IS NULL
          OR btrim(sqlc.narg(q)::text) = ''
          OR r.requested_model_id ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
          OR COALESCE(m.display_name, '') ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
          OR r.request_id ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
          OR COALESCE(r.client_ip, '') ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
      )
      AND (sqlc.narg(from_time)::timestamptz IS NULL OR r.created_at >= sqlc.narg(from_time)::timestamptz)
      AND (sqlc.narg(to_time)::timestamptz IS NULL OR r.created_at < sqlc.narg(to_time)::timestamptz)
    ORDER BY
      CASE WHEN COALESCE(sqlc.narg(sort_field)::text, 'created_at') IN ('', 'created_at') AND COALESCE(sqlc.narg(sort_desc)::bool, true) THEN r.created_at END DESC NULLS LAST,
      CASE WHEN COALESCE(sqlc.narg(sort_field)::text, 'created_at') IN ('', 'created_at') AND NOT COALESCE(sqlc.narg(sort_desc)::bool, true) THEN r.created_at END ASC NULLS LAST,
      CASE WHEN sqlc.narg(sort_field)::text = 'model' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN COALESCE(m.display_name, r.requested_model_id) END DESC NULLS LAST,
      CASE WHEN sqlc.narg(sort_field)::text = 'model' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN COALESCE(m.display_name, r.requested_model_id) END ASC NULLS LAST,
      CASE WHEN sqlc.narg(sort_field)::text = 'reasoning' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN r.reasoning_effort END DESC NULLS LAST,
      CASE WHEN sqlc.narg(sort_field)::text = 'reasoning' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN r.reasoning_effort END ASC NULLS LAST,
      CASE WHEN sqlc.narg(sort_field)::text = 'stream' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN r.stream END DESC NULLS LAST,
      CASE WHEN sqlc.narg(sort_field)::text = 'stream' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN r.stream END ASC NULLS LAST,
      CASE WHEN sqlc.narg(sort_field)::text = 'latency' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) END DESC NULLS LAST,
      CASE WHEN sqlc.narg(sort_field)::text = 'latency' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) END ASC NULLS LAST,
      CASE WHEN sqlc.narg(sort_field)::text = 'cost' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN (
        SELECT COALESCE(SUM(
            CASE
                WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
                WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
                ELSE 0
            END
        ), 0)
        FROM ledger_entries le
        WHERE le.request_record_id = r.id AND le.currency = 'USD'
      ) END DESC NULLS LAST,
      CASE WHEN sqlc.narg(sort_field)::text = 'cost' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN (
        SELECT COALESCE(SUM(
            CASE
                WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
                WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
                ELSE 0
            END
        ), 0)
        FROM ledger_entries le
        WHERE le.request_record_id = r.id AND le.currency = 'USD'
      ) END ASC NULLS LAST,
      CASE WHEN sqlc.narg(sort_field)::text = 'tokens' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN (
        COALESCE(ur.uncached_input_tokens, 0)
        + COALESCE(ur.cache_read_input_tokens, 0)
        + COALESCE(ur.cache_write_5m_input_tokens, 0)
        + COALESCE(ur.cache_write_1h_input_tokens, 0)
        + COALESCE(ur.cache_write_30m_input_tokens, 0)
        + COALESCE(ur.output_tokens_total, 0)
      ) END DESC NULLS LAST,
      CASE WHEN sqlc.narg(sort_field)::text = 'tokens' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN (
        COALESCE(ur.uncached_input_tokens, 0)
        + COALESCE(ur.cache_read_input_tokens, 0)
        + COALESCE(ur.cache_write_5m_input_tokens, 0)
        + COALESCE(ur.cache_write_1h_input_tokens, 0)
        + COALESCE(ur.cache_write_30m_input_tokens, 0)
        + COALESCE(ur.output_tokens_total, 0)
      ) END ASC NULLS LAST,
      r.id DESC
    LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset)
)
SELECT
    fp.total_count,
    r.id,
    r.request_id,
    r.created_at,
    r.client_ip,
    rt.id AS route_id,
    rt.name AS route_name,
    r.api_key_id,
    ak.name AS api_key_name,
    ak.key_prefix AS api_key_prefix,
    ak.key_plaintext AS api_key_plaintext,
    r.endpoint,
    r.stream,
    r.requested_model_id,
    m.display_name AS model_display_name,
    r.ingress_protocol,
    ps.uncached_input_price AS input_price_per_1m,
    ps.output_price AS output_price_per_1m,
    ps.cache_read_input_price AS cache_read_price_per_1m,
    ps.cache_write_5m_input_price AS cache_write_5m_price_per_1m,
    ps.cache_write_1h_input_price AS cache_write_1h_price_per_1m,
    ps.cache_write_30m_input_price AS cache_write_30m_price_per_1m,
    ps.reasoning_output_price AS reasoning_output_price_per_1m,
    ps.service_tier AS price_service_tier,
    r.reasoning_effort,
    COALESCE(ur.uncached_input_tokens, 0)::bigint AS uncached_input_tokens,
    COALESCE(ur.cache_read_input_tokens, 0)::bigint AS cache_read_input_tokens,
    COALESCE(ur.cache_write_5m_input_tokens, 0)::bigint AS cache_write_5m_input_tokens,
    COALESCE(ur.cache_write_1h_input_tokens, 0)::bigint AS cache_write_1h_input_tokens,
    COALESCE(ur.cache_write_30m_input_tokens, 0)::bigint AS cache_write_30m_input_tokens,
    (
        COALESCE(ur.uncached_input_tokens, 0)
        + COALESCE(ur.cache_read_input_tokens, 0)
        + COALESCE(ur.cache_write_5m_input_tokens, 0)
        + COALESCE(ur.cache_write_1h_input_tokens, 0)
        + COALESCE(ur.cache_write_30m_input_tokens, 0)
    )::bigint AS input_tokens,
    COALESCE(ur.output_tokens_total, 0)::bigint AS output_tokens,
    COALESCE(ur.reasoning_output_tokens, 0)::bigint AS reasoning_output_tokens,
    r.started_at,
    r.completed_at,
    r.gateway_first_token_at,
    (
        SELECT COALESCE(SUM(
            CASE
                WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
                WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
                ELSE 0
            END
        ), 0)
        FROM ledger_entries le
        WHERE le.request_record_id = r.id AND le.currency = 'USD'
    )::numeric AS user_charge_usd
FROM filtered_page fp
JOIN request_records r ON r.id = fp.id
JOIN usage_records ur ON ur.request_record_id = r.id
LEFT JOIN api_keys ak ON ak.id = r.api_key_id
LEFT JOIN routes rt ON rt.id = COALESCE(r.route_id, ak.route_id)
LEFT JOIN models m ON m.model_id = r.requested_model_id
LEFT JOIN price_snapshots ps ON ps.request_record_id = r.id
ORDER BY
  CASE WHEN COALESCE(sqlc.narg(sort_field)::text, 'created_at') IN ('', 'created_at') AND COALESCE(sqlc.narg(sort_desc)::bool, true) THEN r.created_at END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg(sort_field)::text, 'created_at') IN ('', 'created_at') AND NOT COALESCE(sqlc.narg(sort_desc)::bool, true) THEN r.created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'model' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN COALESCE(m.display_name, r.requested_model_id) END DESC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'model' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN COALESCE(m.display_name, r.requested_model_id) END ASC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'reasoning' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN r.reasoning_effort END DESC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'reasoning' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN r.reasoning_effort END ASC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'stream' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN r.stream END DESC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'stream' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN r.stream END ASC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'latency' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) END DESC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'latency' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) END ASC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'cost' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN (
    SELECT COALESCE(SUM(
        CASE
            WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
            WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
            ELSE 0
        END
    ), 0)
    FROM ledger_entries le
    WHERE le.request_record_id = r.id AND le.currency = 'USD'
  ) END DESC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'cost' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN (
    SELECT COALESCE(SUM(
        CASE
            WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
            WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
            ELSE 0
        END
    ), 0)
    FROM ledger_entries le
    WHERE le.request_record_id = r.id AND le.currency = 'USD'
  ) END ASC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'tokens' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN (
    COALESCE(ur.uncached_input_tokens, 0)
    + COALESCE(ur.cache_read_input_tokens, 0)
    + COALESCE(ur.cache_write_5m_input_tokens, 0)
    + COALESCE(ur.cache_write_1h_input_tokens, 0)
    + COALESCE(ur.cache_write_30m_input_tokens, 0)
    + COALESCE(ur.output_tokens_total, 0)
  ) END DESC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'tokens' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN (
    COALESCE(ur.uncached_input_tokens, 0)
    + COALESCE(ur.cache_read_input_tokens, 0)
    + COALESCE(ur.cache_write_5m_input_tokens, 0)
    + COALESCE(ur.cache_write_1h_input_tokens, 0)
    + COALESCE(ur.cache_write_30m_input_tokens, 0)
    + COALESCE(ur.output_tokens_total, 0)
  ) END ASC NULLS LAST,
  r.id DESC;

-- name: CountConsoleBilledRequests :one
SELECT COUNT(*)::bigint AS total
FROM request_records r
JOIN usage_records ur ON ur.request_record_id = r.id
LEFT JOIN api_keys ak ON ak.id = r.api_key_id
LEFT JOIN models m ON m.model_id = r.requested_model_id
WHERE r.user_id = sqlc.arg(user_id)
  AND (
      SELECT COALESCE(SUM(
          CASE
              WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
              WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
              ELSE 0
          END
      ), 0)
      FROM ledger_entries le
      WHERE le.request_record_id = r.id AND le.currency = 'USD'
  ) > 0
  AND (
      COALESCE(cardinality(sqlc.narg(route_ids)::bigint[]), 0) = 0
      OR COALESCE(r.route_id, ak.route_id) = ANY(sqlc.narg(route_ids)::bigint[])
  )
  AND (
      COALESCE(cardinality(sqlc.narg(api_key_ids)::bigint[]), 0) = 0
      OR r.api_key_id = ANY(sqlc.narg(api_key_ids)::bigint[])
  )
  AND (
      COALESCE(cardinality(sqlc.narg(endpoints)::text[]), 0) = 0
      OR r.endpoint = ANY(sqlc.narg(endpoints)::text[])
  )
  AND (
      COALESCE(cardinality(sqlc.narg(stream_types)::text[]), 0) = 0
      OR (r.stream AND 'stream' = ANY(sqlc.narg(stream_types)::text[]))
      OR ((NOT r.stream) AND 'sync' = ANY(sqlc.narg(stream_types)::text[]))
  )
  AND (
      sqlc.narg(q)::text IS NULL
      OR btrim(sqlc.narg(q)::text) = ''
      OR r.requested_model_id ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
      OR COALESCE(m.display_name, '') ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
      OR r.request_id ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
      OR COALESCE(r.client_ip, '') ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
  )
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR r.created_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR r.created_at < sqlc.narg(to_time)::timestamptz);

-- name: SummarizeConsoleBilledRequests :one
-- 账户累计实际扣费请求。筛选口径与列表相同；from_time/to_time 可空（narg，NULL = 不过滤时间）。
-- 先按 user_id + 时间窗收口，账本只 JOIN 一次，避免对每行做两次 ledger 相关子查询。
-- windowed / charges 必须 MATERIALIZED：pgx 预编译几次后会改用 generic plan，
-- 可选筛选参数未知时优化器把账本聚合估成 1 行并内联进嵌套循环，1 万行会扫几千万次。
WITH windowed AS MATERIALIZED (
    SELECT
        r.id,
        r.created_at,
        r.stream,
        r.started_at,
        r.completed_at,
        r.gateway_first_token_at,
        r.requested_model_id,
        r.ingress_protocol,
        r.route_id,
        r.api_key_id,
        r.endpoint,
        r.request_id,
        r.client_ip
    FROM request_records r
    WHERE r.user_id = sqlc.arg(user_id)
      AND (sqlc.narg(from_time)::timestamptz IS NULL OR r.created_at >= sqlc.narg(from_time)::timestamptz)
      AND (sqlc.narg(to_time)::timestamptz IS NULL OR r.created_at < sqlc.narg(to_time)::timestamptz)
),
charges AS MATERIALIZED (
    SELECT
        le.request_record_id,
        SUM(
            CASE
                WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
                WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
                ELSE 0
            END
        ) AS charge_usd
    FROM ledger_entries le
    JOIN windowed w ON w.id = le.request_record_id
    WHERE le.currency = 'USD'
    GROUP BY le.request_record_id
)
SELECT
    COUNT(*)::bigint AS request_count,
    COALESCE(SUM(
        COALESCE(ur.uncached_input_tokens, 0)
        + COALESCE(ur.cache_read_input_tokens, 0)
        + COALESCE(ur.cache_write_5m_input_tokens, 0)
        + COALESCE(ur.cache_write_1h_input_tokens, 0)
        + COALESCE(ur.cache_write_30m_input_tokens, 0)
        + COALESCE(ur.output_tokens_total, 0)
    ), 0)::bigint AS token_count,
    COALESCE(SUM(
        COALESCE(ur.uncached_input_tokens, 0)
        + COALESCE(ur.cache_read_input_tokens, 0)
        + COALESCE(ur.cache_write_5m_input_tokens, 0)
        + COALESCE(ur.cache_write_1h_input_tokens, 0)
        + COALESCE(ur.cache_write_30m_input_tokens, 0)
    ), 0)::bigint AS input_token_count,
    COALESCE(SUM(COALESCE(ur.output_tokens_total, 0)), 0)::bigint AS output_token_count,
    COALESCE(SUM(COALESCE(ur.uncached_input_tokens, 0)), 0)::bigint AS uncached_input_token_count,
    COALESCE(SUM(COALESCE(ur.cache_read_input_tokens, 0)), 0)::bigint AS cache_read_token_count,
    COALESCE(SUM(
        COALESCE(ur.cache_write_5m_input_tokens, 0)
        + COALESCE(ur.cache_write_1h_input_tokens, 0)
        + COALESCE(ur.cache_write_30m_input_tokens, 0)
    ), 0)::bigint AS cache_write_token_count,
    COALESCE(SUM(c.charge_usd), 0)::numeric AS charge_usd,
    COALESCE(SUM(
        COALESCE(ur.uncached_input_tokens, 0)::numeric
        * COALESCE(ps.uncached_input_price, 0)
        / 1000000
    ), 0)::numeric AS uncached_input_charge_usd,
    COALESCE(SUM(
        GREATEST(
            COALESCE(ur.output_tokens_total, 0) - COALESCE(ur.reasoning_output_tokens, 0),
            0
        )::numeric * COALESCE(ps.output_price, 0) / 1000000
        + COALESCE(ur.reasoning_output_tokens, 0)::numeric
            * COALESCE(ps.reasoning_output_price, ps.output_price, 0)
            / 1000000
    ), 0)::numeric AS output_charge_usd,
    COALESCE(SUM(
        COALESCE(ur.cache_read_input_tokens, 0)::numeric
        * COALESCE(ps.cache_read_input_price, ps.uncached_input_price, 0)
        / 1000000
    ), 0)::numeric AS cache_read_charge_usd,
    COALESCE(SUM(
        COALESCE(ur.cache_write_5m_input_tokens, 0)::numeric
            * COALESCE(ps.cache_write_5m_input_price, ps.uncached_input_price, 0)
            / 1000000
        + COALESCE(ur.cache_write_1h_input_tokens, 0)::numeric
            * COALESCE(ps.cache_write_1h_input_price, ps.uncached_input_price, 0)
            / 1000000
        + COALESCE(ur.cache_write_30m_input_tokens, 0)::numeric
            * COALESCE(ps.cache_write_30m_input_price, ps.uncached_input_price, 0)
            / 1000000
    ), 0)::numeric AS cache_write_charge_usd,
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
            + COALESCE(ur.cache_write_5m_input_tokens, 0)::numeric
                * COALESCE(ps.cache_write_5m_input_price, ps.uncached_input_price, 0)
            + COALESCE(ur.cache_write_1h_input_tokens, 0)::numeric
                * COALESCE(ps.cache_write_1h_input_price, ps.uncached_input_price, 0)
            + COALESCE(ur.cache_write_30m_input_tokens, 0)::numeric
                * COALESCE(ps.cache_write_30m_input_price, ps.uncached_input_price, 0)
        ) / 1000000
        / COALESCE(NULLIF(ps.price_ratio, 0), 1)
    ), 0)::numeric AS list_charge_usd,
    COALESCE(
        AVG(EXTRACT(EPOCH FROM (w.completed_at - w.started_at)) * 1000)
            FILTER (WHERE w.completed_at IS NOT NULL AND w.started_at IS NOT NULL),
        0
    )::float8 AS average_latency_ms,
    COALESCE(
        AVG(EXTRACT(EPOCH FROM (w.gateway_first_token_at - w.started_at)) * 1000)
            FILTER (
                WHERE w.stream
                  AND w.gateway_first_token_at IS NOT NULL
                  AND w.started_at IS NOT NULL
            ),
        0
    )::float8 AS average_first_token_ms,
    COALESCE(
        percentile_cont(0.5) WITHIN GROUP (
            ORDER BY EXTRACT(EPOCH FROM (w.completed_at - w.started_at)) * 1000
        ) FILTER (WHERE w.completed_at IS NOT NULL AND w.started_at IS NOT NULL),
        0
    )::float8 AS median_latency_ms,
    COALESCE(
        AVG(
            COALESCE(ur.output_tokens_total, 0)::float8
            / EXTRACT(EPOCH FROM (w.completed_at - w.gateway_first_token_at))
        ) FILTER (
            WHERE w.stream
              AND w.gateway_first_token_at IS NOT NULL
              AND w.completed_at IS NOT NULL
              AND w.completed_at > w.gateway_first_token_at
              AND COALESCE(ur.output_tokens_total, 0) > 0
        ),
        0
    )::float8 AS average_tps,
    COUNT(*) FILTER (WHERE w.stream)::bigint AS stream_count
FROM windowed w
JOIN charges c ON c.request_record_id = w.id AND c.charge_usd > 0
JOIN usage_records ur ON ur.request_record_id = w.id
LEFT JOIN price_snapshots ps ON ps.request_record_id = w.id
LEFT JOIN api_keys ak ON ak.id = w.api_key_id
LEFT JOIN models m ON m.model_id = w.requested_model_id
WHERE (
      COALESCE(cardinality(sqlc.narg(route_ids)::bigint[]), 0) = 0
      OR COALESCE(w.route_id, ak.route_id) = ANY(sqlc.narg(route_ids)::bigint[])
  )
  AND (
      COALESCE(cardinality(sqlc.narg(api_key_ids)::bigint[]), 0) = 0
      OR w.api_key_id = ANY(sqlc.narg(api_key_ids)::bigint[])
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

-- name: ListConsoleBilledRequestTopModels :many
-- 当前时间窗内实际扣费次数最多的三个模型；占比由调用方按这三行之和归一到 100%。
-- 协议和价格取该模型最近一条扣费请求，供卡片悬停复用列表模型详情。
-- 收口方式与 SummarizeConsoleBilledRequests 相同：先时间窗，再一次账本 JOIN。
-- CTE 同样必须 MATERIALIZED，原因见 SummarizeConsoleBilledRequests。
WITH windowed AS MATERIALIZED (
    SELECT
        r.id,
        r.created_at,
        r.stream,
        r.requested_model_id,
        r.ingress_protocol,
        r.route_id,
        r.api_key_id,
        r.endpoint,
        r.request_id,
        r.client_ip
    FROM request_records r
    WHERE r.user_id = sqlc.arg(user_id)
      AND (sqlc.narg(from_time)::timestamptz IS NULL OR r.created_at >= sqlc.narg(from_time)::timestamptz)
      AND (sqlc.narg(to_time)::timestamptz IS NULL OR r.created_at < sqlc.narg(to_time)::timestamptz)
),
charges AS MATERIALIZED (
    SELECT
        le.request_record_id,
        SUM(
            CASE
                WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
                WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
                ELSE 0
            END
        ) AS charge_usd
    FROM ledger_entries le
    JOIN windowed w ON w.id = le.request_record_id
    WHERE le.currency = 'USD'
    GROUP BY le.request_record_id
),
billed AS (
    SELECT w.id, w.requested_model_id, w.ingress_protocol, w.created_at
    FROM windowed w
    JOIN charges c ON c.request_record_id = w.id AND c.charge_usd > 0
    JOIN usage_records ur ON ur.request_record_id = w.id
    LEFT JOIN api_keys ak ON ak.id = w.api_key_id
    LEFT JOIN models m ON m.model_id = w.requested_model_id
    WHERE (
          COALESCE(cardinality(sqlc.narg(route_ids)::bigint[]), 0) = 0
          OR COALESCE(w.route_id, ak.route_id) = ANY(sqlc.narg(route_ids)::bigint[])
      )
      AND (
          COALESCE(cardinality(sqlc.narg(api_key_ids)::bigint[]), 0) = 0
          OR w.api_key_id = ANY(sqlc.narg(api_key_ids)::bigint[])
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
top AS (
    SELECT requested_model_id, COUNT(*)::bigint AS request_count
    FROM billed
    GROUP BY requested_model_id
    ORDER BY request_count DESC, requested_model_id ASC
    LIMIT 3
),
latest AS (
    SELECT DISTINCT ON (b.requested_model_id)
        b.requested_model_id,
        b.ingress_protocol,
        ps.uncached_input_price,
        ps.output_price
    FROM billed b
    LEFT JOIN price_snapshots ps ON ps.request_record_id = b.id
    WHERE b.requested_model_id IN (SELECT requested_model_id FROM top)
    ORDER BY b.requested_model_id, b.created_at DESC, b.id DESC
)
SELECT
    t.requested_model_id,
    COALESCE(NULLIF(m.display_name, ''), t.requested_model_id) AS model_display_name,
    t.request_count,
    COALESCE(l.ingress_protocol, '') AS ingress_protocol,
    l.uncached_input_price AS input_price_per_1m,
    l.output_price AS output_price_per_1m
FROM top t
LEFT JOIN models m ON m.model_id = t.requested_model_id
LEFT JOIN latest l ON l.requested_model_id = t.requested_model_id
ORDER BY t.request_count DESC, t.requested_model_id ASC;

-- name: ListConsoleFilterRoutes :many
-- 线路筛选项来自线路目录全量，不按用户历史请求聚合。
SELECT rt.id, rt.name
FROM routes rt
ORDER BY rt.name, rt.id;

-- name: ListConsoleFilterAPIKeys :many
-- 密钥筛选项来自当前用户的 API Key 目录，不按请求历史聚合。
SELECT ak.id, ak.name
FROM api_keys ak
WHERE ak.user_id = sqlc.arg(user_id)
ORDER BY ak.name, ak.id;

-- name: ListConsoleBilledRequestEndpoints :many
SELECT DISTINCT r.endpoint
FROM request_records r
JOIN usage_records ur ON ur.request_record_id = r.id
WHERE r.user_id = sqlc.arg(user_id)
  AND (
      SELECT COALESCE(SUM(
          CASE
              WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
              WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
              ELSE 0
          END
      ), 0)
      FROM ledger_entries le
      WHERE le.request_record_id = r.id AND le.currency = 'USD'
  ) > 0
ORDER BY r.endpoint;

-- name: ListConsoleBilledRequestStreamTypes :many
-- 类型筛选项来自当前用户实际扣费请求上出现过的 stream，不写死流式/非流式。
SELECT DISTINCT r.stream
FROM request_records r
JOIN usage_records ur ON ur.request_record_id = r.id
WHERE r.user_id = sqlc.arg(user_id)
  AND (
      SELECT COALESCE(SUM(
          CASE
              WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
              WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
              ELSE 0
          END
      ), 0)
      FROM ledger_entries le
      WHERE le.request_record_id = r.id AND le.currency = 'USD'
  ) > 0
ORDER BY r.stream DESC;
