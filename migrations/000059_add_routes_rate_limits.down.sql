ALTER TABLE routes
    DROP COLUMN IF EXISTS rpm_limit,
    DROP COLUMN IF EXISTS tpm_limit,
    DROP COLUMN IF EXISTS rpd_limit;
