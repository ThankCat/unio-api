-- name: AdminListSubscriptionAccounts :many
-- AdminListSubscriptionAccounts 列出渠道下全部账号（含已停用与归档），供渠道详情的账号页签。
-- 返回凭据摘要而非凭据本身：列表页不需要令牌明文，减少泄露面。
SELECT
    a.id,
    a.channel_id,
    a.platform,
    a.credential_type,
    a.upstream_account_id,
    a.display_name,
    a.plan_type,
    a.proxy_url,
    a.concurrency_limit,
    a.priority,
    a.status,
    a.disabled_reason,
    a.subscription_expires_at,
    a.usage_snapshot,
    a.last_success_at,
    a.config_revision,
    a.created_at,
    a.updated_at,
    (a.credentials ->> 'expires_at') AS token_expires_at,
    (a.credentials ? 'refresh_token') AS has_refresh_token,
    (a.credentials ->> 'email') AS email,
    a.proxy_id,
    apx.name AS proxy_name,
    apx.status AS proxy_status,
    a.fingerprint_mode,
    a.response_timeout_ms,
    a.first_token_timeout_ms,
    a.usage_pause_threshold_percent,
    c.account_usage_pause_threshold_percent AS channel_usage_pause_threshold_percent,
    a.reset_credits_snapshot,
    a.auto_reset_credit_enabled,
    a.auto_reset_credit_mode,
    a.auto_reset_credit_5h_threshold_percent,
    a.auto_reset_credit_7d_threshold_percent,
    a.auto_reset_credit_state,
    a.account_profile
FROM subscription_accounts a
JOIN channels c ON c.id = a.channel_id
LEFT JOIN proxies apx ON apx.id = a.proxy_id
WHERE a.channel_id = $1
  AND (sqlc.narg('status')::text IS NULL OR a.status = sqlc.narg('status')::text)
-- 运维视角排序：在役（enabled）最前，其次停用，归档垫底；同层按优先级与 ID 稳定排序。
-- 不能直接 ORDER BY status——那是字母序，archived 会排最前。
ORDER BY CASE a.status WHEN 'enabled' THEN 0 WHEN 'disabled' THEN 1 ELSE 2 END, a.priority, a.id;

-- name: AdminGetSubscriptionAccount :one
-- AdminGetSubscriptionAccount 取单个账号详情（含凭据，与渠道凭据同权：管理端可查看轮换）。
SELECT *
FROM subscription_accounts
WHERE id = $1;

-- name: AdminCreateSubscriptionAccount :one
-- AdminCreateSubscriptionAccount 落库新账号。导入一律落 disabled，由管理员显式启用。
-- 重复导入由 (platform, upstream_account_id) 唯一键在数据库层拒绝，不依赖应用层先查后插。
INSERT INTO subscription_accounts (
    channel_id, platform, credential_type, upstream_account_id, display_name,
    plan_type, credentials, proxy_url, proxy_id, concurrency_limit, priority,
    status, subscription_expires_at
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9, $10, $11,
    'disabled', $12
)
RETURNING *;

-- name: AdminUpdateSubscriptionAccountConfig :one
-- AdminUpdateSubscriptionAccountConfig 修改调度参数（并发、优先级、代理、备注名）、订阅到期时间与账号级超时覆写。
-- 调度参数改变调度行为，故提升 config_revision；调用方须在同事务提升渠道 capacity_revision，
-- 让运行态围栏立即感知（配置热更新传播）。subscription_expires_at 是运营录入的到期预警事实：
-- 上游不提供机读到期时间，唯一写入路径就是这里（缺省 NULL 表示清除/未知）。
-- 超时两列：NULL 继承渠道、0 不限制、正数覆写（与渠道行同语义）。
UPDATE subscription_accounts
SET display_name = $2,
    proxy_url = $3,
    proxy_id = $4,
    concurrency_limit = $5,
    priority = $6,
    subscription_expires_at = $7,
    response_timeout_ms = $8,
    first_token_timeout_ms = $9,
    config_revision = config_revision + 1,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: AdminUpdateSubscriptionAccountUsagePauseThreshold :one
