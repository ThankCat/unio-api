-- 号池账号级上游超时（2026-09-05）。
--
-- 响应超时与首字超时按「全局默认 → 渠道行 → 号池账号」三层继承：每层 NULL 继承上一层、显式 0 表示不限制、
-- 正数覆写；全局默认为 0。账号层让同一渠道下不同账号（不同套餐/代理出口）可以单独放宽或收紧，
-- 规则与 channels.response_timeout_ms / first_token_timeout_ms 完全一致。
ALTER TABLE public.subscription_accounts
    ADD COLUMN response_timeout_ms integer,
    ADD COLUMN first_token_timeout_ms integer;

ALTER TABLE public.subscription_accounts
    ADD CONSTRAINT subscription_accounts_response_timeout_ms_check
        CHECK (response_timeout_ms IS NULL OR response_timeout_ms >= 0),
    ADD CONSTRAINT subscription_accounts_first_token_timeout_ms_check
        CHECK (first_token_timeout_ms IS NULL OR first_token_timeout_ms >= 0);

COMMENT ON COLUMN public.subscription_accounts.response_timeout_ms IS
    'NULL 继承渠道；0 不限制；正数覆写。非流式限制完整响应；流式限制收到上游响应头。';
COMMENT ON COLUMN public.subscription_accounts.first_token_timeout_ms IS
    'NULL 继承渠道；0 不限制；正数覆写。限制流式首个上游进展。';
