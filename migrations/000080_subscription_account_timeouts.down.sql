ALTER TABLE public.subscription_accounts
    DROP CONSTRAINT IF EXISTS subscription_accounts_response_timeout_ms_check,
    DROP CONSTRAINT IF EXISTS subscription_accounts_first_token_timeout_ms_check,
    DROP COLUMN IF EXISTS response_timeout_ms,
    DROP COLUMN IF EXISTS first_token_timeout_ms;
