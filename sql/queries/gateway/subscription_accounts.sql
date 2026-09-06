-- name: ListSchedulableAccountsByChannel :many
-- ListSchedulableAccountsByChannel 取某个池型渠道下可调度的账号，供每请求的候选快照聚合并发容量与资格。
-- 这是热路径：只取调度必需列，凭据不在其中（真正出站时再按 permit 固化的账号 ID 单取）。
-- 「可调度」在这里只判账号自身 enabled；渠道与服务商状态由候选查询的上层条件保证（绑定式语义）。
-- 两个用量暂停阈值列（账号 → 渠道，NULL 继承）与 usage_snapshot 一起供候选准备实时判定「水位是否触顶」，
-- 阈值变更因此对下一次请求立即生效，不依赖 Redis 标记刷新。
SELECT
    a.id,
    a.priority,
    a.concurrency_limit,
    a.usage_snapshot,
    a.last_success_at,
    a.config_revision,
    a.usage_pause_threshold_percent,
    c.account_default_concurrency,
    c.account_usage_pause_threshold_percent
FROM subscription_accounts a
JOIN channels c ON c.id = a.channel_id
WHERE a.channel_id = $1
  AND a.status = 'enabled'
ORDER BY a.priority, a.id;

-- name: GetAccountUsagePausePolicy :one
-- GetAccountUsagePausePolicy 取账号与其渠道的用量暂停阈值覆写（NULL 继承），供用量观测时解析生效阈值。
-- 全局层来自 appsettings，不在库里；两列都 NULL 时由调用方回落全局。
SELECT
    a.usage_pause_threshold_percent AS account_threshold_percent,
    c.account_usage_pause_threshold_percent AS channel_threshold_percent
FROM subscription_accounts a
JOIN channels c ON c.id = a.channel_id
WHERE a.id = $1;

-- name: GetAccountOutboundCredential :one
-- GetAccountOutboundCredential 取指定账号的出站凭据、代理、指纹收敛档位与账号级超时覆写，
-- 供 transport 按 permit 固化的账号身份发请求。与 ListSchedulableAccountsByChannel 分开，避免把凭据带进每请求的候选快照。
SELECT
    a.id,
    a.channel_id,
    a.platform,
    a.credential_type,
    a.upstream_account_id,
    a.credentials,
    a.proxy_url,
    a.status,
    a.fingerprint_mode,
    a.fingerprint_seed,
    a.response_timeout_ms,
    a.first_token_timeout_ms,
    apx.url AS proxy_entity_url
FROM subscription_accounts a
LEFT JOIN proxies apx ON apx.id = a.proxy_id AND apx.status = 'enabled'
WHERE a.id = $1;

-- name: UpdateAccountTokens :exec
-- UpdateAccountTokens 写回刷新后的凭据文档。调用方负责「新 refresh token 非空才覆盖」的合并逻辑，
-- 本语句只做整体替换；不动 config_revision——凭据轮换不改变调度参数，无需惊动运行态围栏。
UPDATE subscription_accounts
SET credentials = $2,
    updated_at = now()
WHERE id = $1;

-- name: UpdateAccountUsageSnapshot :exec
-- UpdateAccountUsageSnapshot 写入上游返回的用量窗口快照（primary=5h、secondary=7d，任一可缺失）。
-- 观测写入不改 config_revision，失败也不应影响交付。
UPDATE subscription_accounts
SET usage_snapshot = $2,
    updated_at = now()
WHERE id = $1;

-- name: TouchAccountLastSuccess :exec
-- TouchAccountLastSuccess 记录最近一次完整成功时间，供池内选号的 LRU 排序使用。
UPDATE subscription_accounts
SET last_success_at = $2,
    updated_at = now()
WHERE id = $1;

