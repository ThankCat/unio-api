-- name: GetChannelPrice :one
-- GetChannelPrice 按主键读取单条渠道-模型价。
SELECT * FROM channel_prices WHERE id = sqlc.arg(id) LIMIT 1;
