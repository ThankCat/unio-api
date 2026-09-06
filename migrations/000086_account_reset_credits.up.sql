-- 号池账号的 Codex 重置卡（2026-09-06）。
--
-- 上游为 ChatGPT/Codex 账号发放 rate-limit reset credit（一张卡同时重置 5h 与 7d 窗口）。本次引入：
--   - reset_credits_snapshot：最近一次主动查用量拿到的可用卡数与到期明细（展示缓存，只存到期时刻，不存卡 id）；
--   - auto_reset_credit_*：账号级「自动使用重置卡」开关、5h/7d 各自的触发阈值（NULL = 该窗口不作为触发条件，
--     1~100）与触发方式（any = 任一达到即用卡，all = 全部已设窗口同时达到才用卡）；
--   - auto_reset_credit_state：自动用卡的脱敏运行态（状态机、触发窗口、尝试指纹），供管理端展示与重试幂等。
--
-- 一张卡同时重置 5h 与 7d：5h 几小时内自己就恢复，只因 5h 打满就用卡会浪费周重置的价值；
-- 但 7d 打满时账号会被锁到下周。所以窗口是否参与、多个窗口是「或」还是「与」由运营按账号自行决定。
ALTER TABLE public.subscription_accounts
    ADD COLUMN reset_credits_snapshot jsonb,
    ADD COLUMN auto_reset_credit_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN auto_reset_credit_mode text NOT NULL DEFAULT 'any',
    ADD COLUMN auto_reset_credit_5h_threshold_percent integer,
    ADD COLUMN auto_reset_credit_7d_threshold_percent integer,
    ADD COLUMN auto_reset_credit_state jsonb;

ALTER TABLE public.subscription_accounts
    ADD CONSTRAINT subscription_accounts_auto_reset_credit_mode_check
        CHECK (auto_reset_credit_mode IN ('any', 'all')),
    ADD CONSTRAINT subscription_accounts_auto_reset_credit_5h_threshold_check
        CHECK (auto_reset_credit_5h_threshold_percent IS NULL
               OR auto_reset_credit_5h_threshold_percent BETWEEN 1 AND 100),
    ADD CONSTRAINT subscription_accounts_auto_reset_credit_7d_threshold_check
        CHECK (auto_reset_credit_7d_threshold_percent IS NULL
               OR auto_reset_credit_7d_threshold_percent BETWEEN 1 AND 100),
    -- 开启时至少要有一个窗口参与触发，否则开关形同虚设（应用层同样校验，DB 兜底）。
    ADD CONSTRAINT subscription_accounts_auto_reset_credit_window_check
        CHECK (auto_reset_credit_enabled = false
               OR auto_reset_credit_5h_threshold_percent IS NOT NULL
               OR auto_reset_credit_7d_threshold_percent IS NOT NULL);

COMMENT ON COLUMN public.subscription_accounts.reset_credits_snapshot IS
    '最近一次主动查用量得到的重置卡快照：{available_count, applicable_available_count, credits:[{expires_at}], fetched_at}。展示缓存，不存卡 id。';
COMMENT ON COLUMN public.subscription_accounts.auto_reset_credit_enabled IS
    '自动使用重置卡：按 mode 与已设窗口阈值判定触发，账号持有可用卡时自动消费最早到期的一张。默认关闭。';
COMMENT ON COLUMN public.subscription_accounts.auto_reset_credit_mode IS
    '多窗口触发方式：any = 任一已设窗口达到阈值即用卡；all = 全部已设窗口同时达到才用卡。';
COMMENT ON COLUMN public.subscription_accounts.auto_reset_credit_5h_threshold_percent IS
    '自动用卡的 5h 窗口触发阈值（%）。NULL = 5h 不作为触发条件；1~100。';
COMMENT ON COLUMN public.subscription_accounts.auto_reset_credit_7d_threshold_percent IS
    '自动用卡的 7d 窗口触发阈值（%）。NULL = 7d 不作为触发条件；1~100。';
COMMENT ON COLUMN public.subscription_accounts.auto_reset_credit_state IS
    '自动用卡运行态（脱敏）：{status, trigger_window, available_count, checked_at, last_result_at, error_code, attempt_cycle_hash, attempt_credit_hash}。';
