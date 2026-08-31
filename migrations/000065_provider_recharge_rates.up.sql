-- 充值汇率从渠道级迁移到服务商级（docs/changes/2026-08-31-provider-recharge-rate）。
--
-- Provider recharge rate 是「服务商充值汇率」：实际支付的服务商币种金额 ÷ 到账的 USD 名义额度。
-- 同一服务商下所有渠道共享同一个当前生效版本；渠道级 channel_recharge_factors 整体废弃删除。
-- 开发环境删库重建前提：不迁移历史数据，存量引用直接清空/删除。
--
-- 本迁移按依赖顺序执行：
--   1. 建 provider_recharge_rates（沿用「不可改 + 时间窗 + 新建一条 + 关闭旧窗口」范式，新增审计字段）；
--   2. 重写毛利守卫视图（B/C 分支充值来源 crf → prr），解除对旧表的依赖；
--   3. cost_snapshots 充值来源列替换为服务商级（channel_recharge_factor_id/recharge_factor →
--      provider_recharge_rate_id/provider_recharge_rate）；
--   4. settlement_recovery_jobs 的充值 pin 列同步改名换 FK；
--   5. provider_ledger_entries 新增事件时 USD 折算快照三列（amount_usd/fx_rate/fx_rate_date）；
--   6. drop channel_recharge_factors。

-- 1. 服务商充值汇率表。
CREATE SEQUENCE public.provider_recharge_rates_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.provider_recharge_rates (
    -- id: 主键。--
    id bigint NOT NULL,
    -- provider_id: 充值汇率归属的服务商 ID；其下所有渠道共享。--
    provider_id bigint NOT NULL,
    -- provider_currency: 写入时的服务商结算币种快照（服务端从 providers.currency 读取，不受客户端控制）。--
    provider_currency text NOT NULL,
    -- nominal_currency: 名义额度币种，固定 USD（平台币种）。--
    nominal_currency text DEFAULT 'USD' NOT NULL,
    -- rate: 充值汇率 = 支付金额（服务商币种）÷ 到账名义额度（USD）。仅允许 > 0（D-04）。--
    rate numeric(20,10) NOT NULL,
    -- status: 启停状态。--
    status text NOT NULL,
    -- source: 数值来源：manual 手工输入 / calculated 由「支付金额 ÷ 名义额度」助手算出。--
    source text DEFAULT 'manual' NOT NULL,
    -- reason: 调整原因，可空但建议填写（审计）。--
    reason text,
    -- created_by: 操作者标识（审计）。--
    created_by text,
    -- effective_from: 生效开始时间。--
    effective_from timestamp with time zone NOT NULL,
    -- effective_to: 生效结束时间，空值表示长期有效。--
    effective_to timestamp with time zone,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_recharge_rates_rate_check CHECK ((rate > (0)::numeric)),
    CONSTRAINT provider_recharge_rates_status_check CHECK ((status = ANY (ARRAY['enabled'::text, 'disabled'::text]))),
    CONSTRAINT provider_recharge_rates_source_check CHECK ((source = ANY (ARRAY['manual'::text, 'calculated'::text]))),
    CONSTRAINT provider_recharge_rates_nominal_check CHECK ((nominal_currency = 'USD'::text)),
    CONSTRAINT ck_provider_recharge_rates_window CHECK (((effective_to IS NULL) OR (effective_to > effective_from)))
);

ALTER SEQUENCE public.provider_recharge_rates_id_seq OWNED BY public.provider_recharge_rates.id;

ALTER TABLE ONLY public.provider_recharge_rates ALTER COLUMN id SET DEFAULT nextval('public.provider_recharge_rates_id_seq'::regclass);

ALTER TABLE ONLY public.provider_recharge_rates
    ADD CONSTRAINT provider_recharge_rates_pkey PRIMARY KEY (id);

-- 同一服务商启用版本生效窗口半开区间 [from, to) 不可重叠（与旧渠道表同范式）。
ALTER TABLE ONLY public.provider_recharge_rates
    ADD CONSTRAINT ex_provider_recharge_rates_enabled_window EXCLUDE USING gist (provider_id WITH =, tstzrange(effective_from, COALESCE(effective_to, 'infinity'::timestamp with time zone), '[)'::text) WITH &&) WHERE ((status = 'enabled'::text));

CREATE INDEX idx_provider_recharge_rates_provider_status_effective ON public.provider_recharge_rates USING btree (provider_id, status, effective_from DESC, id DESC);

