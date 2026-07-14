-- name: GetUsageRecordByRequest :one
-- GetUsageRecordByRequest 按请求 ID 读取协议无关 usage 记录。
SELECT *
FROM usage_records
WHERE request_record_id = sqlc.arg(request_record_id);

-- 注：admin 用量列表已下线（用量并入请求记录页），此处不再暴露 ListUsageRecordsPage / CountUsageRecords。
-- CreateUsageRecord（结算写入）与 GetUsageRecordByRequest（请求详情）保留。
