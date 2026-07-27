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
