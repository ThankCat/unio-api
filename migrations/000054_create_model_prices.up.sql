-- Model price 是某个 Unio 模型的「基准客户售价」（DEC-026 倍率定价）。
-- 客户最终售价 = 本表基准售价 × routes.price_ratio（线路倍率）；售价不再挂渠道，渠道只记成本。
-- 结算审计：price snapshot 记录命中的 model_prices.id + 当时线路倍率，历史账单可按原事实复算。
-- btree_gist 让 exclusion constraint 可同时比较等值列与时间范围重叠（000031 已建，幂等保证）。
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE model_prices (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- model_id: 基准售价适用的 Unio 模型 ID。--
    model_id BIGINT NOT NULL REFERENCES models (id),

    -- currency: 计价币种。--
    currency TEXT NOT NULL CHECK (currency <> ''),

    -- pricing_unit: 计价单位。--
    pricing_unit TEXT NOT NULL CHECK (pricing_unit = 'per_1m_tokens'),

    -- uncached_input_price: 每计价单位未缓存输入 token 基准售价（必填）。--
    uncached_input_price NUMERIC(20, 10) NOT NULL CHECK (uncached_input_price >= 0),

    -- cache_read_input_price: 每计价单位缓存读取输入 token 基准售价。--
    cache_read_input_price NUMERIC(20, 10) CHECK (
        cache_read_input_price IS NULL OR cache_read_input_price >= 0
    ),

    -- cache_write_5m_input_price: 每计价单位 5 分钟缓存写入输入 token 基准售价。--
    cache_write_5m_input_price NUMERIC(20, 10) CHECK (
        cache_write_5m_input_price IS NULL OR cache_write_5m_input_price >= 0
    ),

    -- cache_write_1h_input_price: 每计价单位 1 小时缓存写入输入 token 基准售价。--
    cache_write_1h_input_price NUMERIC(20, 10) CHECK (
        cache_write_1h_input_price IS NULL OR cache_write_1h_input_price >= 0
    ),

    -- output_price: 每计价单位权威输出 token 基准售价（必填）。--
    output_price NUMERIC(20, 10) NOT NULL CHECK (output_price >= 0),

    -- reasoning_output_price: 每计价单位 reasoning 输出 token 基准售价。--
    reasoning_output_price NUMERIC(20, 10) CHECK (
        reasoning_output_price IS NULL OR reasoning_output_price >= 0
    ),

    -- status: 基准售价启停状态。--
    status TEXT NOT NULL CHECK (status IN ('enabled', 'disabled')),

    -- effective_from: 生效开始时间。--
    effective_from TIMESTAMPTZ NOT NULL,

    -- effective_to: 生效结束时间，空值表示长期有效。--
    effective_to TIMESTAMPTZ,

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 基准售价 ID 与 model 组合需要支持 price snapshot 复合引用。--
    CONSTRAINT uq_model_prices_id_model
        UNIQUE (id, model_id),

    -- 生效结束时间必须晚于开始时间。--
    CONSTRAINT ck_model_prices_window
        CHECK (effective_to IS NULL OR effective_to > effective_from),

    -- 同一 model/币种/计价单位在 enabled 状态下同一时间只能命中一条基准售价。--
    CONSTRAINT ex_model_prices_enabled_window
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

-- routing/settlement 会按 model/status 和生效时间查找当前基准售价。
CREATE INDEX idx_model_prices_model_status_effective
    ON model_prices (model_id, status, effective_from DESC, id DESC);