-- AdminUpdateSubscriptionAccountUsagePauseThreshold 单独修改账号用量暂停阈值（NULL 继承渠道，1~100 覆写，不接受 0）。
-- 候选快照每请求读库，普通列更新即热生效，不经渠道容量 control 发布；bump config_revision 让审计能定位
-- 「这次是按哪版配置放行的」。调用方随后按快照重算该账号的 Redis 暂停标记（展示缓存）。
UPDATE subscription_accounts
SET usage_pause_threshold_percent = sqlc.narg(usage_pause_threshold_percent),
    config_revision = config_revision + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: AdminListUsagePauseReconcileAccounts :many
-- AdminListUsagePauseReconcileAccounts 列出需要按阈值重算用量暂停标记的账号：启用中且属于池型渠道，
-- 带最近用量快照与两层阈值覆写。channel_id / account_id 任一给定即收窄范围（全局阈值变更 → 全量，
-- 渠道阈值变更 → 该渠道，账号阈值变更 → 该账号）。
SELECT
    a.id,
    a.channel_id,
    a.usage_snapshot,
    a.usage_pause_threshold_percent,
    c.account_usage_pause_threshold_percent AS channel_usage_pause_threshold_percent
FROM subscription_accounts a
JOIN channels c ON c.id = a.channel_id
WHERE a.status = 'enabled'
  AND c.supply_form = 'pool'
  AND (sqlc.narg('channel_id')::bigint IS NULL OR a.channel_id = sqlc.narg('channel_id')::bigint)
  AND (sqlc.narg('account_id')::bigint IS NULL OR a.id = sqlc.narg('account_id')::bigint)
ORDER BY a.channel_id, a.id;

-- name: AdminSetSubscriptionAccountFingerprint :one
-- AdminSetSubscriptionAccountFingerprint 切换账号指纹收敛档位。种子只在首次需要时生成（已有则保留，
-- 切回 off 也不清空），保证切回收敛时设备身份不变。收敛档位改变出站身份，提升 config_revision 让
-- 运行态围栏与请求路径的账号快照失效重取。
UPDATE subscription_accounts
SET fingerprint_mode = $2,
    fingerprint_seed = COALESCE(fingerprint_seed, sqlc.narg('seed')::uuid),
    config_revision = config_revision + 1,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: AdminSetSubscriptionAccountStatus :one
-- AdminSetSubscriptionAccountStatus 启停或归档账号。状态影响可调度性，提升 config_revision。
-- 归档不可逆到 enabled：恢复统一落 disabled，由调用方分两步执行（与 Provider/Channel 恢复一致）。
UPDATE subscription_accounts
SET status = $2,
    disabled_reason = $3,
    config_revision = config_revision + 1,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: AdminReauthorizeSubscriptionAccount :one
-- AdminReauthorizeSubscriptionAccount 用重新授权得到的新凭据覆盖既有账号（边界 21：续命是高频场景，
-- 不应走删除重建）。按 (platform, upstream_account_id) 定位，保留其调度参数与台账关联。
UPDATE subscription_accounts
SET credentials = $3,
    plan_type = COALESCE($4, plan_type),
    subscription_expires_at = COALESCE($5, subscription_expires_at),
    updated_at = now()
WHERE platform = $1
  AND upstream_account_id = $2
RETURNING *;

-- name: AdminCountAccountsByChannel :one
-- AdminCountAccountsByChannel 渠道账号聚合，供账号页签概览区与「池空 ≠ 熔断」的区分。
SELECT
    count(*) AS total,
    count(*) FILTER (WHERE status = 'enabled') AS enabled,
    count(*) FILTER (WHERE status = 'disabled') AS disabled,
    count(*) FILTER (WHERE status = 'archived') AS archived,
    count(*) FILTER (WHERE status <> 'archived'
        AND subscription_expires_at IS NOT NULL
        AND subscription_expires_at < now() + interval '7 days') AS expiring_soon
