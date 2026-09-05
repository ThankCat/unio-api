-- 销售定价口径变更：倍率 → 折扣（2026-09-05）。
--
-- 客户售价 = 基准牌价 × 折扣系数（存储仍是系数：0.05 = 0.5 折），仅改名不改数学。
-- 范围只含销售侧：model_prices.sale_price_ratio 与两张资金快照/补偿表的 price_ratio；
-- 上游成本侧（channel_cost_multipliers、provider_recharge_rates、长上下文加价倍率）保持「倍率」不动——
-- 上游按倍率拿价、对客户按折扣卖。列改名自动同步视图（margin_violations_current）与约束的内部引用。

ALTER TABLE public.model_prices RENAME COLUMN sale_price_ratio TO sale_discount;
ALTER TABLE public.model_prices RENAME CONSTRAINT ck_model_prices_sale_ratio_positive TO ck_model_prices_sale_discount_positive;
COMMENT ON COLUMN public.model_prices.sale_discount IS
    '售价折扣系数（0.05 = 0.5 折）：客户售价 = 基准牌价 × 本值；与 sale_* 绝对售价二选一。';

-- 其它列注释里引用了旧列名/倍率口径，一并改写。
COMMENT ON COLUMN public.model_prices.sale_uncached_input_price IS
    '模型对外绝对售价；整组非空时覆盖折扣，Standard 与 Fast 必须同在。整组为空时回退「基准价 × 同行 sale_discount」。两者都空则此行不可售。';
COMMENT ON COLUMN public.model_price_service_tiers.sale_uncached_input_price IS
    'Fast 档对外绝对售价；整组为空时回退「该档基准价 × 模型 sale_discount」（与 Standard 共用折扣）。';

ALTER TABLE public.price_snapshots RENAME COLUMN price_ratio TO sale_discount;
ALTER TABLE public.price_snapshots RENAME CONSTRAINT price_snapshots_price_ratio_check TO price_snapshots_sale_discount_check;
COMMENT ON COLUMN public.price_snapshots.sale_discount IS
    '结算时刻生效的售价折扣系数快照（倍率定价路径）；绝对售价路径为 NULL。';

ALTER TABLE public.settlement_recovery_jobs RENAME COLUMN price_ratio TO sale_discount;
ALTER TABLE public.settlement_recovery_jobs RENAME CONSTRAINT settlement_recovery_jobs_price_ratio_check TO settlement_recovery_jobs_sale_discount_check;
COMMENT ON COLUMN public.settlement_recovery_jobs.sale_discount IS
    '补偿任务冻结的售价折扣系数（与创建时刻的定价一致，重放不受后续调价影响）。';
