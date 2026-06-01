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
