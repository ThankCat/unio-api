ALTER TABLE api_keys
    DROP COLUMN IF EXISTS rpm_limit,
    DROP COLUMN IF EXISTS tpm_limit,
    DROP COLUMN IF EXISTS rpd_limit;
