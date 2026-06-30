-- 为 route（线路 = 分组 / 档）增加价格倍率（DEC-026 倍率定价）。
-- 客户最终售价 = model_prices（模型基准售价） × routes.price_ratio。默认 1.0（与基准价同价）。
-- 同一线路对所有模型套用同一倍率（new-api groupRatio 口径）；如需逐模型差异，后续另建表，本期不做。
ALTER TABLE routes
    ADD COLUMN price_ratio NUMERIC(20, 10) NOT NULL DEFAULT 1.0 CHECK (price_ratio >= 0);
