-- name: CreateModelPrice :one
-- CreateModelPrice 创建一条模型基准售价（DEC-026）。客户最终售价 = 本基准价 × 线路倍率。
-- 启用窗口重叠由 ex_model_prices_enabled_window 保证，违反报 23P01。
INSERT INTO model_prices (
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

-- name: GetModelPrice :one
-- GetModelPrice 按主键读取单条模型基准售价。
SELECT * FROM model_prices WHERE id = sqlc.arg(id) LIMIT 1;

-- name: ListModelPricesByModel :many
-- ListModelPricesByModel 列出某模型的全部基准售价（含历史与停用），连带模型对外 ID/展示名，供 admin 管理台展示。
SELECT
    mp.id,
    mp.model_id,
    mp.currency,
    mp.pricing_unit,
    mp.uncached_input_price,
    mp.cache_read_input_price,
    mp.cache_write_5m_input_price,
    mp.cache_write_1h_input_price,
    mp.output_price,
    mp.reasoning_output_price,
    mp.status,
    mp.effective_from,
    mp.effective_to,
    mp.created_at,
    mp.updated_at,
    m.model_id AS model_external_id,
    m.display_name AS model_display_name
FROM model_prices mp
JOIN models m ON m.id = mp.model_id
WHERE mp.model_id = sqlc.arg(model_id)
ORDER BY mp.effective_from DESC, mp.id DESC;

-- name: ListEnabledModelPriceWindows :many
-- ListEnabledModelPriceWindows 取某 model 全部启用中的价格生效窗口，供「窗口不重叠」校验；exclude_id 用于更新时排除自身（创建时传 0）。
SELECT id, effective_from, effective_to
FROM model_prices
WHERE model_id = sqlc.arg(model_id)
    AND status = 'enabled'
    AND id <> sqlc.arg(exclude_id);

-- name: UpdateModelPriceWindow :one
-- UpdateModelPriceWindow 调整生效结束时间与启停状态；金额不可改（改价请新建一条），账务可复算。
UPDATE model_prices
SET effective_to = sqlc.arg(effective_to),
    status = sqlc.arg(status),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: FindActiveModelPrice :one
-- FindActiveModelPrice 查找指定 model 在指定时间生效的基准售价（settlement / authorization 计算客户售价用）。
SELECT *
FROM model_prices
WHERE model_id = sqlc.arg(model_id)
    AND status = 'enabled'
    AND effective_from <= sqlc.arg(at_time)
    AND (
        effective_to IS NULL
        OR effective_to > sqlc.arg(at_time)
    )
ORDER BY effective_from DESC, id DESC
LIMIT 1;
