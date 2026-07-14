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
