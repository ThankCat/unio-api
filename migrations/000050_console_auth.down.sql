ALTER TABLE public.users
    DROP CONSTRAINT IF EXISTS users_status_check,
    DROP CONSTRAINT IF EXISTS users_uid_key,
    DROP COLUMN IF EXISTS uid,
    DROP COLUMN IF EXISTS status;
