-- name: GetCostSnapshotByRequest :one
-- GetCostSnapshotByRequest 按请求 ID 读取上游成本快照。
SELECT *
FROM cost_snapshots
WHERE request_record_id = sqlc.arg(request_record_id);
