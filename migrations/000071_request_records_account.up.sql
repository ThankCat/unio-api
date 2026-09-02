-- 请求记录携带最终命中的订阅账号（docs/changes/2026-09-02-account-pool 批次一）。
--
-- 有了这一列，「每个号实际摊到的单 token 成本」「账号利用率」「多用户共享一个号的用量归属」
-- 都能离线算出来，不必把估算值写进实时结算链路。credential 型渠道的请求该列恒为 NULL。
--
-- 沿用同表 final_provider_id / final_channel_id 的命名与外键惯例。

ALTER TABLE public.request_records
    -- final_account_id: 最终命中的订阅账号 ID；池型渠道才有值，credential 型渠道为 NULL。--
    ADD COLUMN final_account_id bigint;

ALTER TABLE ONLY public.request_records
    ADD CONSTRAINT request_records_final_account_id_fkey
        FOREIGN KEY (final_account_id) REFERENCES public.subscription_accounts(id);

-- 账号维度的用量与成本归因查询（Admin 账号下钻、离线摊销）。
CREATE INDEX request_records_final_account_created_idx
    ON public.request_records (final_account_id, created_at DESC)
    WHERE final_account_id IS NOT NULL;
