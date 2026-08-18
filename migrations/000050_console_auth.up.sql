ALTER TABLE public.users
    ADD COLUMN status text NOT NULL DEFAULT 'active',
    ADD COLUMN uid uuid;

-- Existing users need a public identifier during migration. New Console users
-- provide a UUIDv7 explicitly from the application.
UPDATE public.users
SET uid = gen_random_uuid()
WHERE uid IS NULL;

ALTER TABLE public.users
    ALTER COLUMN uid SET NOT NULL;

ALTER TABLE public.users
    ADD CONSTRAINT users_status_check CHECK (status IN ('active', 'disabled')),
    ADD CONSTRAINT users_uid_key UNIQUE (uid);
