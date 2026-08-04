ALTER TABLE public.settlement_recovery_jobs
    DROP CONSTRAINT IF EXISTS ck_settlement_recovery_jobs_long_context,
    DROP CONSTRAINT IF EXISTS ck_settlement_recovery_jobs_error_facts,
    DROP CONSTRAINT IF EXISTS ck_settlement_recovery_jobs_attempt_final_status,
    DROP CONSTRAINT IF EXISTS ck_settlement_recovery_jobs_request_final_status,
    DROP COLUMN IF EXISTS long_context_output_multiplier,
    DROP COLUMN IF EXISTS long_context_input_multiplier,
    DROP COLUMN IF EXISTS long_context_threshold,
    DROP COLUMN IF EXISTS long_context_enabled,
    DROP COLUMN IF EXISTS settlement_internal_error_detail,
    DROP COLUMN IF EXISTS settlement_error_message,
    DROP COLUMN IF EXISTS settlement_error_code,
    DROP COLUMN IF EXISTS attempt_final_status,
    DROP COLUMN IF EXISTS request_final_status;
