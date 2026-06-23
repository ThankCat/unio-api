-- 能力 key 字典增加协议归属（DEC-024 运维区分 OpenAI / Anthropic / 通用）。
ALTER TABLE capability_keys
    ADD COLUMN protocol_scope TEXT NOT NULL DEFAULT 'both'
        CHECK (protocol_scope IN ('both', 'openai', 'anthropic'));

COMMENT ON COLUMN capability_keys.protocol_scope IS
    '协议归属：both=OpenAI+Anthropic 通用；openai=OpenAI Chat/Responses 专有；anthropic=Anthropic Messages 专有。';

-- 通用（双协议均常见）
UPDATE capability_keys SET protocol_scope = 'both' WHERE key IN (
    'text.input', 'text.output',
    'image.input', 'image.output',
    'audio.input', 'audio.output',
    'file.input',
    'tools.function', 'tools.parallel', 'tools.choice_required',
    'response_format.json_object', 'response_format.json_schema',
    'prompt_cache', 'logprobs',
    'stream', 'stream.tools', 'stream.usage'
);

-- OpenAI 专有（Chat Completions / Responses API）
UPDATE capability_keys SET protocol_scope = 'openai' WHERE key IN (
    'tools.custom',
    'tools.builtin.web_search', 'tools.builtin.file_search',
    'tools.builtin.code_interpreter', 'tools.builtin.computer_use',
    'tools.builtin.image_generation', 'tools.builtin.mcp',
    'reasoning.effort', 'reasoning.summary',
    'service_tier',
    'server_state.store', 'server_state.background',
    'responses.encrypted_content', 'responses.compact.native', 'responses.compact.synthetic'
);

-- Anthropic 专有（Messages API）
UPDATE capability_keys SET protocol_scope = 'anthropic' WHERE key IN (
    'reasoning.budget'
);
