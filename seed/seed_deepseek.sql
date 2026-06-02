BEGIN;
-- ① 用户与项目
INSERT INTO users (email, password_hash, display_name)
VALUES ('dev@local.test', 'not-used-for-gateway', 'Dev User')
RETURNING id;  -- 记下 user_id

INSERT INTO projects (user_id, name)
VALUES (1, 'default')
RETURNING id;  -- 记下 project_id

-- ② 客户 API Key
INSERT INTO api_keys (project_id, name, key_prefix, key_hash)
VALUES (1, 'dev-key', 'unio_sk_YJWgvcQd', '80f18aa303ea352d1b42f490fdcb0ae36b56b068d05e04924eea6739c4ac3d04');

-- ③ 用户余额（预付费，必须有）
INSERT INTO user_balances (user_id, currency, balance, reserved_balance)
VALUES (1, 'USD', 10.0000000000, 0);

-- ④ Provider（Phase 10 起 adapter 绑定下沉到 channel）
INSERT INTO providers (slug, name, status)
VALUES ('deepseek', 'DeepSeek', 'enabled')
RETURNING id;  -- 记下 provider_id

-- ⑤ Channel（DeepSeek 上游；protocol=openai，adapter_key=deepseek 命中 adapter/openai/deepseek）
INSERT INTO channels (provider_id, name, protocol, adapter_key, base_url, credential_encrypted, status, priority, timeout_ms)
VALUES (
           1,
            'deepseek-main',
            'openai',
            'deepseek',
            'https://api.deepseek.com',
            '\xa57c1a5bbd47e00069e8452df0cd47f66da686f480ff828094dbf7133373f2176ce404755d072b4c451184981e1cd5e2a2bb3d3788bbe1e0879b4913a8f3f6'::bytea,
            'enabled',
            0,
            60000
       )
RETURNING id;  -- 记下 channel_id

-- ⑥ 对外模型
INSERT INTO models (model_id, display_name, owned_by, status)
VALUES ('deepseek-chat', 'DeepSeek Chat', 'deepseek', 'enabled')
RETURNING id;  -- 记下 model_id

-- ⑦ Channel ↔ Model 映射
INSERT INTO channel_models (channel_id, model_id, upstream_model, status)
VALUES (1,1, 'deepseek-chat', 'enabled');

-- ⑧ 客户售价（per_1m_tokens，数值可按你意愿填，测试用简单价即可）
INSERT INTO prices (
    model_id, currency, pricing_unit,
    input_price, output_price,
    status, effective_from
)
VALUES (
           1, 'USD', 'per_1m_tokens',
            0.5000000000, 2.0000000000,
            'enabled', now() - interval '1 hour'
);

-- ⑨ 上游成本价（settlement 需要）
INSERT INTO channel_cost_prices (
    channel_id, model_id, currency, pricing_unit,
    input_cost, output_cost, cached_input_cost, reasoning_output_cost,
    status, effective_from
)
VALUES (
           1, 1, 'CNY', 'per_1m_tokens',
           3.0000000000,   -- 输入，缓存未命中
           6.0000000000,   -- 输出
           0.0250000000,   -- 输入，缓存命中
           NULL,           -- reasoning 沿用 output_cost
           'enabled', now() - interval '1 hour'
       );
COMMIT;