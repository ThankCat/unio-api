-- DEC-026：客户售价 = 模型基准价（model_prices）× 线路倍率（routes.price_ratio），
-- 渠道侧只承载「成本」。channel_prices 的售价列与录入毛利守卫自此退役（售价快照取 model_prices×ratio）。
-- 先删依赖售价列的毛利 CHECK，再删 6 个售价列；成本列保留。

ALTER TABLE channel_prices
    DROP CONSTRAINT IF EXISTS ck_channel_prices_margin;

ALTER TABLE channel_prices
    DROP COLUMN uncached_input_price,
    DROP COLUMN cache_read_input_price,
    DROP COLUMN cache_write_5m_input_price,
    DROP COLUMN cache_write_1h_input_price,
    DROP COLUMN output_price,
    DROP COLUMN reasoning_output_price;
