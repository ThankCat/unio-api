-- cost_snapshots 钉汇率三列（多货币 Phase 2，PLAN M3/D4）：
--   fx_rate / fx_rate_date：本笔结算换算所用的汇率与其所属日——不是换算结果，是「存档」，
--     保证任何一笔历史账单可离线复算（cost ÷ fx_rate = usd，字节可比）。
--   total_cost_amount_usd：USD 归一总额（结算时 big.Rat 精确除后单次舍入 scale 10 落库）。
--     刻意冗余：给报表 O(1) 聚合口径（SUM 不再按币种过滤），同时给对账提供
--     「两种表示必须对得上」的审计抓手（对账不变量 I3：usd = round(total ÷ rate, 10)）。
-- USD 行 fx 列为 NULL 且 usd = 原币总额，由 CHECK 强制——防止「USD 行也写了汇率」或
-- 「CNY 行漏钉汇率」这两类静默脏数据。
ALTER TABLE public.cost_snapshots
    ADD COLUMN fx_rate numeric(20,10),
    ADD COLUMN fx_rate_date date,
    ADD COLUMN total_cost_amount_usd numeric(20,10);

-- 存量快照全部为 USD（多货币上线前不存在非 USD 行），usd 归一列 = 原币总额。
UPDATE public.cost_snapshots SET total_cost_amount_usd = total_cost_amount;

ALTER TABLE public.cost_snapshots
    ALTER COLUMN total_cost_amount_usd SET NOT NULL;

ALTER TABLE public.cost_snapshots
    ADD CONSTRAINT ck_cost_snapshots_fx CHECK (
        (currency = 'USD' AND fx_rate IS NULL AND fx_rate_date IS NULL
             AND total_cost_amount_usd = total_cost_amount)
     OR (currency <> 'USD' AND fx_rate IS NOT NULL AND fx_rate > 0
             AND fx_rate_date IS NOT NULL)
    );
