-- name: ListEnabledChannelAdapters :many
-- ListEnabledChannelAdapters 列出启用 provider 下启用 channel 的协议与 adapter 注册键，供启动期 preflight 校验 channel 运行时绑定是否被当前进程支持。
SELECT
    c.id AS channel_id,
    c.protocol,
    c.adapter_key,
    p.slug AS provider_slug
FROM channels c
JOIN providers p ON p.id = c.provider_id
WHERE c.status = 'enabled'
  AND p.status = 'enabled'
ORDER BY c.id;

-- name: ListChannelsByProvider :many
-- ListChannelsByProvider 列出指定 provider 下的 channel，按 priority、id 升序。
SELECT id, provider_id, name, protocol, adapter_key, base_url, credential, status, priority, timeout_ms, created_at, updated_at, rpm_limit, tpm_limit, rpd_limit, last_tested_at, last_test_ok, last_test_latency_ms, last_test_error, credential_valid, archived_at, concurrency_limit, upstream_bills_on_disconnect
FROM channels
WHERE provider_id = $1
ORDER BY priority, id;

-- name: ListChannelsPage :many
-- ListChannelsPage 按 provider/状态/关键字过滤后分页列出 channel，连带 provider 名称；过滤项为 NULL 时不过滤。
SELECT
    c.id, c.provider_id, c.name, c.protocol, c.adapter_key, c.base_url,
    c.credential, c.status, c.priority, c.timeout_ms, c.created_at, c.updated_at,
    c.rpm_limit, c.tpm_limit, c.rpd_limit, c.concurrency_limit, c.upstream_bills_on_disconnect,
    c.last_tested_at, c.last_test_ok, c.last_test_latency_ms, c.last_test_error, c.credential_valid,
    p.name AS provider_name
FROM channels c
JOIN providers p ON p.id = c.provider_id
WHERE (sqlc.narg('provider_id')::bigint IS NULL OR c.provider_id = sqlc.narg('provider_id')::bigint)
  AND (sqlc.narg('status')::text IS NULL OR c.status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR c.name ILIKE '%' || sqlc.narg('q')::text || '%'
    OR c.base_url ILIKE '%' || sqlc.narg('q')::text || '%'
  )
ORDER BY c.priority, c.id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountChannels :one
-- CountChannels 返回与 ListChannelsPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM channels c
WHERE (sqlc.narg('provider_id')::bigint IS NULL OR c.provider_id = sqlc.narg('provider_id')::bigint)
  AND (sqlc.narg('status')::text IS NULL OR c.status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR c.name ILIKE '%' || sqlc.narg('q')::text || '%'
    OR c.base_url ILIKE '%' || sqlc.narg('q')::text || '%'
  );

-- name: GetChannel :one
-- GetChannel 按 id 读取单个 channel。
SELECT id, provider_id, name, protocol, adapter_key, base_url, credential, status, priority, timeout_ms, created_at, updated_at, rpm_limit, tpm_limit, rpd_limit, last_tested_at, last_test_ok, last_test_latency_ms, last_test_error, credential_valid, archived_at, concurrency_limit, upstream_bills_on_disconnect
FROM channels
WHERE id = $1
LIMIT 1;

-- name: CreateChannel :one
-- CreateChannel 创建 channel；credential 为明文上游凭据，protocol+adapter_key 复合键须先在 adapter registry 校验存在。
INSERT INTO channels (provider_id, name, protocol, adapter_key, base_url, credential, status, priority, timeout_ms)
VALUES (sqlc.arg(provider_id), sqlc.arg(name), sqlc.arg(protocol), sqlc.arg(adapter_key), sqlc.arg(base_url), sqlc.arg(credential), sqlc.arg(status), sqlc.arg(priority), sqlc.arg(timeout_ms))
RETURNING id, provider_id, name, protocol, adapter_key, base_url, credential, status, priority, timeout_ms, created_at, updated_at, rpm_limit, tpm_limit, rpd_limit, last_tested_at, last_test_ok, last_test_latency_ms, last_test_error, credential_valid, archived_at, concurrency_limit, upstream_bills_on_disconnect;

-- name: UpdateChannel :one
-- UpdateChannel 更新 channel 的展示名、上游地址、启停状态、优先级与超时；protocol、adapter_key 与凭据不在此更新。
UPDATE channels
SET name = sqlc.arg(name), base_url = sqlc.arg(base_url), status = sqlc.arg(status), priority = sqlc.arg(priority), timeout_ms = sqlc.arg(timeout_ms), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, provider_id, name, protocol, adapter_key, base_url, credential, status, priority, timeout_ms, created_at, updated_at, rpm_limit, tpm_limit, rpd_limit, last_tested_at, last_test_ok, last_test_latency_ms, last_test_error, credential_valid, archived_at, concurrency_limit, upstream_bills_on_disconnect;

-- name: SetChannelRateLimits :one
-- SetChannelRateLimits 设置/清除 channel 的渠道级限流上限（P2-8）与在途并发上限（DEC-029）；
-- 各列 NULL=继承全局默认，0=不限，>0=具体上限。
UPDATE channels
SET rpm_limit = sqlc.narg(rpm_limit), tpm_limit = sqlc.narg(tpm_limit), rpd_limit = sqlc.narg(rpd_limit), concurrency_limit = sqlc.narg(concurrency_limit), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, provider_id, name, protocol, adapter_key, base_url, credential, status, priority, timeout_ms, created_at, updated_at, rpm_limit, tpm_limit, rpd_limit, last_tested_at, last_test_ok, last_test_latency_ms, last_test_error, credential_valid, archived_at, concurrency_limit, upstream_bills_on_disconnect;

