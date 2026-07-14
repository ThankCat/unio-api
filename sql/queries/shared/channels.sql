-- name: SetChannelCredentialInvalid :execrows
-- SetChannelCredentialInvalid 将渠道标记为「凭据失效」（阶段二闸门）。幂等：仅在 true→false 跳变时
-- 写入并返回受影响行数=1，供调用方据此决定是否补写一条 channel_test_logs（避免重复写日志）。
-- 不改 status（与管理员启停正交），不改 updated_at（这是系统遥测，非配置变更）。
UPDATE channels
SET credential_valid = FALSE
WHERE id = sqlc.arg(id) AND credential_valid = TRUE;
