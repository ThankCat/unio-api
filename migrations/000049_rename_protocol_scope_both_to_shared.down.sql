UPDATE capability_keys SET protocol_scope = 'both' WHERE protocol_scope = 'shared';

ALTER TABLE capability_keys DROP CONSTRAINT IF EXISTS capability_keys_protocol_scope_check;
ALTER TABLE capability_keys
    ADD CONSTRAINT capability_keys_protocol_scope_check
        CHECK (protocol_scope IN ('both', 'openai', 'anthropic'));

ALTER TABLE capability_keys ALTER COLUMN protocol_scope SET DEFAULT 'both';
