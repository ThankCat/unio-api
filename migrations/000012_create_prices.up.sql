-- btree_gist 让 exclusion constraint 可以同时比较 BIGINT/TEXT 等值和时间范围重叠。
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- Price 是模型客户侧售卖价配置，属于后台可管理的业务数据。
CREATE TABLE prices (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- model_id: 价格适用的 Unio 模型 ID。--
    model_id BIGINT NOT NULL REFERENCES models (id),

    -- currency: 计价币种。--
    currency TEXT NOT NULL CHECK (currency <> ''),

    -- pricing_unit: 计价单位。--
    pricing_unit TEXT NOT NULL CHECK (pricing_unit = 'per_1m_tokens'),

    -- uncached_input_price: 每计价单位未缓存输入 token 售价。--
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

    -- output_price: 每计价单位权威输出 token 售价。--
    output_price NUMERIC(20, 10) NOT NULL CHECK (output_price >= 0),

    -- reasoning_output_price: 每计价单位 reasoning 输出 token 售价。--
    reasoning_output_price NUMERIC(20, 10) CHECK (
        reasoning_output_price IS NULL OR reasoning_output_price >= 0
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

    -- 价格结束时间必须晚于开始时间。--
    CONSTRAINT ck_prices_effective_window
        CHECK (effective_to IS NULL OR effective_to > effective_from),

    -- 同一模型、币种和计价单位在 enabled 状态下同一时间只能命中一条价格。--
    CONSTRAINT ex_prices_enabled_effective_window
        EXCLUDE USING gist (
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

-- active price 查询会按 model、status 和生效时间窗口过滤。
CREATE INDEX idx_prices_model_status_effective
    ON prices (model_id, status, effective_from DESC, id DESC);
