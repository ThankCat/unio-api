-- btree_gist 让 exclusion constraint 可以同时比较 BIGINT/TEXT 等值和时间范围重叠（000012 已建，幂等保证）。
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- Channel price 是某条 channel 服务某个 Unio model 时的「售价 + 成本价」合并配置（阶段 15）。
-- 一行同时含客户售价（必填）与上游成本价（可空），毛利在录入期即被守卫保证非负。
-- 退役 prices（模型级售价）与 channel_cost_prices（渠道级成本价），计费一律走渠道-模型级。
CREATE TABLE channel_prices (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- channel_id: 价格适用的 channel ID。--
    channel_id BIGINT NOT NULL,

    -- model_id: 价格适用的 Unio 模型 ID。--
    model_id BIGINT NOT NULL,

    -- currency: 计价币种。--
    currency TEXT NOT NULL CHECK (currency <> ''),

    -- pricing_unit: 计价单位。--
    pricing_unit TEXT NOT NULL CHECK (pricing_unit = 'per_1m_tokens'),

    -- uncached_input_price: 每计价单位未缓存输入 token 售价（客户侧，必填）。--
    uncached_input_price NUMERIC(20, 10) NOT NULL CHECK (uncached_input_price >= 0),

    -- cache_read_input_price: 每计价单位缓存读取输入 token 售价。--
    cache_read_input_price NUMERIC(20, 10) CHECK (
        cache_read_input_price IS NULL OR cache_read_input_price >= 0
    ),

    -- cache_write_5m_input_price: 每计价单位 5 分钟缓存写入输入 token 售价。--
    cache_write_5m_input_price NUMERIC(20, 10) CHECK (
        cache_write_5m_input_price IS NULL OR cache_write_5m_input_price >= 0
    ),

    -- cache_write_1h_input_price: 每计价单位 1 小时缓存写入输入 token 售价。--
    cache_write_1h_input_price NUMERIC(20, 10) CHECK (
        cache_write_1h_input_price IS NULL OR cache_write_1h_input_price >= 0
    ),

    -- output_price: 每计价单位权威输出 token 售价（客户侧，必填）。--
    output_price NUMERIC(20, 10) NOT NULL CHECK (output_price >= 0),

    -- reasoning_output_price: 每计价单位 reasoning 输出 token 售价。--
    reasoning_output_price NUMERIC(20, 10) CHECK (
        reasoning_output_price IS NULL OR reasoning_output_price >= 0
    ),

    -- uncached_input_cost: 每计价单位未缓存输入 token 成本价（上游侧，可空）。--
    uncached_input_cost NUMERIC(20, 10) CHECK (
        uncached_input_cost IS NULL OR uncached_input_cost >= 0
    ),

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
    output_cost NUMERIC(20, 10) CHECK (
        output_cost IS NULL OR output_cost >= 0
    ),

    -- reasoning_output_cost: 每计价单位 reasoning 输出 token 成本价。--
    reasoning_output_cost NUMERIC(20, 10) CHECK (
        reasoning_output_cost IS NULL OR reasoning_output_cost >= 0
    ),

    -- status: 价格启停状态。--
    status TEXT NOT NULL CHECK (status IN ('enabled', 'disabled')),

    -- effective_from: 价格生效开始时间。--
    effective_from TIMESTAMPTZ NOT NULL,

    -- effective_to: 价格生效结束时间，空值表示长期有效。--
    effective_to TIMESTAMPTZ,

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 价格必须对应真实存在的 channel-model 服务能力（沿用 channel_cost_prices 既有口径）。--
    CONSTRAINT fk_channel_prices_channel_model
        FOREIGN KEY (channel_id, model_id)
            REFERENCES channel_models (channel_id, model_id),

    -- 价格 ID 与 channel/model 组合需要支持 price/cost 快照复合引用。--
    CONSTRAINT uq_channel_prices_id_channel_model
        UNIQUE (id, channel_id, model_id),

    -- 价格结束时间必须晚于开始时间。--
    CONSTRAINT ck_channel_prices_window
        CHECK (effective_to IS NULL OR effective_to > effective_from),

    -- 录入守卫：任一分项有成本时，售价不得低于成本（成本为空则跳过该项）。--
    CONSTRAINT ck_channel_prices_margin CHECK (
        (uncached_input_cost IS NULL OR uncached_input_price >= uncached_input_cost)
        AND (output_cost IS NULL OR output_price >= output_cost)
        AND (cache_read_input_cost IS NULL OR cache_read_input_price IS NULL OR cache_read_input_price >= cache_read_input_cost)
        AND (cache_write_5m_input_cost IS NULL OR cache_write_5m_input_price IS NULL OR cache_write_5m_input_price >= cache_write_5m_input_cost)
        AND (cache_write_1h_input_cost IS NULL OR cache_write_1h_input_price IS NULL OR cache_write_1h_input_price >= cache_write_1h_input_cost)
        AND (reasoning_output_cost IS NULL OR reasoning_output_price IS NULL OR reasoning_output_price >= reasoning_output_cost)
    ),

    -- 同一 channel/model/币种/计价单位在 enabled 状态下同一时间只能命中一条价格。--
    CONSTRAINT ex_channel_prices_enabled_window
        EXCLUDE USING gist (
            channel_id WITH =,
            model_id WITH =,
            currency WITH =,
            pricing_unit WITH =,
            tstzrange(
                effective_from,
                COALESCE(effective_to, 'infinity'::timestamptz),
                '[)'
            ) WITH &&
        )
        WHERE (status = 'enabled')
);

-- routing/settlement 会按 channel/model/status 和生效时间查找当前价格。
CREATE INDEX idx_channel_prices_channel_model_status_effective
    ON channel_prices (channel_id, model_id, status, effective_from DESC, id DESC);
