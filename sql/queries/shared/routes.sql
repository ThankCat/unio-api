-- name: GetRouteByID :one
-- GetRouteByID 按主键读取单条线路。
SELECT * FROM routes WHERE id = sqlc.arg(id) LIMIT 1;
