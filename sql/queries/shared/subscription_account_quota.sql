-- 号池账号的 Codex 用量主动查询与重置卡（admin 手动 / worker 自动共用）。
-- 用量窗口快照本身仍走 UpdateAccountUsageSnapshot（与请求路径观测同一列）；这里只管重置卡快照、
-- 自动用卡配置与运行态。三者都是观测/运行态列，不改 config_revision。

-- name: UpdateAccountResetCreditsSnapshot :exec
-- UpdateAccountResetCreditsSnapshot 写入最近一次主动查用量得到的重置卡快照（只存到期时刻，不存卡 id）。
UPDATE subscription_accounts
SET reset_credits_snapshot = $2,
    updated_at = now()
WHERE id = $1;

-- name: UpdateAccountProfileSnapshot :exec
-- UpdateAccountProfileSnapshot 写入「刷新状态」拿到的上游账号画像（accounts/check + me），展示缓存。
UPDATE subscription_accounts
SET account_profile = $2,
    updated_at = now()
WHERE id = $1;

-- name: UpdateAccountSubscriptionFacts :exec
-- UpdateAccountSubscriptionFacts 用上游权威值回写套餐与订阅到期（accounts/check 的 entitlement）。
-- 任一参数为 NULL 表示该项上游没给，保留现值；到期日原本靠运营手工录入，这里让它随刷新自动更正。
UPDATE subscription_accounts
SET plan_type = COALESCE(sqlc.narg(plan_type), plan_type),
    subscription_expires_at = COALESCE(sqlc.narg(subscription_expires_at), subscription_expires_at),
    updated_at = now()
WHERE id = $1;

-- name: UpdateAccountAutoResetCreditState :exec
-- UpdateAccountAutoResetCreditState 写入自动用卡的脱敏运行态（状态机 + 尝试指纹）。
UPDATE subscription_accounts
SET auto_reset_credit_state = $2,
    updated_at = now()
WHERE id = $1;

-- name: UpdateAccountAutoResetCreditConfig :one
-- UpdateAccountAutoResetCreditConfig 修改账号的自动用卡开关、触发方式（any/all）与 5h/7d 触发阈值
-- （NULL = 该窗口不参与触发）。是运营策略而非调度围栏参数：普通列更新，bump config_revision 只为审计定位。
UPDATE subscription_accounts
SET auto_reset_credit_enabled = $2,
    auto_reset_credit_mode = $3,
    auto_reset_credit_5h_threshold_percent = sqlc.narg(threshold_5h_percent),
    auto_reset_credit_7d_threshold_percent = sqlc.narg(threshold_7d_percent),
    config_revision = config_revision + 1,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ListAutoResetCreditAccounts :many
-- ListAutoResetCreditAccounts 列出开启了自动用卡、启用中且属于池型渠道的账号，供 worker 周期评估。
-- 带最近用量快照与重置卡快照/运行态，评估先看本地快照，只有触顶或快照陈旧才打上游。
SELECT
    a.id,
    a.display_name,
    a.usage_snapshot,
    a.reset_credits_snapshot,
    a.auto_reset_credit_mode,
    a.auto_reset_credit_5h_threshold_percent,
    a.auto_reset_credit_7d_threshold_percent,
    a.auto_reset_credit_state
FROM subscription_accounts a
JOIN channels c ON c.id = a.channel_id
WHERE a.status = 'enabled'
  AND a.auto_reset_credit_enabled = true
  AND c.supply_form = 'pool'
ORDER BY a.id
LIMIT $1;
