-- 还原退役的 prices 与 channel_cost_prices（结构与 000012 / 000019 一致），供 000035.down 重新挂回外键。

CREATE TABLE prices (
    id BIGSERIAL PRIMARY KEY,
    model_id BIGINT NOT NULL REFERENCES models (id),
    currency TEXT NOT NULL CHECK (currency <> ''),
    pricing_unit TEXT NOT NULL CHECK (pricing_unit = 'per_1m_tokens'),
    uncached_input_price NUMERIC(20, 10) NOT NULL CHECK (uncached_input_price >= 0),
    cache_read_input_price NUMERIC(20, 10) CHECK (
        cache_read_input_price IS NULL OR cache_read_input_price >= 0
    ),
    cache_write_5m_input_price NUMERIC(20, 10) CHECK (
        cache_write_5m_input_price IS NULL OR cache_write_5m_input_price >= 0
    ),
    cache_write_1h_input_price NUMERIC(20, 10) CHECK (
        cache_write_1h_input_price IS NULL OR cache_write_1h_input_price >= 0
    ),
    output_price NUMERIC(20, 10) NOT NULL CHECK (output_price >= 0),
    reasoning_output_price NUMERIC(20, 10) CHECK (
        reasoning_output_price IS NULL OR reasoning_output_price >= 0
    ),
    status TEXT NOT NULL CHECK (status IN ('enabled', 'disabled')),
    effective_from TIMESTAMPTZ NOT NULL,
    effective_to TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_prices_effective_window
        CHECK (effective_to IS NULL OR effective_to > effective_from),
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

CREATE INDEX idx_prices_model_status_effective
    ON prices (model_id, status, effective_from DESC, id DESC);

CREATE TABLE channel_cost_prices (
    id BIGSERIAL PRIMARY KEY,
    channel_id BIGINT NOT NULL REFERENCES channels (id),
    model_id BIGINT NOT NULL REFERENCES models (id),
    currency TEXT NOT NULL CHECK (currency <> ''),
    pricing_unit TEXT NOT NULL CHECK (pricing_unit = 'per_1m_tokens'),
    uncached_input_cost NUMERIC(20, 10) NOT NULL CHECK (uncached_input_cost >= 0),
    cache_read_input_cost NUMERIC(20, 10) CHECK (
        cache_read_input_cost IS NULL OR cache_read_input_cost >= 0
    ),
    cache_write_5m_input_cost NUMERIC(20, 10) CHECK (
        cache_write_5m_input_cost IS NULL OR cache_write_5m_input_cost >= 0
    ),
    cache_write_1h_input_cost NUMERIC(20, 10) CHECK (
        cache_write_1h_input_cost IS NULL OR cache_write_1h_input_cost >= 0
    ),
    output_cost NUMERIC(20, 10) NOT NULL CHECK (output_cost >= 0),
    reasoning_output_cost NUMERIC(20, 10) CHECK (
        reasoning_output_cost IS NULL OR reasoning_output_cost >= 0
    ),
    status TEXT NOT NULL CHECK (status IN ('enabled', 'disabled')),
    effective_from TIMESTAMPTZ NOT NULL,
    effective_to TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_channel_cost_prices_id_channel_model
        UNIQUE (id, channel_id, model_id),
    CONSTRAINT fk_channel_cost_prices_channel_model
        FOREIGN KEY (channel_id, model_id)
            REFERENCES channel_models (channel_id, model_id),
    CONSTRAINT ck_channel_cost_prices_effective_window
        CHECK (effective_to IS NULL OR effective_to > effective_from)
);

CREATE INDEX idx_channel_cost_prices_channel_model_status_effective
    ON channel_cost_prices (channel_id, model_id, status, effective_from DESC, id DESC);
