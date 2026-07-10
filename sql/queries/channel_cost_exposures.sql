-- name: CreateChannelCostExposure :one
-- CreateChannelCostExposure 写入一条 bill-on-disconnect 渠道的平台成本敞口（DESIGN-bill-on-cancel 阶段一）。
INSERT INTO channel_cost_exposures (
    request_record_id,
    attempt_id,
    channel_id,
    provider_id,
    reason,
    estimated_input_tokens,
    assumed_output_tokens,
    estimated_cost_amount,
    currency
)
VALUES (
    sqlc.arg(request_record_id),
    sqlc.arg(attempt_id),
    sqlc.arg(channel_id),
    sqlc.arg(provider_id),
    sqlc.arg(reason),
    sqlc.arg(estimated_input_tokens),
    sqlc.arg(assumed_output_tokens),
    sqlc.arg(estimated_cost_amount),
    sqlc.arg(currency)
)
RETURNING id, request_record_id, attempt_id, channel_id, provider_id, reason, estimated_input_tokens, assumed_output_tokens, estimated_cost_amount, currency, created_at;

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
