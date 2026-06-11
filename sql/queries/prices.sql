-- name: CreatePrice :one
-- CreatePrice 创建模型客户侧售卖价配置。
INSERT INTO prices (
    model_id,
    currency,
    pricing_unit,
    uncached_input_price,
    cache_read_input_price,
    cache_write_5m_input_price,
    cache_write_1h_input_price,
    output_price,
    reasoning_output_price,
    status,
    effective_from,
    effective_to
)
VALUES (
    sqlc.arg(model_id),
    sqlc.arg(currency),
    sqlc.arg(pricing_unit),
    sqlc.arg(uncached_input_price),
    sqlc.arg(cache_read_input_price),
    sqlc.arg(cache_write_5m_input_price),
    sqlc.arg(cache_write_1h_input_price),
    sqlc.arg(output_price),
    sqlc.arg(reasoning_output_price),
    sqlc.arg(status),
    sqlc.arg(effective_from),
    sqlc.arg(effective_to)
)
RETURNING *;

-- name: GetPrice :one
-- GetPrice 按主键读取单条售价。
SELECT * FROM prices WHERE id = sqlc.arg(id) LIMIT 1;

-- name: ListPricesByModel :many
-- ListPricesByModel 列出某模型全部售价（含历史与停用），供 admin 管理台展示。
SELECT *
FROM prices
WHERE model_id = sqlc.arg(model_id)
ORDER BY effective_from DESC, id DESC;

-- name: UpdatePriceWindow :one
-- UpdatePriceWindow 调整生效结束时间与启停状态；金额不可改（改价请新建一条）。
-- 启用窗口重叠由 DB EXCLUDE 约束（ex_prices_enabled_effective_window）保证，违反时报 23P01。
UPDATE prices
SET effective_to = sqlc.arg(effective_to),
    status = sqlc.arg(status),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: FindActivePriceForModel :one
-- FindActivePriceForModel 查找指定模型在指定时间生效的客户侧售卖价。
SELECT *
FROM prices
WHERE model_id = sqlc.arg(model_id)
    AND status = 'enabled'
    AND effective_from <= sqlc.arg(at_time)
    AND (
        effective_to IS NULL
        OR effective_to > sqlc.arg(at_time)
    )
ORDER BY effective_from DESC, id DESC
LIMIT 1;
