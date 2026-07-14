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
