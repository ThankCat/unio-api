ALTER TABLE api_keys
    DROP COLUMN IF EXISTS spend_limit,
    DROP COLUMN IF EXISTS spent_total;
