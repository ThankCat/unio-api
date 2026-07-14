-- name: GetPriceSnapshotByRequest :one
-- GetPriceSnapshotByRequest 按请求 ID 读取客户售价快照。
SELECT *
FROM price_snapshots
WHERE request_record_id = sqlc.arg(request_record_id);
