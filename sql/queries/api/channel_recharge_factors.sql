-- name: FindActiveChannelRechargeFactor :one
-- FindActiveChannelRechargeFactor 查找指定 channel 在指定时间生效的充值倍率（账户级、无 model 维度）。
SELECT *
FROM channel_recharge_factors
WHERE channel_id = sqlc.arg(channel_id)
    AND status = 'enabled'
    AND effective_from <= sqlc.arg(at_time)
    AND (
        effective_to IS NULL
        OR effective_to > sqlc.arg(at_time)
    )
ORDER BY effective_from DESC, id DESC
LIMIT 1;
