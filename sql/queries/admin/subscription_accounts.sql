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
    apx.status AS proxy_status
FROM subscription_accounts a
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
-- AdminUpdateSubscriptionAccountConfig 修改调度参数（并发、优先级、代理、备注名）与订阅到期时间。
-- 调度参数改变调度行为，故提升 config_revision；调用方须在同事务提升渠道 capacity_revision，
-- 让运行态围栏立即感知（配置热更新传播）。subscription_expires_at 是运营录入的到期预警事实：
-- 上游不提供机读到期时间，唯一写入路径就是这里（缺省 NULL 表示清除/未知）。
UPDATE subscription_accounts
SET display_name = $2,
    proxy_url = $3,
    proxy_id = $4,
    concurrency_limit = $5,
    priority = $6,
    subscription_expires_at = $7,
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

-- name: AdminChannelAccountsLastFailure24h :many
-- AdminChannelAccountsLastFailure24h 每账号近 24 小时最近一次失败（时间 + 错误码），tooltip 展示。
SELECT DISTINCT ON (r.final_account_id)
    r.final_account_id AS account_id,
    r.created_at AS failed_at,
    COALESCE(r.error_code, '') AS error_code
FROM request_records r
WHERE r.final_account_id IN (SELECT sa.id FROM subscription_accounts sa WHERE sa.channel_id = sqlc.arg(channel_id))
  AND r.status = 'failed'
  AND r.created_at > now() - interval '24 hours'
ORDER BY r.final_account_id, r.created_at DESC;
