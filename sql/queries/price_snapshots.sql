-- name: CreatePriceSnapshot :one
-- CreatePriceSnapshot 创建一次请求结算使用的客户售价快照。
INSERT INTO price_snapshots (
    request_record_id,
    price_id,
    currency,
    pricing_unit,
    uncached_input_price,
    cache_read_input_price,
    cache_write_5m_input_price,
    cache_write_1h_input_price,
    output_price,
    reasoning_output_price,
    formula_version
)
VALUES (
    sqlc.arg(request_record_id),
    sqlc.arg(price_id),
    sqlc.arg(currency),
    sqlc.arg(pricing_unit),
    sqlc.arg(uncached_input_price),
    sqlc.arg(cache_read_input_price),
    sqlc.arg(cache_write_5m_input_price),
    sqlc.arg(cache_write_1h_input_price),
    sqlc.arg(output_price),
    sqlc.arg(reasoning_output_price),
    sqlc.arg(formula_version)
)
RETURNING *;

-- name: GetPriceSnapshotByRequest :one
-- GetPriceSnapshotByRequest 按请求 ID 读取客户售价快照。
SELECT *
FROM price_snapshots
WHERE request_record_id = sqlc.arg(request_record_id);
