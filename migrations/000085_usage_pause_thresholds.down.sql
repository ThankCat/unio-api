ALTER TABLE public.subscription_accounts
    DROP CONSTRAINT IF EXISTS subscription_accounts_usage_pause_threshold_percent_check,
    DROP COLUMN IF EXISTS usage_pause_threshold_percent;

ALTER TABLE public.channels
    DROP CONSTRAINT IF EXISTS channels_account_usage_pause_threshold_pool_only_check,
    DROP CONSTRAINT IF EXISTS channels_account_usage_pause_threshold_percent_check,
    DROP COLUMN IF EXISTS account_usage_pause_threshold_percent;
