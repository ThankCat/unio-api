-- name: GetChannelRechargeFactor :one
-- GetChannelRechargeFactor 按主键读取单条充值倍率。
SELECT * FROM channel_recharge_factors WHERE id = sqlc.arg(id) LIMIT 1;
