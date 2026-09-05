DROP INDEX IF EXISTS public.request_attempts_account_created_idx;
ALTER TABLE public.request_attempts DROP COLUMN IF EXISTS account_id;
