-- 回滚：重建 channel_prices 售价列（必填两项以 0 兜底历史行）与录入毛利守卫。
ALTER TABLE channel_prices
    ADD COLUMN uncached_input_price NUMERIC(20, 10) NOT NULL DEFAULT 0 CHECK (uncached_input_price >= 0),
    ADD COLUMN cache_read_input_price NUMERIC(20, 10) CHECK (cache_read_input_price IS NULL OR cache_read_input_price >= 0),
    ADD COLUMN cache_write_5m_input_price NUMERIC(20, 10) CHECK (cache_write_5m_input_price IS NULL OR cache_write_5m_input_price >= 0),
    ADD COLUMN cache_write_1h_input_price NUMERIC(20, 10) CHECK (cache_write_1h_input_price IS NULL OR cache_write_1h_input_price >= 0),
    ADD COLUMN output_price NUMERIC(20, 10) NOT NULL DEFAULT 0 CHECK (output_price >= 0),
    ADD COLUMN reasoning_output_price NUMERIC(20, 10) CHECK (reasoning_output_price IS NULL OR reasoning_output_price >= 0);

ALTER TABLE channel_prices
    ADD CONSTRAINT ck_channel_prices_margin CHECK (
        (uncached_input_cost IS NULL OR uncached_input_price >= uncached_input_cost)
        AND (output_cost IS NULL OR output_price >= output_cost)
        AND (cache_read_input_cost IS NULL OR cache_read_input_price IS NULL OR cache_read_input_price >= cache_read_input_cost)
        AND (cache_write_5m_input_cost IS NULL OR cache_write_5m_input_price IS NULL OR cache_write_5m_input_price >= cache_write_5m_input_cost)
        AND (cache_write_1h_input_cost IS NULL OR cache_write_1h_input_price IS NULL OR cache_write_1h_input_price >= cache_write_1h_input_cost)
        AND (reasoning_output_cost IS NULL OR reasoning_output_price IS NULL OR reasoning_output_price >= reasoning_output_cost)
    );
