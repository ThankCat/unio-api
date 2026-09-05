-- attempt 级账号归因（2026-09-05）。
--
-- 此前账号维度只有 request_records.final_account_id，且它只在成功/已结算路径写入——失败请求永远不归属
-- 任何账号，导致「渠道成功率（attempt 口径，含上游归责失败）」与「账号成功率（request 口径，只见成功）」
-- 两个数字不可比：单账号号池出现渠道 90.5%、账号 100% 的矛盾展示。
--
-- account_id 在 attempt 创建时随 permit 固化的账号写入（credential 型渠道为 NULL），使账号侧可以用与
-- 渠道完全相同的口径统计成功率与最近失败。不回填存量行：历史 attempt 无法可靠归因（多账号池的
-- fallback 链无从追溯），聚合窗口只有 24h，自然滚动收敛。
ALTER TABLE public.request_attempts
    ADD COLUMN account_id bigint REFERENCES subscription_accounts(id);

COMMENT ON COLUMN public.request_attempts.account_id IS
    'permit 固化的订阅账号（attempt 级归因，创建即写入）；credential 型渠道为 NULL。';

CREATE INDEX request_attempts_account_created_idx
    ON public.request_attempts (account_id, created_at)
    WHERE account_id IS NOT NULL;