-- name: SetChannelBillingBehavior :one
-- SetChannelBillingBehavior 设置渠道「断开仍计费」标记（DESIGN-bill-on-cancel 阶段一）。
-- true 表示上游在连接断开后仍会完成生成并计费（sub2api 类中转）；打开后失败/取消路径会记成本敞口。
UPDATE channels
SET upstream_bills_on_disconnect = sqlc.arg(upstream_bills_on_disconnect), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, provider_id, name, protocol, adapter_key, base_url, credential, status, priority, timeout_ms, created_at, updated_at, rpm_limit, tpm_limit, rpd_limit, last_tested_at, last_test_ok, last_test_latency_ms, last_test_error, credential_valid, archived_at, concurrency_limit, upstream_bills_on_disconnect;

-- name: SetChannelTestResult :execrows
-- SetChannelTestResult 写入渠道「最近一次主动检测结果」（渠道检测，阶段一）。
-- last_tested_at 用 DB now() 保证服务器时钟；latency 恒有值（连接失败也测到耗时）；
-- error 成功时为 NULL、失败时为可读原因。不改 updated_at（检测是运营遥测，非配置变更），
-- 也不改 status（阶段一只报告不摘除）。返回受影响行数用于判定渠道是否存在。
UPDATE channels
SET last_tested_at = now(),
    last_test_ok = sqlc.arg(last_test_ok),
    last_test_latency_ms = sqlc.arg(last_test_latency_ms),
    last_test_error = sqlc.narg(last_test_error)
WHERE id = sqlc.arg(id);

-- name: UpdateChannelCredential :execrows
-- UpdateChannelCredential 更新 channel 的明文上游凭据；返回受影响行数用于判定 channel 是否存在。
UPDATE channels
SET credential = sqlc.arg(credential), updated_at = now()
WHERE id = sqlc.arg(id);

-- name: SetChannelCredentialInvalid :execrows
-- SetChannelCredentialInvalid 将渠道标记为「凭据失效」（阶段二闸门）。幂等：仅在 true→false 跳变时
-- 写入并返回受影响行数=1，供调用方据此决定是否补写一条 channel_test_logs（避免重复写日志）。
-- 不改 status（与管理员启停正交），不改 updated_at（这是系统遥测，非配置变更）。
UPDATE channels
SET credential_valid = FALSE
WHERE id = sqlc.arg(id) AND credential_valid = TRUE;

-- name: SetChannelCredentialValid :execrows
-- SetChannelCredentialValid 将渠道恢复为「凭据有效」。幂等：仅在 false→true 跳变时写入并返回受影响行数=1。
UPDATE channels
SET credential_valid = TRUE
WHERE id = sqlc.arg(id) AND credential_valid = FALSE;

-- name: ListChannelsForCredentialTest :many
-- ListChannelsForCredentialTest 供渠道自动检测 worker 巡检：所有启用渠道（含 credential_valid=false 以便恢复），
-- 失效的排在前面（优先复检以尽快恢复），再按 priority、id。
SELECT id, provider_id, name, protocol, adapter_key, base_url, credential, status, priority, timeout_ms, created_at, updated_at, rpm_limit, tpm_limit, rpd_limit, last_tested_at, last_test_ok, last_test_latency_ms, last_test_error, credential_valid, archived_at, concurrency_limit, upstream_bills_on_disconnect
FROM channels
WHERE status = 'enabled'
ORDER BY credential_valid ASC, priority, id;

-- name: ArchiveChannelCascade :execrows
-- ArchiveChannelCascade 归档单个渠道：从所有线路候选池移除（删 route_channels 行）、置 archived、
-- 释放渠道名（追加 __archived_<id> 后缀释放 (provider_id, name) 槽位供复用）。不动 provider。
-- 返回 channels 受影响行数（0 = 渠道不存在或已归档）。恢复保持后缀名、不自动重加线路池。
WITH cleared_pools AS (
    DELETE FROM route_channels WHERE channel_id = sqlc.arg(id)
)
UPDATE channels
SET status = 'archived', archived_at = now(), name = name || '__archived_' || id::text
WHERE channels.id = sqlc.arg(id) AND channels.status <> 'archived';

-- name: RestoreChannel :execrows
-- RestoreChannel 取消归档渠道：archived → disabled（archived_at 清空）。名字保持归档时的后缀名
-- （如需干净名由管理员手动改）。调用方需先保证所属 provider 非 archived（服务层护栏）。
UPDATE channels
SET status = 'disabled', archived_at = NULL
WHERE id = sqlc.arg(id) AND status = 'archived';

-- name: DeleteChannelCascade :execrows
-- DeleteChannelCascade 物理删除 channel，用于清理录错且从未使用的脏数据，并在同一条语句内
-- 级联清理 channel 自身的配置子表：channel_models（模型绑定）、channel_prices（渠道-模型价）。
-- 外键均为默认 NO ACTION（约束在语句末校验），故 CTE 删子表 + 删主体在单条语句内原子完成：
-- 子配置先删除，语句末 channels 的删除不会留下悬挂引用。若 channel 仍被线路池（route_channels）、
-- 请求/账务快照（request_attempts/request_records/cost_snapshots/settlement_recovery_jobs）引用，
-- 整条语句报 23503 全部回滚，上层降级为 conflict，提示改用停用。返回值为 channels 行的受影响数（0 表示 channel 不存在）。
WITH deleted_channel_prices AS (
    DELETE FROM channel_prices WHERE channel_prices.channel_id = sqlc.arg(id)
),
deleted_channel_models AS (
    DELETE FROM channel_models WHERE channel_models.channel_id = sqlc.arg(id)
)
DELETE FROM channels WHERE channels.id = sqlc.arg(id);