ALTER TABLE ONLY public.provider_recharge_rates
    ADD CONSTRAINT provider_recharge_rates_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id);

-- 充值汇率变更纳入毛利守卫（与旧渠道倍率表同语义）：改汇率可能让倍率路径成本越过售价线，
-- 事务提交前由 assert_non_negative_margins() 全量校验（函数复用 000058 定义，查 margin_violations_current 视图）。
CREATE CONSTRAINT TRIGGER trg_provider_recharge_rates_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON public.provider_recharge_rates DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_margins();

-- 2. 毛利守卫视图重写：B/C 倍率分支的充值来源从渠道级 crf 改为服务商级 prr（窗口重叠 join 语义不变）。
--    仍以 DO/EXECUTE 包裹（sqlc 无法解析此形态视图 DDL）。A/D0/D1/D2 分支与 000059 完全一致。
DO $do$
BEGIN
EXECUTE $view$
CREATE OR REPLACE VIEW public.margin_violations_current AS
    -- A. 绝对成本覆盖 × 绝对售价/倍率售价：两侧都是绝对值，逐项比较（跨币种按最新汇率换算）。
    SELECT
        cm.channel_id,
        cm.model_id,
        'standard/' || rates.component AS component,
        rates.sale,
        rates.cost,
        mp.currency AS sale_currency,
        cp.currency AS cost_currency,
        fx.rate AS fx_rate,
        fx.rate_date AS fx_rate_date
    FROM channel_models cm
    JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled'
    JOIN providers p ON p.id = c.provider_id AND p.status = 'enabled'
    JOIN models m ON m.id = cm.model_id AND m.status = 'enabled'
    JOIN model_prices mp ON mp.model_id = m.id AND mp.status = 'enabled'
    JOIN channel_prices cp ON cp.channel_id = c.id AND cp.model_id = m.id AND cp.status = 'enabled'
    LEFT JOIN LATERAL (
        SELECT er.rate, er.rate_date
        FROM exchange_rates er
        WHERE er.base_currency = mp.currency
          AND er.quote_currency = cp.currency
        ORDER BY er.rate_date DESC, er.fetched_at DESC
        LIMIT 1
    ) fx ON cp.currency <> mp.currency
    CROSS JOIN LATERAL (VALUES
        ('uncached_input',
         COALESCE(mp.sale_uncached_input_price, mp.uncached_input_price * mp.sale_price_ratio),
         cp.uncached_input_cost),
        ('cache_read_input',
         COALESCE(mp.sale_cache_read_input_price, mp.sale_uncached_input_price,
                  COALESCE(mp.cache_read_input_price, mp.uncached_input_price) * mp.sale_price_ratio),
         COALESCE(cp.cache_read_input_cost, cp.uncached_input_cost)),
        ('cache_creation_5m_input',
         COALESCE(mp.sale_cache_creation_5m_input_price, mp.sale_uncached_input_price,
                  COALESCE(mp.cache_creation_5m_input_price, mp.uncached_input_price) * mp.sale_price_ratio),
         COALESCE(cp.cache_creation_5m_input_cost, cp.uncached_input_cost)),
        ('cache_creation_1h_input',
         COALESCE(mp.sale_cache_creation_1h_input_price, mp.sale_uncached_input_price,
                  COALESCE(mp.cache_creation_1h_input_price, mp.uncached_input_price) * mp.sale_price_ratio),
         COALESCE(cp.cache_creation_1h_input_cost, cp.uncached_input_cost)),
        ('cache_creation_30m_input',
         COALESCE(mp.sale_cache_creation_30m_input_price, mp.sale_uncached_input_price,
                  COALESCE(mp.cache_creation_30m_input_price, mp.uncached_input_price) * mp.sale_price_ratio),
         COALESCE(cp.cache_creation_30m_input_cost, cp.uncached_input_cost)),
        ('output',
         COALESCE(mp.sale_output_price, mp.output_price * mp.sale_price_ratio),
         cp.output_cost),
        ('reasoning_output',
         COALESCE(mp.sale_reasoning_output_price, mp.sale_output_price,
                  COALESCE(mp.reasoning_output_price, mp.output_price) * mp.sale_price_ratio),
         COALESCE(cp.reasoning_output_cost, cp.output_cost))
    ) AS rates(component, sale, cost)
    WHERE (mp.sale_uncached_input_price IS NOT NULL OR mp.sale_price_ratio IS NOT NULL)
      AND mp.effective_from < COALESCE(cp.effective_to, 'infinity'::timestamptz)
      AND cp.effective_from < COALESCE(mp.effective_to, 'infinity'::timestamptz)
      AND (
          mp.pricing_unit <> cp.pricing_unit
          OR (mp.currency <> cp.currency AND fx.rate IS NULL)
          OR rates.sale * COALESCE(fx.rate, 1) < rates.cost
      )

    UNION ALL

    -- B. 倍率成本 × 倍率售价：基准价两侧相消，退化为标量比较。
    --    充值来源为服务商级 prr：成本币种 = provider 结算币种，跨币种时 sale_ratio × rate ≥ multiplier × recharge。
    SELECT
        cm.channel_id,
        cm.model_id,
        CASE WHEN prr.id IS NULL THEN 'standard/cost_multiplier'
             ELSE 'standard/cost_multiplier_x_recharge' END AS component,
        mp.sale_price_ratio AS sale,
        ccm.multiplier * COALESCE(prr.rate, 1) AS cost,
        mp.currency AS sale_currency,
        p.currency AS cost_currency,
        fx.rate AS fx_rate,
        fx.rate_date AS fx_rate_date
    FROM channel_models cm
    JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled'
    JOIN providers p ON p.id = c.provider_id AND p.status = 'enabled'
    JOIN models m ON m.id = cm.model_id AND m.status = 'enabled'
    JOIN model_prices mp ON mp.model_id = m.id AND mp.status = 'enabled'
    JOIN channel_cost_multipliers ccm
      ON ccm.channel_id = c.id
     AND (ccm.model_id = m.id OR ccm.model_id IS NULL)
     AND ccm.status = 'enabled'
    LEFT JOIN provider_recharge_rates prr
      ON prr.provider_id = p.id
     AND prr.status = 'enabled'
     AND ccm.effective_from < COALESCE(prr.effective_to, 'infinity'::timestamptz)
     AND prr.effective_from < COALESCE(ccm.effective_to, 'infinity'::timestamptz)
    LEFT JOIN LATERAL (
        SELECT er.rate, er.rate_date
        FROM exchange_rates er
        WHERE er.base_currency = mp.currency
          AND er.quote_currency = p.currency
        ORDER BY er.rate_date DESC, er.fetched_at DESC
        LIMIT 1
    ) fx ON p.currency <> mp.currency
    WHERE mp.sale_uncached_input_price IS NULL
      AND mp.sale_price_ratio IS NOT NULL
      AND mp.effective_from < COALESCE(ccm.effective_to, 'infinity'::timestamptz)
      AND ccm.effective_from < COALESCE(mp.effective_to, 'infinity'::timestamptz)
      AND (
          (p.currency <> mp.currency AND fx.rate IS NULL)
          OR mp.sale_price_ratio * COALESCE(fx.rate, 1) < ccm.multiplier * COALESCE(prr.rate, 1)
      )

    UNION ALL

    -- C. 倍率成本 × 绝对售价：售价侧无基准价公因子，逐项还原后比较。
    --    充值来源为服务商级 prr：成本币种 = provider 结算币种，跨币种按最新汇率乘法比较。
    SELECT
        cm.channel_id,
        cm.model_id,
        'standard/' || rates.component AS component,
        rates.sale,
        rates.cost,
        mp.currency AS sale_currency,
        p.currency AS cost_currency,
        fx.rate AS fx_rate,
        fx.rate_date AS fx_rate_date
    FROM channel_models cm
    JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled'
    JOIN providers p ON p.id = c.provider_id AND p.status = 'enabled'
    JOIN models m ON m.id = cm.model_id AND m.status = 'enabled'
    JOIN model_prices mp ON mp.model_id = m.id AND mp.status = 'enabled'
    JOIN channel_cost_multipliers ccm
      ON ccm.channel_id = c.id
     AND (ccm.model_id = m.id OR ccm.model_id IS NULL)
     AND ccm.status = 'enabled'
    LEFT JOIN provider_recharge_rates prr
      ON prr.provider_id = p.id
     AND prr.status = 'enabled'
     AND ccm.effective_from < COALESCE(prr.effective_to, 'infinity'::timestamptz)
     AND prr.effective_from < COALESCE(ccm.effective_to, 'infinity'::timestamptz)
    LEFT JOIN LATERAL (
        SELECT er.rate, er.rate_date
        FROM exchange_rates er
        WHERE er.base_currency = mp.currency
          AND er.quote_currency = p.currency
        ORDER BY er.rate_date DESC, er.fetched_at DESC
        LIMIT 1
    ) fx ON p.currency <> mp.currency
    CROSS JOIN LATERAL (VALUES
        ('uncached_input',
         mp.sale_uncached_input_price,
         mp.uncached_input_price * ccm.multiplier * COALESCE(prr.rate, 1)),
        ('cache_read_input',
         COALESCE(mp.sale_cache_read_input_price, mp.sale_uncached_input_price),
         COALESCE(mp.cache_read_input_price, mp.uncached_input_price)
             * ccm.multiplier * COALESCE(prr.rate, 1)),
        ('cache_creation_5m_input',
         COALESCE(mp.sale_cache_creation_5m_input_price, mp.sale_uncached_input_price),
         COALESCE(mp.cache_creation_5m_input_price, mp.uncached_input_price)
             * ccm.multiplier * COALESCE(prr.rate, 1)),
        ('cache_creation_1h_input',
         COALESCE(mp.sale_cache_creation_1h_input_price, mp.sale_uncached_input_price),
         COALESCE(mp.cache_creation_1h_input_price, mp.uncached_input_price)
             * ccm.multiplier * COALESCE(prr.rate, 1)),
        ('cache_creation_30m_input',
         COALESCE(mp.sale_cache_creation_30m_input_price, mp.sale_uncached_input_price),
         COALESCE(mp.cache_creation_30m_input_price, mp.uncached_input_price)
             * ccm.multiplier * COALESCE(prr.rate, 1)),
        ('output',
         mp.sale_output_price,
         mp.output_price * ccm.multiplier * COALESCE(prr.rate, 1)),
        ('reasoning_output',
         COALESCE(mp.sale_reasoning_output_price, mp.sale_output_price),
         COALESCE(mp.reasoning_output_price, mp.output_price)
             * ccm.multiplier * COALESCE(prr.rate, 1))
    ) AS rates(component, sale, cost)
    WHERE mp.sale_uncached_input_price IS NOT NULL
      AND mp.effective_from < COALESCE(ccm.effective_to, 'infinity'::timestamptz)
      AND ccm.effective_from < COALESCE(mp.effective_to, 'infinity'::timestamptz)
      AND (
          (p.currency <> mp.currency AND fx.rate IS NULL)
          OR rates.sale * COALESCE(fx.rate, 1) < rates.cost
      )

    UNION ALL

    -- D0. Fast 与 Standard 必须走同一套售价实体：绝对售价同在或同不在。
    SELECT
        cm.channel_id,
        cm.model_id,
        'fast/absolute_incomplete' AS component,
        0::numeric AS sale,
        1::numeric AS cost,
        mp.currency AS sale_currency,
        mp.currency AS cost_currency,
        NULL::numeric AS fx_rate,
        NULL::date AS fx_rate_date
    FROM channel_models cm
    JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled'
    JOIN providers p ON p.id = c.provider_id AND p.status = 'enabled'
    JOIN models m ON m.id = cm.model_id AND m.status = 'enabled'
    JOIN model_prices mp ON mp.model_id = m.id AND mp.status = 'enabled'
    JOIN model_price_service_tiers fmp
      ON fmp.model_price_id = mp.id AND fmp.service_tier = 'fast'
    WHERE (mp.sale_uncached_input_price IS NULL) <> (fmp.sale_uncached_input_price IS NULL)

    UNION ALL

    -- D1. Fast 绝对售价实体（跨币种按最新汇率换算）。
    SELECT
        cm.channel_id,
        cm.model_id,
        'fast/' || rates.component AS component,
        rates.sale,
        rates.cost,
        mp.currency AS sale_currency,
        cp.currency AS cost_currency,
        fx.rate AS fx_rate,
        fx.rate_date AS fx_rate_date
    FROM channel_models cm
    JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled'
    JOIN providers p ON p.id = c.provider_id AND p.status = 'enabled'
    JOIN models m ON m.id = cm.model_id AND m.status = 'enabled'
    JOIN model_prices mp ON mp.model_id = m.id AND mp.status = 'enabled'
    JOIN model_price_service_tiers fmp
      ON fmp.model_price_id = mp.id AND fmp.service_tier = 'fast'
    JOIN channel_prices cp ON cp.channel_id = c.id AND cp.model_id = m.id AND cp.status = 'enabled'
    JOIN channel_price_service_tiers fcp
      ON fcp.channel_price_id = cp.id AND fcp.service_tier = 'fast'
    LEFT JOIN LATERAL (
        SELECT er.rate, er.rate_date
        FROM exchange_rates er
        WHERE er.base_currency = mp.currency
          AND er.quote_currency = cp.currency
        ORDER BY er.rate_date DESC, er.fetched_at DESC
        LIMIT 1
    ) fx ON cp.currency <> mp.currency
    CROSS JOIN LATERAL (VALUES
        ('uncached_input',
         fmp.sale_uncached_input_price,
         fcp.uncached_input_cost),
        ('cache_read_input',
         COALESCE(fmp.sale_cache_read_input_price, fmp.sale_uncached_input_price),
         COALESCE(fcp.cache_read_input_cost, fcp.uncached_input_cost)),
        ('cache_creation_5m_input',
         COALESCE(fmp.sale_cache_creation_5m_input_price, fmp.sale_uncached_input_price),
         COALESCE(fcp.cache_creation_5m_input_cost, fcp.uncached_input_cost)),
        ('cache_creation_1h_input',
         COALESCE(fmp.sale_cache_creation_1h_input_price, fmp.sale_uncached_input_price),
         COALESCE(fcp.cache_creation_1h_input_cost, fcp.uncached_input_cost)),
        ('cache_creation_30m_input',
         COALESCE(fmp.sale_cache_creation_30m_input_price, fmp.sale_uncached_input_price),
         COALESCE(fcp.cache_creation_30m_input_cost, fcp.uncached_input_cost)),
        ('output',
         fmp.sale_output_price,
         fcp.output_cost),
        ('reasoning_output',
         COALESCE(fmp.sale_reasoning_output_price, fmp.sale_output_price),
         COALESCE(fcp.reasoning_output_cost, fcp.output_cost))
    ) AS rates(component, sale, cost)
    WHERE mp.sale_uncached_input_price IS NOT NULL
      AND fmp.sale_uncached_input_price IS NOT NULL
      AND mp.effective_from < COALESCE(cp.effective_to, 'infinity'::timestamptz)
      AND cp.effective_from < COALESCE(mp.effective_to, 'infinity'::timestamptz)
      AND (
          rates.sale IS NULL
          OR (mp.currency <> cp.currency AND fx.rate IS NULL)
          OR rates.sale * COALESCE(fx.rate, 1) < rates.cost
      )

    UNION ALL

    -- D2. Fast 倍率实体：Fast 售价 = Fast 基准价 × 模型倍率（跨币种按最新汇率换算）。
    SELECT
        cm.channel_id,
        cm.model_id,
        'fast/' || rates.component AS component,
        rates.sale,
        rates.cost,
        mp.currency AS sale_currency,
        cp.currency AS cost_currency,
        fx.rate AS fx_rate,
        fx.rate_date AS fx_rate_date
    FROM channel_models cm
    JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled'
    JOIN providers p ON p.id = c.provider_id AND p.status = 'enabled'
    JOIN models m ON m.id = cm.model_id AND m.status = 'enabled'
    JOIN model_prices mp ON mp.model_id = m.id AND mp.status = 'enabled'
    JOIN model_price_service_tiers fmp
      ON fmp.model_price_id = mp.id AND fmp.service_tier = 'fast'
    JOIN channel_prices cp ON cp.channel_id = c.id AND cp.model_id = m.id AND cp.status = 'enabled'
    JOIN channel_price_service_tiers fcp
      ON fcp.channel_price_id = cp.id AND fcp.service_tier = 'fast'
    LEFT JOIN LATERAL (
        SELECT er.rate, er.rate_date
        FROM exchange_rates er
        WHERE er.base_currency = mp.currency
          AND er.quote_currency = cp.currency
        ORDER BY er.rate_date DESC, er.fetched_at DESC
        LIMIT 1
    ) fx ON cp.currency <> mp.currency
    CROSS JOIN LATERAL (VALUES
        ('uncached_input',
         fmp.uncached_input_price * mp.sale_price_ratio,
         fcp.uncached_input_cost),
        ('cache_read_input',
         COALESCE(fmp.cache_read_input_price, fmp.uncached_input_price) * mp.sale_price_ratio,
         COALESCE(fcp.cache_read_input_cost, fcp.uncached_input_cost)),
        ('cache_creation_5m_input',
         COALESCE(fmp.cache_creation_5m_input_price, fmp.uncached_input_price) * mp.sale_price_ratio,
         COALESCE(fcp.cache_creation_5m_input_cost, fcp.uncached_input_cost)),
        ('cache_creation_1h_input',
         COALESCE(fmp.cache_creation_1h_input_price, fmp.uncached_input_price) * mp.sale_price_ratio,
         COALESCE(fcp.cache_creation_1h_input_cost, fcp.uncached_input_cost)),
        ('cache_creation_30m_input',
         COALESCE(fmp.cache_creation_30m_input_price, fmp.uncached_input_price) * mp.sale_price_ratio,
         COALESCE(fcp.cache_creation_30m_input_cost, fcp.uncached_input_cost)),
        ('output',
         fmp.output_price * mp.sale_price_ratio,
         fcp.output_cost),
        ('reasoning_output',
         COALESCE(fmp.reasoning_output_price, fmp.output_price) * mp.sale_price_ratio,
         COALESCE(fcp.reasoning_output_cost, fcp.output_cost))
    ) AS rates(component, sale, cost)
    WHERE mp.sale_uncached_input_price IS NULL
      AND fmp.sale_uncached_input_price IS NULL
      AND mp.sale_price_ratio IS NOT NULL
      AND mp.effective_from < COALESCE(cp.effective_to, 'infinity'::timestamptz)
      AND cp.effective_from < COALESCE(mp.effective_to, 'infinity'::timestamptz)
      AND (
          rates.sale IS NULL
          OR (mp.currency <> cp.currency AND fx.rate IS NULL)
          OR rates.sale * COALESCE(fx.rate, 1) < rates.cost
      )
