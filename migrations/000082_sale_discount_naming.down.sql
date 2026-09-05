ALTER TABLE public.settlement_recovery_jobs RENAME CONSTRAINT settlement_recovery_jobs_sale_discount_check TO settlement_recovery_jobs_price_ratio_check;
ALTER TABLE public.settlement_recovery_jobs RENAME COLUMN sale_discount TO price_ratio;
COMMENT ON COLUMN public.settlement_recovery_jobs.price_ratio IS NULL;

ALTER TABLE public.price_snapshots RENAME CONSTRAINT price_snapshots_sale_discount_check TO price_snapshots_price_ratio_check;
ALTER TABLE public.price_snapshots RENAME COLUMN sale_discount TO price_ratio;
COMMENT ON COLUMN public.price_snapshots.price_ratio IS NULL;

ALTER TABLE public.model_prices RENAME CONSTRAINT ck_model_prices_sale_discount_positive TO ck_model_prices_sale_ratio_positive;
ALTER TABLE public.model_prices RENAME COLUMN sale_discount TO sale_price_ratio;
COMMENT ON COLUMN public.model_prices.sale_price_ratio IS
    '模型售价倍率：绝对售价整组留空时，客户售价 = 基准价 × 本倍率。可与绝对售价同行共存，但绝对售价生效时倍率不参与计算。';
COMMENT ON COLUMN public.model_prices.sale_uncached_input_price IS
    '模型对外绝对售价；整组非空时覆盖倍率，Standard 与 Fast 必须同在。整组为空时回退「基准价 × 同行 sale_price_ratio」。两者都空则此行不可售。';
COMMENT ON COLUMN public.model_price_service_tiers.sale_uncached_input_price IS
    'Fast 档对外绝对售价；整组为空时回退「该档基准价 × 模型 sale_price_ratio」（与 Standard 共用倍率）。';
