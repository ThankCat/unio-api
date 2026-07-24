-- name: ListChannelsForCredentialTest :many
-- ListChannelsForCredentialTest 供渠道自动检测 worker 巡检：所有启用渠道（含 credential_valid=false 以便恢复），
-- 失效的排在前面（优先复检以尽快恢复），再按 priority、id。
SELECT c.id, c.provider_id, c.provider_origin_id, c.name, c.protocol, c.adapter_key, pe.base_url, c.credential, c.status, c.priority, c.timeout_ms, c.created_at, c.updated_at, c.rpm_limit, c.tpm_limit, c.rpd_limit, c.last_tested_at, c.last_test_ok, c.last_test_latency_ms, c.last_test_error, c.credential_valid, c.archived_at, c.concurrency_limit, c.upstream_bills_on_disconnect, c.config_revision, c.admission_limits_revision, pe.base_url_revision AS provider_origin_base_url_revision, pe.status_revision AS provider_origin_status_revision
FROM channels c
JOIN provider_origins pe ON pe.id = c.provider_origin_id
WHERE c.status = 'enabled'
ORDER BY c.credential_valid ASC, c.priority, c.id;
