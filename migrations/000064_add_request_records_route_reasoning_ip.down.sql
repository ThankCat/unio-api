ALTER TABLE request_records
    DROP COLUMN IF EXISTS route_id,
    DROP COLUMN IF EXISTS reasoning_effort,
    DROP COLUMN IF EXISTS reasoning_budget_tokens,
    DROP COLUMN IF EXISTS client_ip;
