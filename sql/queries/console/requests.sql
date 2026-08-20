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
          OR r.request_id ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
          OR COALESCE(r.client_ip, '') ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
      )
      AND (sqlc.narg(from_time)::timestamptz IS NULL OR r.created_at >= sqlc.narg(from_time)::timestamptz)
      AND (sqlc.narg(to_time)::timestamptz IS NULL OR r.created_at < sqlc.narg(to_time)::timestamptz)
    ORDER BY
      CASE WHEN COALESCE(sqlc.narg(sort_field)::text, 'created_at') IN ('', 'created_at') AND COALESCE(sqlc.narg(sort_desc)::bool, true) THEN r.created_at END DESC NULLS LAST,
      CASE WHEN COALESCE(sqlc.narg(sort_field)::text, 'created_at') IN ('', 'created_at') AND NOT COALESCE(sqlc.narg(sort_desc)::bool, true) THEN r.created_at END ASC NULLS LAST,
      CASE WHEN sqlc.narg(sort_field)::text = 'model' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN r.requested_model_id END DESC NULLS LAST,
      CASE WHEN sqlc.narg(sort_field)::text = 'model' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN r.requested_model_id END ASC NULLS LAST,
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
    r.endpoint,
    r.stream,
    r.requested_model_id,
    r.reasoning_effort,
    (
        COALESCE(ur.uncached_input_tokens, 0)
        + COALESCE(ur.cache_read_input_tokens, 0)
        + COALESCE(ur.cache_write_5m_input_tokens, 0)
        + COALESCE(ur.cache_write_1h_input_tokens, 0)
        + COALESCE(ur.cache_write_30m_input_tokens, 0)
    )::bigint AS input_tokens,
    COALESCE(ur.output_tokens_total, 0)::bigint AS output_tokens,
    r.started_at,
    r.completed_at,
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
ORDER BY
  CASE WHEN COALESCE(sqlc.narg(sort_field)::text, 'created_at') IN ('', 'created_at') AND COALESCE(sqlc.narg(sort_desc)::bool, true) THEN r.created_at END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg(sort_field)::text, 'created_at') IN ('', 'created_at') AND NOT COALESCE(sqlc.narg(sort_desc)::bool, true) THEN r.created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'model' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN r.requested_model_id END DESC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'model' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN r.requested_model_id END ASC NULLS LAST,
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
      OR r.request_id ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
      OR COALESCE(r.client_ip, '') ILIKE '%' || btrim(sqlc.narg(q)::text) || '%'
  )
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR r.created_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR r.created_at < sqlc.narg(to_time)::timestamptz);

-- name: SummarizeConsoleBilledRequests :one
-- 账户累计实际扣费请求。from_time/to_time 可空（narg，NULL = 不过滤时间）。
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
    COALESCE(SUM((
        SELECT COALESCE(SUM(
            CASE
                WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
                WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
                ELSE 0
            END
        ), 0)
        FROM ledger_entries le
        WHERE le.request_record_id = r.id AND le.currency = 'USD'
    )), 0)::numeric AS charge_usd,
    COALESCE(
        AVG(EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)
            FILTER (WHERE r.completed_at IS NOT NULL AND r.started_at IS NOT NULL),
        0
    )::float8 AS average_latency_ms
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
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR r.created_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR r.created_at < sqlc.narg(to_time)::timestamptz);

-- name: ListConsoleBilledRequestRoutes :many
SELECT DISTINCT
    COALESCE(r.route_id, ak.route_id)::bigint AS id,
    rt.name AS name
FROM request_records r
JOIN usage_records ur ON ur.request_record_id = r.id
LEFT JOIN api_keys ak ON ak.id = r.api_key_id
JOIN routes rt ON rt.id = COALESCE(r.route_id, ak.route_id)
WHERE r.user_id = sqlc.arg(user_id)
  AND COALESCE(r.route_id, ak.route_id) IS NOT NULL
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
ORDER BY rt.name, COALESCE(r.route_id, ak.route_id);

-- name: ListConsoleBilledRequestAPIKeys :many
SELECT DISTINCT
    r.api_key_id AS id,
    ak.name AS name
FROM request_records r
JOIN usage_records ur ON ur.request_record_id = r.id
JOIN api_keys ak ON ak.id = r.api_key_id
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
ORDER BY ak.name, r.api_key_id;

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
