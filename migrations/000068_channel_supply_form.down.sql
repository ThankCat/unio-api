-- 回滚渠道供给形态（docs/changes/2026-09-02-account-pool）。
ALTER TABLE public.channels
    DROP CONSTRAINT IF EXISTS channels_account_default_concurrency_pool_only_check,
    DROP CONSTRAINT IF EXISTS channels_account_default_concurrency_check,
    DROP CONSTRAINT IF EXISTS channels_pool_no_credential_check,
    DROP CONSTRAINT IF EXISTS channels_supply_form_check;

ALTER TABLE public.channels
    DROP COLUMN IF EXISTS account_default_concurrency,
    DROP COLUMN IF EXISTS supply_form;

-- 恢复「凭据非空」为全表不变量（回滚后不存在池型渠道，该约束重新无条件成立）。
ALTER TABLE public.channels
    DROP CONSTRAINT IF EXISTS channels_credential_check;

ALTER TABLE public.channels
    ADD CONSTRAINT channels_credential_check CHECK (credential <> '');
