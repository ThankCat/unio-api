-- 账号用量暂停阈值三层继承（2026-09-06）。
--
-- 池型账号的用量自动暂停阈值按「全局 setting → 池型渠道 → 号池账号」三层继承：账号与渠道两层 NULL 表示继承
-- 上一层，1~100 表示覆写；不接受 0（「基本不拦」= 设 100，只在完全打满时暂停）。拦截按账号 usage_snapshot
-- 水位与生效阈值实时判定，任一层阈值变更即刻生效，Redis usage_pause 标记只是展示缓存。
ALTER TABLE public.channels
    ADD COLUMN account_usage_pause_threshold_percent integer;

ALTER TABLE public.channels
    ADD CONSTRAINT channels_account_usage_pause_threshold_percent_check
        CHECK (account_usage_pause_threshold_percent IS NULL
               OR account_usage_pause_threshold_percent BETWEEN 1 AND 100),
    -- 与 account_default_concurrency 同规则：credential 渠道不持该设置（应用层拒绝，DB 兜底）。
    ADD CONSTRAINT channels_account_usage_pause_threshold_pool_only_check
        CHECK (supply_form = 'pool' OR account_usage_pause_threshold_percent IS NULL);

COMMENT ON COLUMN public.channels.account_usage_pause_threshold_percent IS
    '池型渠道下账号的用量暂停阈值（%）。NULL 继承全局 setting；1~100 覆写；不接受 0。credential 渠道恒为 NULL。';

ALTER TABLE public.subscription_accounts
    ADD COLUMN usage_pause_threshold_percent integer;

ALTER TABLE public.subscription_accounts
    ADD CONSTRAINT subscription_accounts_usage_pause_threshold_percent_check
        CHECK (usage_pause_threshold_percent IS NULL
               OR usage_pause_threshold_percent BETWEEN 1 AND 100);

COMMENT ON COLUMN public.subscription_accounts.usage_pause_threshold_percent IS
    '账号用量暂停阈值（%）。NULL 继承渠道（渠道再继承全局）；1~100 覆写；不接受 0。';