FROM subscription_accounts
WHERE channel_id = $1;

-- name: AdminListSubscriptionLedger :many
-- AdminListSubscriptionLedger 列出账号的订阅费用台账，供摊销与利用率的离线计算与页面展示。
SELECT *
FROM subscription_ledger_entries
WHERE account_id = $1
ORDER BY period_start DESC, id DESC;

-- name: AdminCreateSubscriptionLedgerEntry :one
-- AdminCreateSubscriptionLedgerEntry 录入一期订阅费用（续费一次写一行）。
INSERT INTO subscription_ledger_entries (
    account_id, amount, currency, period_start, period_end, note, created_by
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
RETURNING *;

-- name: AdminDeleteSubscriptionAccountCascade :execrows
-- AdminDeleteSubscriptionAccountCascade 物理删除账号，用于清理录错/试错的脏数据。
-- 台账是账号自身的运营录入（非请求账务事实），随账号一并删除。
-- 若账号已被请求历史引用（request_records.final_account_id NO ACTION 外键），
-- 语句报 23503 整体回滚，上层降级为 conflict 提示保持归档——保住账务归因链路。
WITH deleted_ledger AS (
    DELETE FROM subscription_ledger_entries
    WHERE subscription_ledger_entries.account_id = sqlc.arg(id)
)
DELETE FROM subscription_accounts WHERE subscription_accounts.id = sqlc.arg(id);

-- name: AdminListPoolChannels :many
-- AdminListPoolChannels 列出全部未归档池型渠道（号池并发监测的钻取骨架：Provider → 池 → 账号）。
SELECT c.id, c.name, c.status, c.account_default_concurrency,
       p.id AS provider_id, p.name AS provider_name
FROM channels c
JOIN providers p ON p.id = c.provider_id
WHERE c.supply_form = 'pool'
  AND c.status <> 'archived'
ORDER BY c.priority, c.id;

-- name: AdminPickProbeAccount :one
-- AdminPickProbeAccount 为渠道检测选一个账号：未指定账号时取「调度视角最优先」的在役账号
-- （priority 小者优先，同档按 ID 稳定），保证检测走的号与真实调度大概率一致。
SELECT id
FROM subscription_accounts
WHERE channel_id = $1
  AND status = 'enabled'
ORDER BY priority, id
LIMIT 1;

-- name: AdminChannelAccountsUsage24h :many
-- AdminChannelAccountsUsage24h 按账号聚合近 24 小时的请求/成功/失败/取消/token/延迟（渠道账号页「24H」列）。
-- total_requests 包含已归属账号的全部终态记录（含 canceled）；running 尚未写入 final_account_id，不在此聚合。
-- token 口径与请求详情一致：全部输入形态 + 输出总量；延迟只统计成功请求（失败的时长无意义）。
SELECT
    r.final_account_id AS account_id,
    count(*)::bigint AS total_requests,
    (count(*) FILTER (WHERE r.status = 'succeeded'))::bigint AS succeeded_requests,
    (count(*) FILTER (WHERE r.status = 'failed'))::bigint AS failed_requests,
    (count(*) FILTER (WHERE r.status = 'canceled'))::bigint AS canceled_requests,
    COALESCE(sum(
        ur.uncached_input_tokens + ur.cache_read_input_tokens
        + ur.cache_creation_5m_input_tokens + ur.cache_creation_1h_input_tokens
        + ur.cache_creation_30m_input_tokens + ur.output_tokens_total
    ), 0)::bigint AS total_tokens,
    COALESCE(avg(EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)
        FILTER (WHERE r.status = 'succeeded' AND r.completed_at IS NOT NULL AND r.started_at IS NOT NULL), 0)::bigint AS avg_latency_ms,
    COALESCE(avg(EXTRACT(EPOCH FROM (r.gateway_first_token_at - r.started_at)) * 1000)
        FILTER (WHERE r.status = 'succeeded' AND r.gateway_first_token_at IS NOT NULL AND r.started_at IS NOT NULL), 0)::bigint AS avg_first_token_ms
FROM request_records r
LEFT JOIN usage_records ur ON ur.request_record_id = r.id
WHERE r.final_account_id IN (SELECT sa.id FROM subscription_accounts sa WHERE sa.channel_id = sqlc.arg(channel_id))
  AND r.created_at > now() - interval '24 hours'
GROUP BY r.final_account_id;

-- name: AdminChannelAccountsSale24h :many
-- AdminChannelAccountsSale24h 按账号 + 币种聚合近 24 小时净扣费（售卖额，与工作台 revenue 同口径 debit 族 − 冲正）。
SELECT
    r.final_account_id AS account_id,
    le.currency,
    COALESCE(sum(CASE
        WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
        WHEN le.entry_type IN ('refund', 'adjustment_credit') THEN -le.amount
        ELSE 0
    END), 0)::numeric AS sale_amount
FROM ledger_entries le
JOIN request_records r ON r.id = le.request_record_id
WHERE r.final_account_id IN (SELECT sa.id FROM subscription_accounts sa WHERE sa.channel_id = sqlc.arg(channel_id))
  AND le.created_at > now() - interval '24 hours'
GROUP BY r.final_account_id, le.currency;

-- name: AdminChannelAccountsAttempts24h :many
-- AdminChannelAccountsAttempts24h 按账号聚合近 24 小时的 attempt 成功率事实，口径与渠道运维表完全一致
-- （分母 = 成功 + 上游归责失败；客户取消、平台侧故障不计入）。账号归因来自 attempt 级 account_id
-- （创建即写入），失败也能归到号——request 级 final_account_id 只在成功路径写入，不能用来算成功率。
SELECT
    a.account_id,
    (count(*) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream'))::bigint AS attempt_total,
    (count(*) FILTER (WHERE a.status = 'succeeded'))::bigint AS attempt_succeeded
FROM request_attempts a
WHERE a.account_id IN (SELECT sa.id FROM subscription_accounts sa WHERE sa.channel_id = sqlc.arg(channel_id))
  AND a.created_at > now() - interval '24 hours'
GROUP BY a.account_id;

-- name: AdminChannelAccountsLastFailure24h :many
-- AdminChannelAccountsLastFailure24h 每账号近 24 小时最近一次失败 attempt（时间 + 错误码），tooltip 展示。
-- 按 attempt 级 account_id 归因：请求级 final_account_id 不写失败，永远查不到失败记录。
SELECT DISTINCT ON (a.account_id)
    a.account_id,
    a.created_at AS failed_at,
    COALESCE(a.error_code, '') AS error_code
FROM request_attempts a
WHERE a.account_id IN (SELECT sa.id FROM subscription_accounts sa WHERE sa.channel_id = sqlc.arg(channel_id))
  AND a.status = 'failed'
  AND a.created_at > now() - interval '24 hours'
ORDER BY a.account_id, a.created_at DESC;

-- name: AdminChannelAccountsLifetimeStats :many
-- AdminChannelAccountsLifetimeStats 读取渠道下各账号的生命周期累计（「用量」列）。
-- 数据由结算路径增量累加（subscription_account_stats），O(1) 读取，绝不做请求表全时聚合。
SELECT s.account_id, s.lifetime_requests, s.lifetime_input_tokens, s.lifetime_output_tokens, s.lifetime_sale_amount
FROM subscription_account_stats s
WHERE s.account_id IN (SELECT sa.id FROM subscription_accounts sa WHERE sa.channel_id = sqlc.arg(channel_id));
