-- settlement 补偿任务携带账号快照（docs/changes/2026-09-02-account-pool 批次七）。
--
-- 重放 settlement 时 MarkRequestSucceeded 会重写 request_records.final_account_id：
-- job 不带账号快照的话，池型请求经补偿收口后账号归因会被 NULL 冲掉。
-- 与 request_records.final_account_id 同口径：可空、不加外键（不阻碍账号归档）。

ALTER TABLE public.settlement_recovery_jobs
    -- account_id: 中标订阅账号快照；credential 型渠道为 NULL。--
    ADD COLUMN account_id bigint;
