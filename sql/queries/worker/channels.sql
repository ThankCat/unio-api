-- name: ListChannelsForCredentialTest :many
-- ListChannelsForCredentialTest 供渠道自动检测 worker 巡检：所有启用渠道（含 credential_valid=false 以便恢复），
-- 失效的排在前面（优先复检以尽快恢复），再按 priority、id。
-- 刻意排除池型渠道：自动巡检的每次探测都是对真实订阅账号的一次真请求，周期性打会白烧
-- 用量窗口且行为特征像机器人（上游风控敏感）。池的健康由请求路径账号反馈 + 手动检测承担。
SELECT c.id, c.provider_id, c.name, c.protocols, c.adapter_key, p.origin, c.credential,
       c.status, c.priority, c.created_at, c.updated_at, c.last_tested_at,
       c.last_test_ok, c.last_test_latency_ms, c.last_test_error, c.credential_valid,
       c.archived_at, c.concurrency_limit,
       c.response_timeout_ms, c.first_token_timeout_ms,
       c.config_revision, c.capacity_revision,
       p.origin_revision AS provider_origin_revision,
       p.status_revision AS provider_status_revision
FROM channels c
JOIN providers p ON p.id = c.provider_id
WHERE c.status = 'enabled' AND p.status = 'enabled'
  AND c.supply_form <> 'pool'
ORDER BY c.credential_valid ASC, c.priority, c.id;
