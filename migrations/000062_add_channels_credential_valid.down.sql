DROP INDEX IF EXISTS idx_channels_credential_invalid;

ALTER TABLE channels
    DROP COLUMN IF EXISTS credential_valid;
