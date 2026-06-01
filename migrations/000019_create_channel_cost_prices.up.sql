-- Channel cost price 是某条 channel 服务某个 Unio model 时的上游成本价配置。
CREATE TABLE channel_cost_prices (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- channel_id: 成本价适用的 channel ID。--
    channel_id BIGINT NOT NULL REFERENCES channels (id),

    -- model_id: 成本价适用的 Unio 模型 ID。--
    model_id BIGINT NOT NULL REFERENCES models (id),

    -- currency: 成本价币种。--
    currency TEXT NOT NULL CHECK (currency <> ''),

    -- pricing_unit: 成本计价单位。--
    pricing_unit TEXT NOT NULL CHECK (pricing_unit = 'per_1m_tokens'),

    -- uncached_input_cost: 每计价单位未缓存输入 token 成本价。--
    uncached_input_cost NUMERIC(20, 10) NOT NULL CHECK (uncached_input_cost >= 0),

    -- cache_read_input_cost: 每计价单位缓存读取输入 token 成本价。--
    cache_read_input_cost NUMERIC(20, 10) CHECK (
        cache_read_input_cost IS NULL OR cache_read_input_cost >= 0
    ),

    -- cache_write_5m_input_cost: 每计价单位 5 分钟缓存写入输入 token 成本价。--
    cache_write_5m_input_cost NUMERIC(20, 10) CHECK (
        cache_write_5m_input_cost IS NULL OR cache_write_5m_input_cost >= 0
    ),

    -- cache_write_1h_input_cost: 每计价单位 1 小时缓存写入输入 token 成本价。--
    cache_write_1h_input_cost NUMERIC(20, 10) CHECK (
        cache_write_1h_input_cost IS NULL OR cache_write_1h_input_cost >= 0
    ),

    -- output_cost: 每计价单位权威输出 token 成本价。--
    output_cost NUMERIC(20, 10) NOT NULL CHECK (output_cost >= 0),

    -- reasoning_output_cost: 每计价单位 reasoning 输出 token 成本价。--
    reasoning_output_cost NUMERIC(20, 10) CHECK (
        reasoning_output_cost IS NULL OR reasoning_output_cost >= 0
    ),

    -- status: 成本价启停状态。--
    status TEXT NOT NULL CHECK (status IN ('enabled', 'disabled')),

    -- effective_from: 成本价生效开始时间。--
    effective_from TIMESTAMPTZ NOT NULL,

    -- effective_to: 成本价生效结束时间，空值表示长期有效。--
    effective_to TIMESTAMPTZ,

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 成本价 ID 与 channel/model 组合需要支持成本快照复合引用。--
    CONSTRAINT uq_channel_cost_prices_id_channel_model
        UNIQUE (id, channel_id, model_id),

    -- 成本价必须对应真实存在的 channel-model 服务能力。--
    CONSTRAINT fk_channel_cost_prices_channel_model
        FOREIGN KEY (channel_id, model_id)
            REFERENCES channel_models (channel_id, model_id),

    -- 成本价结束时间必须晚于开始时间。--
    CONSTRAINT ck_channel_cost_prices_effective_window
        CHECK (effective_to IS NULL OR effective_to > effective_from)
);

-- settlement 会按 channel、model、status 和生效时间查找当前成本价。
CREATE INDEX idx_channel_cost_prices_channel_model_status_effective
    ON channel_cost_prices (channel_id, model_id, status, effective_from DESC, id DESC);
