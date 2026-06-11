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

-- name: GetChannelCostPrice :one
-- GetChannelCostPrice 按主键读取单条成本价。
SELECT * FROM channel_cost_prices WHERE id = sqlc.arg(id) LIMIT 1;

-- name: ListChannelCostPricesByChannel :many
-- ListChannelCostPricesByChannel 列出某 channel 下全部成本价（含历史与停用），连带模型对外 ID/展示名，供 admin 管理台展示。
SELECT
    ccp.id,
    ccp.channel_id,
    ccp.model_id,
    ccp.currency,
    ccp.pricing_unit,
    ccp.uncached_input_cost,
    ccp.cache_read_input_cost,
    ccp.cache_write_5m_input_cost,
    ccp.cache_write_1h_input_cost,
    ccp.output_cost,
    ccp.reasoning_output_cost,
    ccp.status,
    ccp.effective_from,
    ccp.effective_to,
    ccp.created_at,
    ccp.updated_at,
    m.model_id AS model_external_id,
    m.display_name AS model_display_name
FROM channel_cost_prices ccp
JOIN models m ON m.id = ccp.model_id
WHERE ccp.channel_id = sqlc.arg(channel_id)
ORDER BY m.model_id, ccp.effective_from DESC, ccp.id DESC;

-- name: ListEnabledChannelCostPriceWindows :many
-- ListEnabledChannelCostPriceWindows 取某 channel/model 全部启用中的价格生效窗口，供「窗口不重叠」校验；exclude_id 用于更新时排除自身（创建时传 0）。
SELECT id, effective_from, effective_to
FROM channel_cost_prices
WHERE channel_id = sqlc.arg(channel_id)
    AND model_id = sqlc.arg(model_id)
    AND status = 'enabled'
    AND id <> sqlc.arg(exclude_id);

-- name: UpdateChannelCostPriceWindow :one
-- UpdateChannelCostPriceWindow 调整生效结束时间与启停状态；金额不可改（改价请新建一条），账务可复算。
UPDATE channel_cost_prices
SET effective_to = sqlc.arg(effective_to),
    status = sqlc.arg(status),
    updated_at = now()
WHERE id = sqlc.arg(id)
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
