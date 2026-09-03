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
    (a.credentials ? 'refresh_token') AS has_refresh_token
FROM subscription_accounts a
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
    plan_type, credentials, proxy_url, concurrency_limit, priority,
    status, subscription_expires_at
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9, $10,
    'disabled', $11
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
    concurrency_limit = $4,
    priority = $5,
    subscription_expires_at = $6,
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
