-- M9 工作台看板（运营首页只读聚合）。全部纯只读、不引入新业务事实。
-- 约定：金额走 NUMERIC（service 层格式化为十进制字符串，绝不经 float）；
-- 时间范围一律用可空 from_time/to_time（narg，NULL = 不过滤）；
-- 区间口径 [from, to)（左闭右开），与 M6 列表过滤一致。

-- name: DashboardRequestStatusCounts :many
-- DashboardRequestStatusCounts 在区间内按请求终态聚合计数（service 据此算成功率/错误率）。
SELECT status, COUNT(*) AS total
FROM request_records
WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY status;

-- name: DashboardTokenTotals :one
-- DashboardTokenTotals 在区间内汇总 token 用量：input = 四类 input 之和，output = 权威输出总量。
SELECT
    COALESCE(SUM(
        uncached_input_tokens
        + cache_read_input_tokens
        + cache_write_5m_input_tokens
        + cache_write_1h_input_tokens
        + cache_write_30m_input_tokens
    ), 0)::bigint AS input_tokens,
    COALESCE(SUM(output_tokens_total), 0)::bigint AS output_tokens
FROM usage_records
WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz);

-- name: DashboardRevenueByCurrency :many
-- DashboardRevenueByCurrency 在区间内按币种汇总平台收入（客户结算扣费 = entry_type='debit'）。
SELECT currency, COALESCE(SUM(amount), 0)::numeric AS total
FROM ledger_entries
WHERE entry_type = 'debit'
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY currency
ORDER BY currency;

-- name: DashboardCostByCurrency :many
-- DashboardCostByCurrency 在区间内按币种汇总平台上游成本（cost_snapshots.total_cost_amount）。
SELECT currency, COALESCE(SUM(total_cost_amount), 0)::numeric AS total
FROM cost_snapshots
WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY currency
ORDER BY currency;

-- name: DashboardBalanceByCurrency :many
-- DashboardBalanceByCurrency 按币种汇总用户余额总额（时点值，无时间过滤）。
SELECT
    currency,
    COALESCE(SUM(balance), 0)::numeric AS total_balance,
    COALESCE(SUM(reserved_balance), 0)::numeric AS total_reserved
FROM user_balances
GROUP BY currency
ORDER BY currency;

-- name: DashboardBillingExceptionSummary :many
-- DashboardBillingExceptionSummary 在区间内按 event_type 聚合计费异常数与平台承担金额。
-- 语义为「区间内新增异常」（该表无 resolved 状态，写入即审计事实）。
SELECT
    event_type,
    COUNT(*) AS total,
    COALESCE(SUM(platform_amount), 0)::numeric AS platform_amount
FROM ledger_billing_exceptions
WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY event_type
ORDER BY event_type;

-- name: DashboardEnabledChannelCount :one
-- DashboardEnabledChannelCount 返回当前启用的 channel 数（时点值）。
SELECT COUNT(*) AS total
FROM channels
WHERE status = 'enabled';

-- name: DashboardChannelHealth :many
-- DashboardChannelHealth 按区间内 request_attempts 成功率推导每个 channel 的健康（无 health 列）。
-- LEFT JOIN + 时间过滤放在 ON 条件，保留区间内零尝试的 channel（service 视为 no_data）。
SELECT
    c.id AS channel_id,
    c.name,
    c.status,
    COUNT(a.id) AS attempt_total,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS attempt_succeeded
FROM channels c
LEFT JOIN request_attempts a
    ON a.channel_id = c.id
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY c.id, c.name, c.status
ORDER BY attempt_total DESC, c.id;

-- name: DashboardRequestsTimeseries :many
-- DashboardRequestsTimeseries 按时间桶（hour|day，UTC 截断）聚合请求数与成功数，供前端画折线。
SELECT
    date_trunc(sqlc.arg('unit')::text, created_at)::timestamptz AS bucket,
    COUNT(*) FILTER (WHERE status IN ('succeeded', 'failed')) AS total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS succeeded
FROM request_records
WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY bucket
ORDER BY bucket;

-- name: DashboardTokensTimeseries :many
-- DashboardTokensTimeseries 按时间桶聚合 input/output token 用量。
SELECT
    date_trunc(sqlc.arg('unit')::text, created_at)::timestamptz AS bucket,
    COALESCE(SUM(
        uncached_input_tokens
        + cache_read_input_tokens
        + cache_write_5m_input_tokens
        + cache_write_1h_input_tokens
        + cache_write_30m_input_tokens
    ), 0)::bigint AS input_tokens,
    COALESCE(SUM(output_tokens_total), 0)::bigint AS output_tokens
FROM usage_records
WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY bucket
ORDER BY bucket;

-- name: DashboardSpendTimeseries :many
-- DashboardSpendTimeseries 按时间桶 + 币种聚合平台收入（debit），spend 多币种各成一线。
SELECT
    date_trunc(sqlc.arg('unit')::text, created_at)::timestamptz AS bucket,
    currency,
    COALESCE(SUM(amount), 0)::numeric AS total
FROM ledger_entries
WHERE entry_type = 'debit'
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY bucket, currency
ORDER BY bucket, currency;

-- name: DashboardCostTimeseries :many
-- DashboardCostTimeseries 按时间桶 + 币种聚合平台实际成本（cost_snapshots.total_cost_amount），
-- 与 spend 同形（多币种各成一线），供前端画成本趋势折线。时间列用 cost_snapshots.created_at
-- （结算写入时刻），与 Overview 成本 KPI 一致。
SELECT
    date_trunc(sqlc.arg('unit')::text, created_at)::timestamptz AS bucket,
    currency,
    COALESCE(SUM(total_cost_amount), 0)::numeric AS total
FROM cost_snapshots
WHERE (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY bucket, currency
ORDER BY bucket, currency;
