-- 回滚服务商级充值汇率改造：恢复渠道级 channel_recharge_factors 结构（数据不可恢复，开发环境专用）。

-- 1. 重建渠道级充值倍率表（与 000015 同构）。
CREATE SEQUENCE public.channel_recharge_factors_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.channel_recharge_factors (
    id bigint NOT NULL,
    channel_id bigint NOT NULL,
    factor numeric(20,10) NOT NULL,
    status text NOT NULL,
    effective_from timestamp with time zone NOT NULL,
    effective_to timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_recharge_factors_factor_check CHECK ((factor >= (0)::numeric)),
    CONSTRAINT channel_recharge_factors_status_check CHECK ((status = ANY (ARRAY['enabled'::text, 'disabled'::text]))),
    CONSTRAINT ck_channel_recharge_factors_window CHECK (((effective_to IS NULL) OR (effective_to > effective_from)))
);

ALTER SEQUENCE public.channel_recharge_factors_id_seq OWNED BY public.channel_recharge_factors.id;
ALTER TABLE ONLY public.channel_recharge_factors ALTER COLUMN id SET DEFAULT nextval('public.channel_recharge_factors_id_seq'::regclass);
ALTER TABLE ONLY public.channel_recharge_factors ADD CONSTRAINT channel_recharge_factors_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.channel_recharge_factors
    ADD CONSTRAINT ex_channel_recharge_factors_enabled_window EXCLUDE USING gist (channel_id WITH =, tstzrange(effective_from, COALESCE(effective_to, 'infinity'::timestamp with time zone), '[)'::text) WITH &&) WHERE ((status = 'enabled'::text));
CREATE INDEX idx_channel_recharge_factors_channel_status_effective ON public.channel_recharge_factors USING btree (channel_id, status, effective_from DESC, id DESC);
ALTER TABLE ONLY public.channel_recharge_factors
    ADD CONSTRAINT channel_recharge_factors_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id);

-- 2. 毛利守卫视图恢复 000059 形态（B/C 分支充值来源 prr → crf）。
DO $do$
BEGIN
EXECUTE $view$
CREATE OR REPLACE VIEW public.margin_violations_current AS
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

    SELECT
        cm.channel_id,
        cm.model_id,
        CASE WHEN crf.id IS NULL THEN 'standard/cost_multiplier'
             ELSE 'standard/cost_multiplier_x_recharge' END AS component,
        mp.sale_price_ratio AS sale,
        ccm.multiplier * COALESCE(crf.factor, 1) AS cost,
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
    LEFT JOIN channel_recharge_factors crf
      ON crf.channel_id = c.id
     AND crf.status = 'enabled'
     AND ccm.effective_from < COALESCE(crf.effective_to, 'infinity'::timestamptz)
     AND crf.effective_from < COALESCE(ccm.effective_to, 'infinity'::timestamptz)
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
          OR mp.sale_price_ratio * COALESCE(fx.rate, 1) < ccm.multiplier * COALESCE(crf.factor, 1)
      )

    UNION ALL

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
    LEFT JOIN channel_recharge_factors crf
      ON crf.channel_id = c.id
     AND crf.status = 'enabled'
     AND ccm.effective_from < COALESCE(crf.effective_to, 'infinity'::timestamptz)
     AND crf.effective_from < COALESCE(ccm.effective_to, 'infinity'::timestamptz)
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
         mp.uncached_input_price * ccm.multiplier * COALESCE(crf.factor, 1)),
        ('cache_read_input',
         COALESCE(mp.sale_cache_read_input_price, mp.sale_uncached_input_price),
         COALESCE(mp.cache_read_input_price, mp.uncached_input_price)
             * ccm.multiplier * COALESCE(crf.factor, 1)),
        ('cache_creation_5m_input',
         COALESCE(mp.sale_cache_creation_5m_input_price, mp.sale_uncached_input_price),
         COALESCE(mp.cache_creation_5m_input_price, mp.uncached_input_price)
             * ccm.multiplier * COALESCE(crf.factor, 1)),
        ('cache_creation_1h_input',
         COALESCE(mp.sale_cache_creation_1h_input_price, mp.sale_uncached_input_price),
         COALESCE(mp.cache_creation_1h_input_price, mp.uncached_input_price)
             * ccm.multiplier * COALESCE(crf.factor, 1)),
        ('cache_creation_30m_input',
         COALESCE(mp.sale_cache_creation_30m_input_price, mp.sale_uncached_input_price),
         COALESCE(mp.cache_creation_30m_input_price, mp.uncached_input_price)
             * ccm.multiplier * COALESCE(crf.factor, 1)),
        ('output',
         mp.sale_output_price,
         mp.output_price * ccm.multiplier * COALESCE(crf.factor, 1)),
        ('reasoning_output',
         COALESCE(mp.sale_reasoning_output_price, mp.sale_output_price),
         COALESCE(mp.reasoning_output_price, mp.output_price)
             * ccm.multiplier * COALESCE(crf.factor, 1))
    ) AS rates(component, sale, cost)
    WHERE mp.sale_uncached_input_price IS NOT NULL
      AND mp.effective_from < COALESCE(ccm.effective_to, 'infinity'::timestamptz)
      AND ccm.effective_from < COALESCE(mp.effective_to, 'infinity'::timestamptz)
      AND (
          (p.currency <> mp.currency AND fx.rate IS NULL)
          OR rates.sale * COALESCE(fx.rate, 1) < rates.cost
      )

    UNION ALL

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

-- 3'. cost_snapshots 列还原。
ALTER TABLE public.cost_snapshots
    DROP COLUMN provider_recharge_rate_id,
    DROP COLUMN provider_recharge_rate;

ALTER TABLE public.cost_snapshots
    ADD COLUMN channel_recharge_factor_id bigint,
    ADD COLUMN recharge_factor numeric(20,10);

ALTER TABLE ONLY public.cost_snapshots
    ADD CONSTRAINT cost_snapshots_channel_recharge_factor_id_fkey FOREIGN KEY (channel_recharge_factor_id) REFERENCES public.channel_recharge_factors(id);

-- 4'. settlement_recovery_jobs 列名还原（000029 原始定义无 FK，保持对称：只删新 FK、不加旧 FK）。
ALTER TABLE ONLY public.settlement_recovery_jobs
    DROP CONSTRAINT settlement_recovery_jobs_provider_recharge_rate_id_fkey;
UPDATE public.settlement_recovery_jobs SET provider_recharge_rate_id = NULL;
ALTER TABLE public.settlement_recovery_jobs
    RENAME COLUMN provider_recharge_rate_id TO channel_recharge_factor_id;

-- 5'. provider_ledger_entries USD 快照列移除。
ALTER TABLE public.provider_ledger_entries
    DROP CONSTRAINT ck_provider_ledger_entries_fx;
ALTER TABLE public.provider_ledger_entries
    DROP COLUMN amount_usd,
    DROP COLUMN fx_rate,
    DROP COLUMN fx_rate_date;

-- 6'. 服务商级充值汇率表移除。
DROP TABLE public.provider_recharge_rates;
