-- Cost snapshot 是一次请求结算时使用的 provider/channel 成本价副本和实际成本事实。
CREATE TABLE cost_snapshots (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- request_record_id: 对应的请求记录 ID，一次请求只能有一条成本快照。--
    request_record_id BIGINT NOT NULL UNIQUE REFERENCES request_records (id),

    -- cost_price_id: 结算时命中的 channel 成本价配置 ID。--
    cost_price_id BIGINT NOT NULL,

    -- provider_id: 本次请求最终使用的 provider ID。--
    provider_id BIGINT NOT NULL REFERENCES providers (id),

    -- channel_id: 本次请求最终使用的 channel ID。--
    channel_id BIGINT NOT NULL REFERENCES channels (id),

    -- model_id: 本次请求使用的 Unio 模型 ID。--
    model_id BIGINT NOT NULL REFERENCES models (id),

    -- upstream_model: 本次请求转发给上游的模型名。--
    upstream_model TEXT NOT NULL CHECK (upstream_model <> ''),

    -- currency: 成本币种。--
    currency TEXT NOT NULL CHECK (currency <> ''),

    -- pricing_unit: 成本计价单位。--
    pricing_unit TEXT NOT NULL CHECK (pricing_unit = 'per_1m_tokens'),

    -- uncached_input_cost: 快照中的未缓存输入 token 成本价。--
    uncached_input_cost NUMERIC(20, 10) NOT NULL CHECK (uncached_input_cost >= 0),

    -- cache_read_input_cost: 快照中的缓存读取输入 token 成本价。--
    cache_read_input_cost NUMERIC(20, 10) CHECK (
        cache_read_input_cost IS NULL OR cache_read_input_cost >= 0
    ),

    -- cache_write_5m_input_cost: 快照中的 5 分钟缓存写入输入 token 成本价。--
    cache_write_5m_input_cost NUMERIC(20, 10) CHECK (
        cache_write_5m_input_cost IS NULL OR cache_write_5m_input_cost >= 0
    ),

    -- cache_write_1h_input_cost: 快照中的 1 小时缓存写入输入 token 成本价。--
    cache_write_1h_input_cost NUMERIC(20, 10) CHECK (
        cache_write_1h_input_cost IS NULL OR cache_write_1h_input_cost >= 0
    ),

    -- output_cost: 快照中的权威输出 token 成本价。--
    output_cost NUMERIC(20, 10) NOT NULL CHECK (output_cost >= 0),

    -- reasoning_output_cost: 快照中的 reasoning 输出 token 成本价。--
    reasoning_output_cost NUMERIC(20, 10) CHECK (
        reasoning_output_cost IS NULL OR reasoning_output_cost >= 0
    ),

    -- uncached_input_cost_amount: 本次请求未缓存输入 token 实际成本金额。--
    uncached_input_cost_amount NUMERIC(20, 10) NOT NULL CHECK (uncached_input_cost_amount >= 0),

    -- cache_read_input_cost_amount: 本次请求缓存读取输入 token 实际成本金额。--
    cache_read_input_cost_amount NUMERIC(20, 10) NOT NULL CHECK (cache_read_input_cost_amount >= 0),

    -- cache_write_5m_input_cost_amount: 本次请求 5 分钟缓存写入输入 token 实际成本金额。--
    cache_write_5m_input_cost_amount NUMERIC(20, 10) NOT NULL CHECK (cache_write_5m_input_cost_amount >= 0),

    -- cache_write_1h_input_cost_amount: 本次请求 1 小时缓存写入输入 token 实际成本金额。--
    cache_write_1h_input_cost_amount NUMERIC(20, 10) NOT NULL CHECK (cache_write_1h_input_cost_amount >= 0),

    -- output_cost_amount: 本次请求普通输出 token 实际成本金额。--
    output_cost_amount NUMERIC(20, 10) NOT NULL CHECK (output_cost_amount >= 0),

    -- reasoning_output_cost_amount: 本次请求 reasoning 输出 token 实际成本金额。--
    reasoning_output_cost_amount NUMERIC(20, 10) NOT NULL CHECK (reasoning_output_cost_amount >= 0),

    -- total_cost_amount: 本次请求平台实际总成本金额。--
    total_cost_amount NUMERIC(20, 10) NOT NULL CHECK (total_cost_amount >= 0),

    -- formula_version: 成本计算公式版本。--
    formula_version TEXT NOT NULL CHECK (formula_version <> ''),

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 成本快照必须保证最终 channel 属于最终 provider。--
    CONSTRAINT fk_cost_snapshots_channel_provider
        FOREIGN KEY (channel_id, provider_id)
            REFERENCES channels (id, provider_id),

    -- 成本快照必须对应真实存在的 channel-model 服务能力。--
    CONSTRAINT fk_cost_snapshots_channel_model
        FOREIGN KEY (channel_id, model_id)
            REFERENCES channel_models (channel_id, model_id),

    -- 总成本必须等于各成本分项金额之和。--
    CONSTRAINT ck_cost_snapshots_total_amount CHECK (
        total_cost_amount =
            uncached_input_cost_amount
            + cache_read_input_cost_amount
            + cache_write_5m_input_cost_amount
            + cache_write_1h_input_cost_amount
            + output_cost_amount
            + reasoning_output_cost_amount
    ),

    -- 成本快照命中的成本价必须属于同一个 channel/model。--
    CONSTRAINT fk_cost_snapshots_cost_price_channel_model
        FOREIGN KEY (cost_price_id, channel_id, model_id)
            REFERENCES channel_cost_prices (id, channel_id, model_id)
);

-- 后台请求详情和成本审计会按 provider/channel 倒序查看成本快照。
CREATE INDEX idx_cost_snapshots_channel_created_at ON cost_snapshots (channel_id, created_at DESC, id DESC);

-- 成本报表会按 provider 和创建时间聚合平台成本。
CREATE INDEX idx_cost_snapshots_provider_created_at ON cost_snapshots (provider_id, created_at DESC, id DESC);
