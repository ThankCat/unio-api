-- channel_test_logs：渠道凭据有效性事件历史（worker 巡检 / 手动检测 / 运行时 401 翻失效）。
-- channels 只存当前布尔 credential_valid；本表存 when/why 与检测明细，供详情页回放。

-- name: InsertChannelTestLog :exec
-- InsertChannelTestLog 追加一条检测/凭据事件日志。写入口径由调用方按 R1(b) 决定（失败/跳变才写、手动总写）。
INSERT INTO channel_test_logs (
    channel_id, source, success, error_code, http_status, latency_ms, tested_model, credential_valid_after, message, upstream_error
) VALUES (
    sqlc.arg(channel_id), sqlc.arg(source), sqlc.arg(success), sqlc.narg(error_code),
    sqlc.narg(http_status), sqlc.narg(latency_ms), sqlc.narg(tested_model), sqlc.arg(credential_valid_after), sqlc.narg(message), sqlc.narg(upstream_error)
);

-- name: ListChannelTestLogsByChannel :many
-- ListChannelTestLogsByChannel 按渠道倒序分页返回检测日志（详情页「检测日志」区块）。
SELECT id, channel_id, created_at, source, success, error_code, http_status, latency_ms, tested_model, credential_valid_after, message, upstream_error
FROM channel_test_logs
WHERE channel_id = sqlc.arg(channel_id)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountChannelTestLogsByChannel :one
-- CountChannelTestLogsByChannel 返回某渠道检测日志总数（分页用）。
SELECT COUNT(*) AS total
FROM channel_test_logs
WHERE channel_id = sqlc.arg(channel_id);

-- name: DeleteChannelTestLogsBeyondPerChannel :execrows
-- DeleteChannelTestLogsBeyondPerChannel 保留每渠道最近 keep 条，删除更旧的（R1：默认 200）。
-- worker 每轮末尾对本轮涉及的渠道逐一调用；用窗口函数按 (channel_id) 分区、created_at/id 倒序打名次，删名次 > keep 的行。
DELETE FROM channel_test_logs AS del
WHERE del.channel_id = sqlc.arg(channel_id)
  AND del.id NOT IN (
      SELECT ctl.id
      FROM channel_test_logs ctl
      WHERE ctl.channel_id = sqlc.arg(channel_id)
      ORDER BY ctl.created_at DESC, ctl.id DESC
      LIMIT sqlc.arg(keep)
  );
