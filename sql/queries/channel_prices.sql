-- name: CreateChannelPrice :one
-- CreateChannelPrice 创建一条渠道-模型成本价（DEC-026：渠道只录成本，售价取 model_prices × 线路倍率）。
-- 启用窗口重叠由 ex_channel_prices_enabled_window 保证，违反报 23P01。
INSERT INTO channel_prices (
    channel_id,
    model_id,
    currency,
    pricing_unit,
    uncached_input_cost,
    cache_read_input_cost,
    cache_write_5m_input_cost,
    cache_write_1h_input_cost,
    cache_write_30m_input_cost,
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
    sqlc.arg(cache_write_30m_input_cost),
    sqlc.arg(output_cost),
    sqlc.arg(reasoning_output_cost),
    sqlc.arg(status),
    sqlc.arg(effective_from),
    sqlc.arg(effective_to)
)
RETURNING *;

-- name: GetChannelPrice :one
-- GetChannelPrice 按主键读取单条渠道-模型价。
SELECT * FROM channel_prices WHERE id = sqlc.arg(id) LIMIT 1;

-- name: ListChannelPricesByChannel :many
-- ListChannelPricesByChannel 列出某 channel 下全部渠道-模型成本价（含历史与停用），连带模型对外 ID/展示名，供 admin 管理台展示成本。
SELECT
    cp.id,
    cp.channel_id,
    cp.model_id,
    cp.currency,
    cp.pricing_unit,
    cp.uncached_input_cost,
    cp.cache_read_input_cost,
    cp.cache_write_5m_input_cost,
    cp.cache_write_1h_input_cost,
    cp.cache_write_30m_input_cost,
    cp.output_cost,
    cp.reasoning_output_cost,
    cp.status,
    cp.effective_from,
    cp.effective_to,
    cp.created_at,
    cp.updated_at,
    m.model_id AS model_external_id,
    m.display_name AS model_display_name
FROM channel_prices cp
JOIN models m ON m.id = cp.model_id
WHERE cp.channel_id = sqlc.arg(channel_id)
ORDER BY m.model_id, cp.effective_from DESC, cp.id DESC;

-- name: ListEnabledChannelPriceWindows :many
-- ListEnabledChannelPriceWindows 取某 channel/model 全部启用中的价格生效窗口，供「窗口不重叠」校验；exclude_id 用于更新时排除自身（创建时传 0）。
SELECT id, effective_from, effective_to
FROM channel_prices
WHERE channel_id = sqlc.arg(channel_id)
    AND model_id = sqlc.arg(model_id)
    AND status = 'enabled'
    AND id <> sqlc.arg(exclude_id);

-- name: UpdateChannelPriceWindow :one
-- UpdateChannelPriceWindow 调整生效结束时间与启停状态；金额不可改（改价请新建一条），账务可复算。
UPDATE channel_prices
SET effective_to = sqlc.arg(effective_to),
    status = sqlc.arg(status),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: FindActiveChannelPrice :one
-- FindActiveChannelPrice 查找指定 channel/model 在指定时间生效的渠道-模型价，一次取回售价 + 成本（阶段 15 计费同源）。
SELECT *
FROM channel_prices
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
