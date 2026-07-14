-- name: SummarizeChannelCostExposures :many
-- SummarizeChannelCostExposures 按渠道聚合时间范围内的成本敞口（条数 + 金额上界合计），供渠道成本对账。
SELECT
    e.channel_id,
    c.name AS channel_name,
    e.provider_id,
    e.currency,
    COUNT(*) AS exposures,
    COALESCE(SUM(e.estimated_cost_amount), 0)::numeric AS total_estimated_cost
FROM channel_cost_exposures e
JOIN channels c ON c.id = e.channel_id
WHERE e.created_at >= sqlc.arg(from_time)
  AND e.created_at < sqlc.arg(to_time)
GROUP BY e.channel_id, c.name, e.provider_id, e.currency
ORDER BY total_estimated_cost DESC;

-- name: ListChannelCostExposuresPage :many
-- ListChannelCostExposuresPage 按渠道分页倒序列出成本敞口明细，连带对外 request_id 供跳转排查。
SELECT
    e.id,
    e.request_record_id,
    r.request_id,
    e.attempt_id,
    e.channel_id,
    e.provider_id,
    e.reason,
    e.estimated_input_tokens,
    e.assumed_output_tokens,
    e.estimated_cost_amount,
    e.currency,
    e.created_at
FROM channel_cost_exposures e
JOIN request_records r ON r.id = e.request_record_id
WHERE e.channel_id = sqlc.arg(channel_id)
  AND e.created_at >= sqlc.arg(from_time)
  AND e.created_at < sqlc.arg(to_time)
ORDER BY e.created_at DESC, e.id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountChannelCostExposures :one
-- CountChannelCostExposures 返回与 ListChannelCostExposuresPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM channel_cost_exposures e
WHERE e.channel_id = sqlc.arg(channel_id)
  AND e.created_at >= sqlc.arg(from_time)
  AND e.created_at < sqlc.arg(to_time);

-- name: GetLedgerBillingExceptionByRequest :one
-- GetLedgerBillingExceptionByRequest 按请求记录 ID 读取该请求的 billing exception（每请求至多一条）。
SELECT *
FROM ledger_billing_exceptions
WHERE request_record_id = sqlc.arg(request_record_id);

-- name: ListLedgerBillingExceptionsPage :many
-- ListLedgerBillingExceptionsPage 供 admin 只读查询台（M6）按用户/事件类型/时间过滤分页倒序列出核销/风险敞口事实。
-- 所有过滤项为 NULL 时不过滤。
SELECT
    id,
    user_id,
    request_record_id,
    reservation_id,
    event_type,
    actual_amount,
    captured_amount,
    platform_amount,
    currency,
    reason_code,
    reason,
    created_at
FROM ledger_billing_exceptions
WHERE (sqlc.narg('user_id')::bigint IS NULL OR user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('event_type')::text IS NULL OR event_type = sqlc.narg('event_type')::text)
  AND (sqlc.narg('reason_code')::text IS NULL OR reason_code = sqlc.narg('reason_code')::text)
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'created_at') IN ('', 'created_at') AND COALESCE(sqlc.narg('sort_desc')::bool, true) THEN created_at END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'created_at') IN ('', 'created_at') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, true) THEN created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'user_id' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN user_id END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'user_id' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN user_id END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'event_type' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN event_type END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'event_type' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN event_type END ASC NULLS LAST,
  id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountLedgerBillingExceptions :one
-- CountLedgerBillingExceptions 返回与 ListLedgerBillingExceptionsPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM ledger_billing_exceptions
WHERE (sqlc.narg('user_id')::bigint IS NULL OR user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('event_type')::text IS NULL OR event_type = sqlc.narg('event_type')::text)
  AND (sqlc.narg('reason_code')::text IS NULL OR reason_code = sqlc.narg('reason_code')::text)
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz);

-- name: ListLedgerEntriesByUser :many
-- ListLedgerEntriesByUser 按用户和币种倒序列出账本流水。
SELECT
    id,
    user_id,
    request_record_id,
    entry_type,
    amount,
    currency,
    balance_before,
    balance_after,
    idempotency_key,
    reason,
    created_at
FROM
    ledger_entries
WHERE
    user_id = sqlc.arg (user_id)
  AND currency = sqlc.arg (currency)
ORDER BY
    created_at DESC,
    id DESC
LIMIT
    sqlc.arg (limit_rows)
    OFFSET
    sqlc.arg (offset_rows);

-- name: ListLedgerEntriesPage :many
-- ListLedgerEntriesPage 供 admin 只读查询台（M6）按用户/类型/币种/时间过滤分页倒序列出账本流水。
-- 所有过滤项为 NULL 时不过滤。
SELECT
    id,
    user_id,
    request_record_id,
    entry_type,
    amount,
    currency,
    balance_before,
    balance_after,
    idempotency_key,
    reason,
    created_at
FROM ledger_entries
WHERE (sqlc.narg('user_id')::bigint IS NULL OR user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('entry_type')::text IS NULL OR entry_type = sqlc.narg('entry_type')::text)
  AND (sqlc.narg('currency')::text IS NULL OR currency = sqlc.narg('currency')::text)
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'created_at') IN ('', 'created_at') AND COALESCE(sqlc.narg('sort_desc')::bool, true) THEN created_at END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'created_at') IN ('', 'created_at') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, true) THEN created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'user_id' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN user_id END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'user_id' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN user_id END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'amount' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN amount END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'amount' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN amount END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'entry_type' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN entry_type END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'entry_type' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN entry_type END ASC NULLS LAST,
  id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountLedgerEntries :one
-- CountLedgerEntries 返回与 ListLedgerEntriesPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM ledger_entries
WHERE (sqlc.narg('user_id')::bigint IS NULL OR user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('entry_type')::text IS NULL OR entry_type = sqlc.narg('entry_type')::text)
  AND (sqlc.narg('currency')::text IS NULL OR currency = sqlc.narg('currency')::text)
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz);