$view$;
END
$do$;

-- 3. cost_snapshots 充值来源列替换为服务商级（删库重建，无兼容期双字段；存量行数据随列一并丢弃）。
ALTER TABLE public.cost_snapshots
    DROP COLUMN channel_recharge_factor_id,
    DROP COLUMN recharge_factor;

ALTER TABLE public.cost_snapshots
    ADD COLUMN provider_recharge_rate_id bigint,
    ADD COLUMN provider_recharge_rate numeric(20,10);

ALTER TABLE ONLY public.cost_snapshots
    ADD CONSTRAINT cost_snapshots_provider_recharge_rate_id_fkey FOREIGN KEY (provider_recharge_rate_id) REFERENCES public.provider_recharge_rates(id);

-- 4. settlement_recovery_jobs 充值 pin 列改名换 FK；存量 pin 指向旧表 id，先清空（开发环境无历史价值）。
UPDATE public.settlement_recovery_jobs SET channel_recharge_factor_id = NULL;
ALTER TABLE public.settlement_recovery_jobs
    RENAME COLUMN channel_recharge_factor_id TO provider_recharge_rate_id;
ALTER TABLE ONLY public.settlement_recovery_jobs
    ADD CONSTRAINT settlement_recovery_jobs_provider_recharge_rate_id_fkey FOREIGN KEY (provider_recharge_rate_id) REFERENCES public.provider_recharge_rates(id);

-- 5. provider_ledger_entries 事件时 USD 折算快照（统一 USD 展示的账本事实层）。
--    三态 CHECK：全 NULL = 本迁移前的存量行（不回填，展示层显示不可折算）；
--    USD 行 fx 为 NULL 且 usd = 原币金额；非 USD 行必须钉汇率（与 cost_snapshots ck 同口径）。
ALTER TABLE public.provider_ledger_entries
    ADD COLUMN amount_usd numeric(20,10),
    ADD COLUMN fx_rate numeric(20,10),
    ADD COLUMN fx_rate_date date;

ALTER TABLE public.provider_ledger_entries
    ADD CONSTRAINT ck_provider_ledger_entries_fx CHECK (
        (amount_usd IS NULL AND fx_rate IS NULL AND fx_rate_date IS NULL)
     OR (currency = 'USD' AND fx_rate IS NULL AND fx_rate_date IS NULL
             AND amount_usd = amount)
     OR (currency <> 'USD' AND fx_rate IS NOT NULL AND fx_rate > 0
             AND fx_rate_date IS NOT NULL AND amount_usd IS NOT NULL)
    );

-- 6. 渠道级充值倍率整体废弃。
DROP TABLE public.channel_recharge_factors;
