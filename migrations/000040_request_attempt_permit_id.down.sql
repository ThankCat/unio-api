DROP INDEX IF EXISTS public.uq_request_attempts_permit_id;

ALTER TABLE public.request_attempts
    DROP CONSTRAINT IF EXISTS request_attempts_permit_id_check,
    DROP COLUMN IF EXISTS permit_id;
