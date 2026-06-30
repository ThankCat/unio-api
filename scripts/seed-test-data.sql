-- seed-test-data.sql — 本地开发测试数据种子（幂等，可重复执行）。
--
-- 内容：1 个 OpenAI provider + 1 条渠道 + 3 个模型（GPT-5.5 / GPT-5.4 / GPT-5.4-mini，
-- 参考 OpenAI 全能力声明）+ 模型与渠道绑定 + 永不过期价格（成本参考官网、售价 = 成本 ×1.5）
-- + 1 用户 + 余额 + 1 条开发用线路 + 1 API Key（线路必填，Key 直接绑定该线路）。
--
-- 必须通过 scripts/seed-test-data.sh 运行（它会注入 :key_prefix / :key_hash 两个 psql 变量）。
-- 渠道 base_url / credential 为占位，需在 Admin 后台改成真实值后才能打真实上游。
--
-- 价格口径：channel_prices 同行承载「售价（客户侧，必填）+ 成本价（上游侧，可空）」，
-- settlement 收入与成本均取自该表（channel_cost_prices 已退役，不写）。单位 per_1m_tokens、币种 USD。

\set ON_ERROR_STOP on

BEGIN;

-- 1) Provider：OpenAI ---------------------------------------------------------
INSERT INTO providers (slug, name, status)
VALUES ('openai', 'OpenAI', 'enabled')
ON CONFLICT (slug) DO NOTHING;

-- 2) Channel：OpenAI 官方渠道（adapter_key=openai，protocol=openai）------------
--    credential 为明文占位（产品决策：渠道凭据明文存储）；base_url 为官方地址占位。需在 Admin 改成真实值。
INSERT INTO channels (
    provider_id, name, protocol, adapter_key, base_url,
    credential, status, priority, timeout_ms
)
SELECT p.id, 'OpenAI 官方渠道', 'openai', 'openai', 'https://api.openai.com/v1',
       'sk-REPLACE-IN-ADMIN', 'enabled', 0, 60000
FROM providers p
WHERE p.slug = 'openai'
ON CONFLICT (provider_id, name) DO NOTHING;

-- 3) Models：GPT-5.5 / GPT-5.4 / GPT-5.4-mini --------------------------------
--    input/output_price_usd_per_million_tokens 仅为目录展示（绝不用于计费），此处填售价便于展示。
INSERT INTO models (
    model_id, display_name, owned_by, status,
    context_window_tokens, max_output_tokens,
    input_price_usd_per_million_tokens, output_price_usd_per_million_tokens, source
)
VALUES
    ('gpt-5.5',      'GPT-5.5',      'openai', 'enabled', 400000, 128000, 1.8750, 15.0000, 'manual'),
    ('gpt-5.4',      'GPT-5.4',      'openai', 'enabled', 400000, 128000, 1.5000, 12.0000, 'manual'),
    ('gpt-5.4-mini', 'GPT-5.4-mini', 'openai', 'enabled', 400000, 128000, 0.3750,  3.0000, 'manual')
ON CONFLICT (model_id) DO NOTHING;

-- 4) 模型 ↔ 渠道绑定（upstream_model 暂用同名，可在 Admin 改）-----------------
INSERT INTO channel_models (channel_id, model_id, upstream_model, status)
SELECT c.id, m.id, m.model_id, 'enabled'
FROM channels c
JOIN providers p ON p.id = c.provider_id AND p.slug = 'openai'
JOIN models m ON m.model_id IN ('gpt-5.5', 'gpt-5.4', 'gpt-5.4-mini')
WHERE c.name = 'OpenAI 官方渠道'
ON CONFLICT (channel_id, model_id) DO NOTHING;

-- 5) 渠道成本价（DEC-026：渠道只录成本，永不过期 effective_to=NULL）---------
--    成本参考 OpenAI 官网量级：input / cached_input / output（USD per 1M tokens）。
--    OpenAI 无 cache_write / 独立 reasoning 计价，对应列留空。客户售价取 model_prices × 线路倍率（见 5b）。
INSERT INTO channel_prices (
    channel_id, model_id, currency, pricing_unit,
    uncached_input_cost, cache_read_input_cost, output_cost,
    status, effective_from, effective_to
)
SELECT
    cm.channel_id, cm.model_id, 'USD', 'per_1m_tokens',
    v.cost_in, v.cost_cached, v.cost_out,
    'enabled', now(), NULL
FROM channel_models cm
JOIN models m ON m.id = cm.model_id
JOIN channels c ON c.id = cm.channel_id
JOIN providers p ON p.id = c.provider_id AND p.slug = 'openai'
JOIN (VALUES
    -- model_id,       cost_in, cost_cached, cost_out
    ('gpt-5.5',        1.2500,  0.1250,      10.0000),
    ('gpt-5.4',        1.0000,  0.1000,       8.0000),
    ('gpt-5.4-mini',   0.2500,  0.0250,       2.0000)
) AS v(model_id, cost_in, cost_cached, cost_out)
    ON v.model_id = m.model_id
