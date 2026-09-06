ALTER TABLE public.subscription_accounts
    DROP CONSTRAINT IF EXISTS subscription_accounts_auto_reset_credit_window_check,
    DROP CONSTRAINT IF EXISTS subscription_accounts_auto_reset_credit_mode_check,
    DROP CONSTRAINT IF EXISTS subscription_accounts_auto_reset_credit_5h_threshold_check,
    DROP CONSTRAINT IF EXISTS subscription_accounts_auto_reset_credit_7d_threshold_check,
    DROP COLUMN IF EXISTS reset_credits_snapshot,
    DROP COLUMN IF EXISTS auto_reset_credit_enabled,
    DROP COLUMN IF EXISTS auto_reset_credit_mode,
    DROP COLUMN IF EXISTS auto_reset_credit_5h_threshold_percent,
    DROP COLUMN IF EXISTS auto_reset_credit_7d_threshold_percent,
    DROP COLUMN IF EXISTS auto_reset_credit_state;