-- name: MarkAccountDisabled :exec
-- MarkAccountDisabled 在确认令牌吊销或上游风控封禁时永久停用账号（临时不可调度走 Redis 运行态，不落库）。
-- 停用改变可调度性，故提升 config_revision，由调用方在同事务提升渠道 capacity_revision。
UPDATE subscription_accounts
SET status = 'disabled',
    disabled_reason = $2,
    config_revision = config_revision + 1,
    updated_at = now()
WHERE id = $1
  AND status = 'enabled';

-- name: CountSchedulableAccountsByChannel :one
-- CountSchedulableAccountsByChannel 统计渠道下可调度账号数，用于「零账号池不成为候选」与
-- 「池空 ≠ 熔断」的区分展示。
SELECT count(*)
FROM subscription_accounts
WHERE channel_id = $1
  AND status = 'enabled';

-- name: ListAccountsNeedingTokenRefresh :many
-- ListAccountsNeedingTokenRefresh 分页扫描 access token 即将过期的账号，供后台保活刷新（第六节）。
-- 只扫未归档的 oauth 账号；disabled 也刷——运维随时可能启用，启用瞬间就要有新鲜令牌可用。
-- expires_at 缺失（异常凭据）不进扫描：刷不刷都没意义，等请求时兜底路径报错暴露。
SELECT a.id, a.channel_id, a.credentials, a.proxy_url, a.status,
       apx.url AS proxy_entity_url
FROM subscription_accounts a
LEFT JOIN proxies apx ON apx.id = a.proxy_id AND apx.status = 'enabled'
WHERE a.status <> 'archived'
  AND a.credential_type = 'oauth'
  AND a.credentials ? 'refresh_token'
  AND (a.credentials ->> 'expires_at') IS NOT NULL
  AND (a.credentials ->> 'expires_at')::timestamptz < now() + make_interval(secs => sqlc.arg(within_seconds)::bigint)
ORDER BY (a.credentials ->> 'expires_at')::timestamptz
LIMIT sqlc.arg(page_limit);

-- name: GetAccountByPlatformUpstreamID :one
-- GetAccountByPlatformUpstreamID 按全局唯一键定位账号，供重复导入提示「已存在于哪个池」。
SELECT a.id, a.channel_id, a.display_name, a.status, c.name AS channel_name
FROM subscription_accounts a
JOIN channels c ON c.id = a.channel_id
WHERE a.platform = $1
  AND a.upstream_account_id = $2;

-- name: GetEnabledProxyURL :one
-- GetEnabledProxyURL 取启用代理的规范 URL（disabled/不存在返回 no rows → 调用方按直连处理）。
SELECT url FROM proxies WHERE id = $1 AND status = 'enabled';

-- name: AddAccountLifetimeStats :exec
-- AddAccountLifetimeStats 在首次结算事务内为归属账号增量累加生命周期计数（「用量」列数据源）。
-- 幂等保障来自结算的 running 状态闸门：重放在进入本语句前已短路，绝不重复累加。
-- 口径与账号 24H 聚合一致（只统计已归属账号的结算终态）；token 为账单口径，金额为客户实扣。
INSERT INTO subscription_account_stats (
    account_id, lifetime_requests, lifetime_input_tokens, lifetime_output_tokens, lifetime_sale_amount, updated_at
)
VALUES (sqlc.arg(account_id), 1, sqlc.arg(input_tokens), sqlc.arg(output_tokens), sqlc.arg(sale_amount), now())
ON CONFLICT (account_id) DO UPDATE SET
    lifetime_requests = subscription_account_stats.lifetime_requests + 1,
    lifetime_input_tokens = subscription_account_stats.lifetime_input_tokens + EXCLUDED.lifetime_input_tokens,
    lifetime_output_tokens = subscription_account_stats.lifetime_output_tokens + EXCLUDED.lifetime_output_tokens,
    lifetime_sale_amount = subscription_account_stats.lifetime_sale_amount + EXCLUDED.lifetime_sale_amount,
    updated_at = now();
