-- name: ListChannelsForCredentialTest :many
-- ListChannelsForCredentialTest 供渠道自动检测 worker 巡检：所有启用渠道（含 credential_valid=false 以便恢复），
-- 失效的排在前面（优先复检以尽快恢复），再按 priority、id。
SELECT id, provider_id, name, protocol, adapter_key, base_url, credential, status, priority, timeout_ms, created_at, updated_at, rpm_limit, tpm_limit, rpd_limit, last_tested_at, last_test_ok, last_test_latency_ms, last_test_error, credential_valid, archived_at, concurrency_limit, upstream_bills_on_disconnect
FROM channels
WHERE status = 'enabled'
ORDER BY credential_valid ASC, priority, id;
