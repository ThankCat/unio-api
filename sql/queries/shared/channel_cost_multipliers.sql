-- name: GetChannelCostMultiplier :one
-- GetChannelCostMultiplier 按主键读取单条价格倍率。
SELECT * FROM channel_cost_multipliers WHERE id = sqlc.arg(id) LIMIT 1;
