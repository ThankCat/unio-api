ALTER TABLE settlement_recovery_jobs
    DROP COLUMN IF EXISTS price_ratio;

ALTER TABLE price_snapshots
    DROP COLUMN IF EXISTS price_ratio;
