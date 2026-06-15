DROP INDEX IF EXISTS idx_api_keys_route_id;
ALTER TABLE projects DROP COLUMN IF EXISTS default_route_id;
ALTER TABLE api_keys DROP COLUMN IF EXISTS route_id;
