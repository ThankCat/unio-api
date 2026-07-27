-- name: FindActiveChannelCostMultiplier :one
-- FindActiveChannelCostMultiplier 查找指定 channel/model 在指定时间生效的价格倍率：优先逐模型覆盖，
-- 无则回退渠道默认（model_id IS NULL）。(model_id IS NULL) ASC 让覆盖（false=0）排在默认（true=1）之前。
SELECT *
FROM channel_cost_multipliers
WHERE channel_id = sqlc.arg(channel_id)
    AND (model_id = sqlc.arg(model_id) OR model_id IS NULL)
    AND status = 'enabled'
    AND effective_from <= sqlc.arg(at_time)
    AND (
        effective_to IS NULL
        OR effective_to > sqlc.arg(at_time)
    )
ORDER BY (model_id IS NULL) ASC, effective_from DESC, id DESC
LIMIT 1;