WHERE c.name = 'OpenAI 官方渠道'
  AND NOT EXISTS (
      SELECT 1 FROM channel_prices cp
      WHERE cp.channel_id = cm.channel_id
        AND cp.model_id = cm.model_id
        AND cp.status = 'enabled'
        AND cp.currency = 'USD'
        AND cp.pricing_unit = 'per_1m_tokens'
  );

-- 5b) 模型基准售价 model_prices（DEC-026：客户售价 = 模型基准价 × 线路倍率）-------
--     基准价取原渠道售价（= 成本 ×1.5）；配合内置「经济」线路倍率 1.0，等价于旧渠道售价口径。
--     OpenAI 无 cache_write / 独立 reasoning 计价，对应列留空。
INSERT INTO model_prices (
    model_id, currency, pricing_unit,
    uncached_input_price, cache_read_input_price, output_price,
    status, effective_from, effective_to
)
SELECT
    m.id, 'USD', 'per_1m_tokens',
    v.sale_in, v.sale_cached, v.sale_out,
    'enabled', now(), NULL
FROM models m
JOIN (VALUES
    -- model_id,       sale_in, sale_cached, sale_out
    ('gpt-5.5',        1.8750,  0.1875,      15.0000),
    ('gpt-5.4',        1.5000,  0.1500,      12.0000),
    ('gpt-5.4-mini',   0.3750,  0.0375,       3.0000)
) AS v(model_id, sale_in, sale_cached, sale_out)
    ON v.model_id = m.model_id
WHERE m.model_id IN ('gpt-5.5', 'gpt-5.4', 'gpt-5.4-mini')
  AND NOT EXISTS (
      SELECT 1 FROM model_prices mp
      WHERE mp.model_id = m.id
        AND mp.status = 'enabled'
        AND mp.currency = 'USD'
        AND mp.pricing_unit = 'per_1m_tokens'
  );

-- 6) 模型能力声明（参考 OpenAI 全能力，support_level=full；展示用，非闸门）-------
--    排除该系列不具备的：image.output / audio.* / tools.builtin.computer_use / reasoning.budget(Anthropic)。
INSERT INTO model_capabilities (model_id, capability_key, support_level, updated_by)
SELECT m.id, k.key, 'full', 'seed'
FROM models m
CROSS JOIN (VALUES
    ('text.input'), ('text.output'),
    ('image.input'), ('file.input'),
    ('tools.function'), ('tools.custom'), ('tools.parallel'), ('tools.choice_required'),
    ('tools.builtin.web_search'), ('tools.builtin.file_search'),
    ('tools.builtin.code_interpreter'), ('tools.builtin.image_generation'), ('tools.builtin.mcp'),
    ('reasoning.effort'), ('reasoning.summary'),
    ('response_format.json_object'), ('response_format.json_schema'),
    ('prompt_cache'), ('logprobs'), ('service_tier'),
    ('stream'), ('stream.tools'), ('stream.usage'),
    ('server_state.store'), ('server_state.background'),
    ('responses.encrypted_content'), ('responses.compact.native'), ('responses.compact.synthetic')
) AS k(key)
WHERE m.model_id IN ('gpt-5.5', 'gpt-5.4', 'gpt-5.4-mini')
ON CONFLICT (model_id, capability_key) DO NOTHING;

-- 7) 用户（dev@unio.local；password_hash 占位，gateway 不校验登录）-------------
INSERT INTO users (email, password_hash, display_name)
SELECT 'dev@unio.local', 'seed-placeholder-not-a-real-hash', 'Dev User'
WHERE NOT EXISTS (SELECT 1 FROM users WHERE lower(email) = lower('dev@unio.local'));

-- 8) 用户余额（100 USD，足够授权扣费）----------------------------------------
INSERT INTO user_balances (user_id, currency, balance, reserved_balance)
SELECT u.id, 'USD', 100.0000000000, 0
FROM users u
WHERE lower(u.email) = lower('dev@unio.local')
ON CONFLICT (user_id, currency) DO NOTHING;

-- 9) 开发用线路（cheapest / all）----------------------------------------------
INSERT INTO routes (name, mode, pool_kind, status, description)
SELECT 'Dev Cheapest', 'cheapest', 'all', 'enabled', '本地 seed：按售价最低选路'
WHERE NOT EXISTS (SELECT 1 FROM routes WHERE name = 'Dev Cheapest');

-- 10) API Key（route_id 必填 → 直接绑定 Dev Cheapest 线路；spend_limit 不限额）---
--     幂等：先删除同用户下同名 seed key，再插入本次新 key。
DELETE FROM api_keys
WHERE name = 'seed test key'
  AND user_id = (
      SELECT u.id FROM users u
      WHERE lower(u.email) = lower('dev@unio.local')
  );

INSERT INTO api_keys (user_id, name, key_prefix, key_hash, key_plaintext, route_id, spend_limit)
SELECT u.id, 'seed test key', :'key_prefix', :'key_hash', :'key_plaintext', r.id, NULL
FROM users u
CROSS JOIN routes r
WHERE lower(u.email) = lower('dev@unio.local')
  AND r.name = 'Dev Cheapest';

COMMIT;
