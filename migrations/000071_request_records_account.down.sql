-- 回滚请求记录的订阅账号快照列（docs/changes/2026-09-02-account-pool）。
DROP INDEX IF EXISTS public.request_records_final_account_created_idx;

ALTER TABLE public.request_records
    DROP CONSTRAINT IF EXISTS request_records_final_account_id_fkey;

ALTER TABLE public.request_records
    DROP COLUMN IF EXISTS final_account_id;
