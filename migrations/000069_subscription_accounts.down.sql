-- 回滚订阅账号实体（docs/changes/2026-09-02-account-pool）。
DROP TABLE IF EXISTS public.subscription_accounts;
DROP SEQUENCE IF EXISTS public.subscription_accounts_id_seq;
