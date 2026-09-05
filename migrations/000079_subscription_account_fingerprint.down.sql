ALTER TABLE public.subscription_accounts
    DROP CONSTRAINT IF EXISTS subscription_accounts_fingerprint_mode_check,
    DROP COLUMN IF EXISTS fingerprint_seed,
    DROP COLUMN IF EXISTS fingerprint_mode;
