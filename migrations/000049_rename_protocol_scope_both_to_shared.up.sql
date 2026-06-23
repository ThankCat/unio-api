-- protocol_scope：both → shared（语义「双协议通用」，Admin 展示为「通用」）。
ALTER TABLE capability_keys DROP CONSTRAINT IF EXISTS capability_keys_protocol_scope_check;

UPDATE capability_keys SET protocol_scope = 'shared' WHERE protocol_scope = 'both';

ALTER TABLE capability_keys
    ADD CONSTRAINT capability_keys_protocol_scope_check
        CHECK (protocol_scope IN ('shared', 'openai', 'anthropic'));

ALTER TABLE capability_keys ALTER COLUMN protocol_scope SET DEFAULT 'shared';

COMMENT ON COLUMN capability_keys.protocol_scope IS
    '协议归属：shared=OpenAI+Anthropic 通用；openai=OpenAI Chat/Responses 专有；anthropic=Anthropic Messages 专有。';
