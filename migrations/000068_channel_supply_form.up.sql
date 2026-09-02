-- 渠道供给形态：单凭据 / 订阅账号池（docs/changes/2026-09-02-account-pool 批次一）。
--
-- 号池把「一份凭据」的渠道扩展为「一池订阅账号」：池型渠道自身不持凭据，协议、adapter、模型绑定、
-- 定价与超时仍在渠道层，凭据、并发、健康与用量下沉到账号（见 000069_subscription_accounts）。
--
-- 存量行全部落 credential，行为不变——这是本批次的硬约束。

ALTER TABLE public.channels
    -- supply_form: 供给形态。credential=渠道持单份 API Key（存量形态）；pool=渠道下挂订阅账号池。--
    ADD COLUMN supply_form text DEFAULT 'credential' NOT NULL,
    -- account_default_concurrency: 池型渠道的账号默认并发上限。NULL 继承全局默认，0 不限，正数为上限；
    -- 账号自身 concurrency_limit 为 NULL 时回落到本列。credential 型渠道该列无意义，恒为 NULL。--
    ADD COLUMN account_default_concurrency integer;

-- 「凭据非空」原本是全表不变量，但它只对 credential 型成立：池型渠道的凭据在账号上，渠道自身必须为空。
-- 不放宽这条，池型渠道与下面的 channels_pool_no_credential_check 互相矛盾，一行都插不进去。
ALTER TABLE public.channels
    DROP CONSTRAINT IF EXISTS channels_credential_check;

ALTER TABLE public.channels
    ADD CONSTRAINT channels_credential_check
        CHECK (supply_form = 'pool' OR credential <> ''),
    ADD CONSTRAINT channels_supply_form_check
        CHECK (supply_form IN ('credential', 'pool')),
    -- 池型渠道不持凭据：credential 必须为空串，避免「渠道有一份凭据、账号又各有一份」的双份真相。--
    ADD CONSTRAINT channels_pool_no_credential_check
        CHECK (supply_form <> 'pool' OR credential = ''),
    -- 账号默认并发沿用全局取值语义：NULL 继承 / 0 不限 / 正数上限，不接受负数。--
    ADD CONSTRAINT channels_account_default_concurrency_check
        CHECK (account_default_concurrency IS NULL OR account_default_concurrency >= 0),
    -- 该列只对池型渠道有意义，credential 型必须留空。--
    ADD CONSTRAINT channels_account_default_concurrency_pool_only_check
        CHECK (supply_form = 'pool' OR account_default_concurrency IS NULL);
