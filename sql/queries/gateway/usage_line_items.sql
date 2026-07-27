-- name: CreateUsageLineItem :one
-- CreateUsageLineItem 创建一条受控附加计量事实。
INSERT INTO usage_line_items (
    usage_record_id,
    kind,
    quantity
)
VALUES (
    sqlc.arg(usage_record_id),
    sqlc.arg(kind),
    sqlc.arg(quantity)
)
RETURNING *;

-- name: ListUsageLineItemsByUsageRecord :many
-- ListUsageLineItemsByUsageRecord 按 usage record ID 读取受控附加计量事实。
SELECT *
FROM usage_line_items
WHERE usage_record_id = sqlc.arg(usage_record_id)
ORDER BY id;
