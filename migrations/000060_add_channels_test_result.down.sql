ALTER TABLE channels
    DROP COLUMN IF EXISTS last_tested_at,
    DROP COLUMN IF EXISTS last_test_ok,
    DROP COLUMN IF EXISTS last_test_latency_ms,
    DROP COLUMN IF EXISTS last_test_error;
