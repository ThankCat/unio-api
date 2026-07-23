-- channel_test_logs：渠道凭据有效性事件历史（worker 巡检 / 手动检测 / 运行时 401 翻失效）。
-- channels 只存当前布尔 credential_valid；本表存 when/why 与检测明细，供详情页回放。

-- name: InsertChannelTestLog :exec
-- InsertChannelTestLog 追加一条检测/凭据事件日志。写入口径由调用方按 R1(b) 决定（失败/跳变才写、手动总写）。
INSERT INTO channel_test_logs (
    channel_id, source, success, error_code, http_status, latency_ms, tested_model, credential_valid_after, message, upstream_error,
    tested_endpoint_base_url_revision, tested_endpoint_status_revision, tested_config_revision, state_change_applied
) VALUES (
    sqlc.arg(channel_id), sqlc.arg(source), sqlc.arg(success), sqlc.narg(error_code),
    sqlc.narg(http_status), sqlc.narg(latency_ms), sqlc.narg(tested_model), sqlc.arg(credential_valid_after), sqlc.narg(message), sqlc.narg(upstream_error),
    sqlc.narg(tested_endpoint_base_url_revision), sqlc.narg(tested_endpoint_status_revision), sqlc.narg(tested_config_revision), sqlc.arg(state_change_applied)
);

-- name: InsertPermissionRecheckLog :execrows
-- 403 Channel-Model 自动复检只写审计，不覆盖 channels.last_test_* 或 credential_valid。
-- credential_valid_after 在 INSERT 时直接读取数据库当前事实，调用方不能猜测；upstream_error 固定 NULL，
-- 禁止把 credential、响应 body 或其它上游敏感内容写入复检日志。
INSERT INTO channel_test_logs (
    channel_id, source, success, error_code, http_status, latency_ms, tested_model, credential_valid_after, message, upstream_error,
    tested_endpoint_base_url_revision, tested_endpoint_status_revision, tested_config_revision, state_change_applied
)
SELECT
    c.id, 'permission_recheck', sqlc.arg(success), sqlc.narg(error_code), sqlc.narg(http_status),
    sqlc.narg(latency_ms), sqlc.narg(tested_model), c.credential_valid, sqlc.narg(message), NULL,
    sqlc.arg(tested_endpoint_base_url_revision), sqlc.arg(tested_endpoint_status_revision),
    sqlc.arg(tested_config_revision), false
FROM channels c
WHERE c.id = sqlc.arg(channel_id);
