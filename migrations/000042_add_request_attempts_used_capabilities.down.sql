ALTER TABLE settlement_recovery_jobs
    DROP COLUMN IF EXISTS used_capabilities;

ALTER TABLE request_attempts
    DROP COLUMN IF EXISTS used_capabilities;
