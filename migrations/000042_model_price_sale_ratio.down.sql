-- 回退到全局售价倍率。
--
-- 有损：模型级倍率被折叠回一个全局值，无法从各行反推出原来那个全局值
-- （改造后不同模型可以有不同倍率）。这里恢复为 1.0，即按基准价原价卖——
-- 不会亏本，但也不等于回退前的定价。回退后需要人工确认倍率取值。
INSERT INTO public.app_settings (key, value, description)
VALUES (
    'gateway.model_sale_price_ratio',
    jsonb_build_object('ratio', '1.0'),
    '模型售价倍率（由 000042 回退重建，取值需人工确认）'
)
ON CONFLICT (key) DO NOTHING;

-- 恢复 000041 定义的守卫：倍率读 app_settings 的单个全局值。
CREATE OR REPLACE FUNCTION public.assert_non_negative_margins()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $$
DECLARE
    violation record;
    sale_ratio NUMERIC;
BEGIN
    SELECT COALESCE((value ->> 'ratio')::numeric, 1.0)
    INTO sale_ratio
    FROM app_settings
    WHERE key = 'gateway.model_sale_price_ratio';

    IF sale_ratio IS NULL THEN
        sale_ratio := 1.0;
    END IF;

    SELECT v.* INTO violation
    FROM (
        -- A. 绝对成本覆盖 × 绝对售价：两侧都是绝对值，逐项比较。
        SELECT
            cm.channel_id,
            cm.model_id,
            'standard/' || rates.component AS component,
            rates.sale,
            rates.cost
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled'
        JOIN providers p ON p.id = c.provider_id AND p.status = 'enabled'
        JOIN models m ON m.id = cm.model_id AND m.status = 'enabled'
        JOIN model_prices mp ON mp.model_id = m.id AND mp.status = 'enabled'
        JOIN channel_prices cp ON cp.channel_id = c.id AND cp.model_id = m.id AND cp.status = 'enabled'
        CROSS JOIN LATERAL (VALUES
            ('uncached_input',
             COALESCE(mp.sale_uncached_input_price, mp.uncached_input_price * sale_ratio),
             cp.uncached_input_cost),
            ('cache_read_input',
             COALESCE(mp.sale_cache_read_input_price, mp.sale_uncached_input_price,
                      COALESCE(mp.cache_read_input_price, mp.uncached_input_price) * sale_ratio),
             COALESCE(cp.cache_read_input_cost, cp.uncached_input_cost)),
            ('cache_write_5m_input',
             COALESCE(mp.sale_cache_write_5m_input_price, mp.sale_uncached_input_price,
                      COALESCE(mp.cache_write_5m_input_price, mp.uncached_input_price) * sale_ratio),
             COALESCE(cp.cache_write_5m_input_cost, cp.uncached_input_cost)),
            ('cache_write_1h_input',
             COALESCE(mp.sale_cache_write_1h_input_price, mp.sale_uncached_input_price,
                      COALESCE(mp.cache_write_1h_input_price, mp.uncached_input_price) * sale_ratio),
             COALESCE(cp.cache_write_1h_input_cost, cp.uncached_input_cost)),
            ('cache_write_30m_input',
             COALESCE(mp.sale_cache_write_30m_input_price, mp.sale_uncached_input_price,
                      COALESCE(mp.cache_write_30m_input_price, mp.uncached_input_price) * sale_ratio),
             COALESCE(cp.cache_write_30m_input_cost, cp.uncached_input_cost)),
            ('output',
             COALESCE(mp.sale_output_price, mp.output_price * sale_ratio),
             cp.output_cost),
            ('reasoning_output',
             COALESCE(mp.sale_reasoning_output_price, mp.sale_output_price,
                      COALESCE(mp.reasoning_output_price, mp.output_price) * sale_ratio),
             COALESCE(cp.reasoning_output_cost, cp.output_cost))
        ) AS rates(component, sale, cost)
        WHERE mp.effective_from < COALESCE(cp.effective_to, 'infinity'::timestamptz)
          AND cp.effective_from < COALESCE(mp.effective_to, 'infinity'::timestamptz)
          AND (
              mp.currency <> cp.currency
              OR mp.pricing_unit <> cp.pricing_unit
              OR rates.sale < rates.cost
          )

        UNION ALL

        -- B. 倍率成本 × 倍率售价：基准价两侧相消，退化为标量比较。
        SELECT
            cm.channel_id,
            cm.model_id,
            CASE WHEN crf.id IS NULL THEN 'standard/cost_multiplier'
                 ELSE 'standard/cost_multiplier_x_recharge' END AS component,
            sale_ratio AS sale,
            ccm.multiplier * COALESCE(crf.factor, 1) AS cost
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
        WHERE mp.sale_uncached_input_price IS NULL
          AND mp.effective_from < COALESCE(ccm.effective_to, 'infinity'::timestamptz)
          AND ccm.effective_from < COALESCE(mp.effective_to, 'infinity'::timestamptz)
          AND sale_ratio < ccm.multiplier * COALESCE(crf.factor, 1)

        UNION ALL

        -- C. 倍率成本 × 绝对售价：售价侧无基准价公因子，必须逐项还原后比较。
        SELECT
            cm.channel_id,
            cm.model_id,
            'standard/' || rates.component AS component,
            rates.sale,
            rates.cost
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
        CROSS JOIN LATERAL (VALUES
            ('uncached_input',
             mp.sale_uncached_input_price,
             mp.uncached_input_price * ccm.multiplier * COALESCE(crf.factor, 1)),
            ('cache_read_input',
             COALESCE(mp.sale_cache_read_input_price, mp.sale_uncached_input_price),
             COALESCE(mp.cache_read_input_price, mp.uncached_input_price)
                 * ccm.multiplier * COALESCE(crf.factor, 1)),
            ('cache_write_5m_input',
             COALESCE(mp.sale_cache_write_5m_input_price, mp.sale_uncached_input_price),
             COALESCE(mp.cache_write_5m_input_price, mp.uncached_input_price)
                 * ccm.multiplier * COALESCE(crf.factor, 1)),
            ('cache_write_1h_input',
             COALESCE(mp.sale_cache_write_1h_input_price, mp.sale_uncached_input_price),
             COALESCE(mp.cache_write_1h_input_price, mp.uncached_input_price)
                 * ccm.multiplier * COALESCE(crf.factor, 1)),
            ('cache_write_30m_input',
             COALESCE(mp.sale_cache_write_30m_input_price, mp.sale_uncached_input_price),
             COALESCE(mp.cache_write_30m_input_price, mp.uncached_input_price)
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
          AND rates.sale < rates.cost

        UNION ALL

        -- D. Fast 档：售价与成本各有独立来源，与 Standard 无约分关系，必须单独比。
        SELECT
            cm.channel_id,
            cm.model_id,
            'fast/' || rates.component AS component,
            rates.sale,
            rates.cost
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
        CROSS JOIN LATERAL (VALUES
            ('uncached_input',
             fmp.uncached_input_price * sale_ratio, fcp.uncached_input_cost),
            ('cache_read_input',
             COALESCE(fmp.cache_read_input_price, fmp.uncached_input_price) * sale_ratio,
             COALESCE(fcp.cache_read_input_cost, fcp.uncached_input_cost)),
            ('cache_write_5m_input',
             COALESCE(fmp.cache_write_5m_input_price, fmp.uncached_input_price) * sale_ratio,
             COALESCE(fcp.cache_write_5m_input_cost, fcp.uncached_input_cost)),
            ('cache_write_1h_input',
             COALESCE(fmp.cache_write_1h_input_price, fmp.uncached_input_price) * sale_ratio,
             COALESCE(fcp.cache_write_1h_input_cost, fcp.uncached_input_cost)),
            ('cache_write_30m_input',
             COALESCE(fmp.cache_write_30m_input_price, fmp.uncached_input_price) * sale_ratio,
             COALESCE(fcp.cache_write_30m_input_cost, fcp.uncached_input_cost)),
            ('output', fmp.output_price * sale_ratio, fcp.output_cost),
            ('reasoning_output',
             COALESCE(fmp.reasoning_output_price, fmp.output_price) * sale_ratio,
             COALESCE(fcp.reasoning_output_cost, fcp.output_cost))
        ) AS rates(component, sale, cost)
        WHERE mp.effective_from < COALESCE(cp.effective_to, 'infinity'::timestamptz)
          AND cp.effective_from < COALESCE(mp.effective_to, 'infinity'::timestamptz)
          AND rates.sale < rates.cost
    ) v
    LIMIT 1;

    IF FOUND THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            CONSTRAINT = 'ck_non_negative_margin',
            MESSAGE = format(
                'negative margin: channel=%s model=%s component=%s sale=%s cost=%s',
                violation.channel_id, violation.model_id,
                violation.component, violation.sale, violation.cost
            ),
            DETAIL = json_build_object(
                'channel_id', violation.channel_id,
                'model_id', violation.model_id,
                'component', violation.component,
                'sale', violation.sale,
                'cost', violation.cost
            )::text;
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER trg_app_settings_sale_ratio_margin_guard
AFTER INSERT OR UPDATE ON app_settings DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
WHEN (NEW.key = 'gateway.model_sale_price_ratio')
EXECUTE FUNCTION assert_non_negative_margins();

COMMENT ON COLUMN public.model_prices.sale_uncached_input_price IS
    '模型对外绝对售价；整组为空时回退「基准价 × 全局售价倍率」。可选分项为空按 billing fallback 归一。';
COMMENT ON COLUMN public.model_price_service_tiers.sale_uncached_input_price IS
    'Fast 档对外绝对售价；整组为空时回退「该档基准价 × 全局售价倍率」。';

ALTER TABLE public.model_prices
    DROP CONSTRAINT IF EXISTS ck_model_prices_sale_configured;

ALTER TABLE public.model_prices
    DROP CONSTRAINT IF EXISTS ck_model_prices_sale_ratio_positive;

ALTER TABLE public.model_prices
    DROP COLUMN IF EXISTS sale_price_ratio;
