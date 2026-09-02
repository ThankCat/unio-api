-- 回滚订阅费用台账（docs/changes/2026-09-02-account-pool）。
DROP TABLE IF EXISTS public.subscription_ledger_entries;
DROP SEQUENCE IF EXISTS public.subscription_ledger_entries_id_seq;
