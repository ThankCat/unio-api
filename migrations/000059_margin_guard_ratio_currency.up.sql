-- 毛利守卫：倍率路径成本币种感知（D2 修订，docs/changes/2026-08-29-multi-currency）。
--
-- 000058 只让绝对成本分支（A/D1/D2）跨币种；倍率分支（B/C）当时假设「成本币种 = 基准价币种」。
-- D2 修订后倍率路径成本按 provider 结算币种记账（基准价数值 × 价格倍率 × 充值倍率 = 原币金额，
-- 充值倍率只承载名义额度折价、不再folding汇率），因此 B/C 需要同样的跨币种处理：
--   B（标量约分）：sale_ratio × rate ≥ multiplier × factor（sale 侧折到成本币种，纯乘法）；
--   C（逐项）：sale × rate ≥ base × multiplier × factor；
-- 跨币种且无汇率 = 违规（保守拒绝）。A/D0/D1/D2 与 000058 完全一致。
-- 仍以 DO/EXECUTE 包裹（sqlc 无法解析此形态视图 DDL）。
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
    --    D2 修订：成本币种 = provider 结算币种，跨币种时 sale_ratio × rate ≥ multiplier × factor。
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

    -- C. 倍率成本 × 绝对售价：售价侧无基准价公因子，逐项还原后比较。
    --    D2 修订：成本币种 = provider 结算币种，跨币种按最新汇率乘法比较。
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
