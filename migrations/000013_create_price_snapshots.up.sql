-- Price snapshot 是一次请求结算时使用的客户售价副本。
CREATE TABLE price_snapshots (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- request_record_id: 对应的请求记录 ID。--
    request_record_id BIGINT NOT NULL UNIQUE REFERENCES request_records (id),

    -- price_id: 结算时命中的价格配置 ID。--
    price_id BIGINT REFERENCES prices (id),

    -- currency: 结算币种。--
    currency TEXT NOT NULL CHECK (currency <> ''),

    -- pricing_unit: 结算计价单位。--
    pricing_unit TEXT NOT NULL CHECK (pricing_unit = 'per_1m_tokens'),

    -- uncached_input_price: 快照中的未缓存输入 token 售价。--
    uncached_input_price NUMERIC(20, 10) NOT NULL CHECK (uncached_input_price >= 0),

    -- cache_read_input_price: 快照中的缓存读取输入 token 售价。--
    cache_read_input_price NUMERIC(20, 10) CHECK (
        cache_read_input_price IS NULL OR cache_read_input_price >= 0
    ),

    -- cache_write_5m_input_price: 快照中的 5 分钟缓存写入输入 token 售价。--
    cache_write_5m_input_price NUMERIC(20, 10) CHECK (
        cache_write_5m_input_price IS NULL OR cache_write_5m_input_price >= 0
    ),

    -- cache_write_1h_input_price: 快照中的 1 小时缓存写入输入 token 售价。--
    cache_write_1h_input_price NUMERIC(20, 10) CHECK (
        cache_write_1h_input_price IS NULL OR cache_write_1h_input_price >= 0
    ),

    -- output_price: 快照中的权威输出 token 售价。--
    output_price NUMERIC(20, 10) NOT NULL CHECK (output_price >= 0),

    -- reasoning_output_price: 快照中的 reasoning 输出 token 售价。--
    reasoning_output_price NUMERIC(20, 10) CHECK (
        reasoning_output_price IS NULL OR reasoning_output_price >= 0
    ),

    -- formula_version: 结算公式版本。--
    formula_version TEXT NOT NULL CHECK (formula_version <> ''),

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
