DROP INDEX IF EXISTS public.idx_request_records_client_thread_created_at;

ALTER TABLE public.request_records
    DROP COLUMN IF EXISTS client_thread_id,
    DROP COLUMN IF EXISTS client_turn_id,
    DROP COLUMN IF EXISTS client_request_kind;
