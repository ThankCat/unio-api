-- name: CreateChannelCostPrice :one
-- CreateChannelCostPrice 创建 channel/model 上游成本价配置。
INSERT INTO channel_cost_prices (
    channel_id,
    model_id,
    currency,
    pricing_unit,
    uncached_input_cost,
    cache_read_input_cost,
    cache_write_5m_input_cost,
    cache_write_1h_input_cost,
    output_cost,
    reasoning_output_cost,
    status,
    effective_from,
    effective_to
)
VALUES (
    sqlc.arg(channel_id),
    sqlc.arg(model_id),
    sqlc.arg(currency),
    sqlc.arg(pricing_unit),
    sqlc.arg(uncached_input_cost),
    sqlc.arg(cache_read_input_cost),
    sqlc.arg(cache_write_5m_input_cost),
    sqlc.arg(cache_write_1h_input_cost),
    sqlc.arg(output_cost),
    sqlc.arg(reasoning_output_cost),
    sqlc.arg(status),
    sqlc.arg(effective_from),
    sqlc.arg(effective_to)
)
RETURNING *;

-- name: FindActiveChannelCostPrice :one
-- FindActiveChannelCostPrice 查找指定 channel/model 在指定时间生效的上游成本价。
SELECT *
FROM channel_cost_prices
WHERE channel_id = sqlc.arg(channel_id)
    AND model_id = sqlc.arg(model_id)
    AND status = 'enabled'
    AND effective_from <= sqlc.arg(at_time)
    AND (
        effective_to IS NULL
        OR effective_to > sqlc.arg(at_time)
    )
ORDER BY effective_from DESC, id DESC
LIMIT 1;
