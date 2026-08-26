-- 绝对售价与倍率是两套独立实体：绝对售价是整组覆盖，不能和倍率混算。
-- Fast 与 Standard 绑在同一套实体上：有绝对售价时 Fast 必须也有绝对售价，不能回落倍率。

CREATE OR REPLACE FUNCTION public.assert_non_negative_margins()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $$
DECLARE
    violation record;
BEGIN
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
             COALESCE(mp.sale_uncached_input_price, mp.uncached_input_price * mp.sale_price_ratio),
             cp.uncached_input_cost),
            ('cache_read_input',
             COALESCE(mp.sale_cache_read_input_price, mp.sale_uncached_input_price,
                      COALESCE(mp.cache_read_input_price, mp.uncached_input_price) * mp.sale_price_ratio),
             COALESCE(cp.cache_read_input_cost, cp.uncached_input_cost)),
            ('cache_write_5m_input',
             COALESCE(mp.sale_cache_write_5m_input_price, mp.sale_uncached_input_price,
                      COALESCE(mp.cache_write_5m_input_price, mp.uncached_input_price) * mp.sale_price_ratio),
             COALESCE(cp.cache_write_5m_input_cost, cp.uncached_input_cost)),
            ('cache_write_1h_input',
             COALESCE(mp.sale_cache_write_1h_input_price, mp.sale_uncached_input_price,
                      COALESCE(mp.cache_write_1h_input_price, mp.uncached_input_price) * mp.sale_price_ratio),
             COALESCE(cp.cache_write_1h_input_cost, cp.uncached_input_cost)),
            ('cache_write_30m_input',
             COALESCE(mp.sale_cache_write_30m_input_price, mp.sale_uncached_input_price,
                      COALESCE(mp.cache_write_30m_input_price, mp.uncached_input_price) * mp.sale_price_ratio),
             COALESCE(cp.cache_write_30m_input_cost, cp.uncached_input_cost)),
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
              mp.currency <> cp.currency
              OR mp.pricing_unit <> cp.pricing_unit
              OR rates.sale < rates.cost
          )

        UNION ALL

        -- B. 倍率成本 × 倍率售价：基准价两侧相消，退化为标量比较。
        --    仅在模型未配置绝对售价、且配了倍率时适用；配了绝对售价走分支 C。
        --    草稿行（倍率和绝对售价都空）不进入亏本比较。
        SELECT
            cm.channel_id,
            cm.model_id,
            CASE WHEN crf.id IS NULL THEN 'standard/cost_multiplier'
                 ELSE 'standard/cost_multiplier_x_recharge' END AS component,
            mp.sale_price_ratio AS sale,
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
          AND mp.sale_price_ratio IS NOT NULL
          AND mp.effective_from < COALESCE(ccm.effective_to, 'infinity'::timestamptz)
          AND ccm.effective_from < COALESCE(mp.effective_to, 'infinity'::timestamptz)
          AND mp.sale_price_ratio < ccm.multiplier * COALESCE(crf.factor, 1)

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

        -- D0. Fast 与 Standard 必须走同一套售价实体：绝对售价同在或同不在。
        --     Standard 走绝对、Fast 走倍率（或反过来）是混算，直接判违规。
        SELECT
            cm.channel_id,
            cm.model_id,
            'fast/absolute_incomplete' AS component,
            0::numeric AS sale,
            1::numeric AS cost
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled'
        JOIN providers p ON p.id = c.provider_id AND p.status = 'enabled'
        JOIN models m ON m.id = cm.model_id AND m.status = 'enabled'
        JOIN model_prices mp ON mp.model_id = m.id AND mp.status = 'enabled'
        JOIN model_price_service_tiers fmp
          ON fmp.model_price_id = mp.id AND fmp.service_tier = 'fast'
        WHERE (mp.sale_uncached_input_price IS NULL) <> (fmp.sale_uncached_input_price IS NULL)

        UNION ALL

        -- D1. 绝对售价实体：两边都有绝对售价。Fast 只比自己的绝对售价，倍率不参与。
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
             fmp.sale_uncached_input_price,
             fcp.uncached_input_cost),
            ('cache_read_input',
             COALESCE(fmp.sale_cache_read_input_price, fmp.sale_uncached_input_price),
             COALESCE(fcp.cache_read_input_cost, fcp.uncached_input_cost)),
            ('cache_write_5m_input',
             COALESCE(fmp.sale_cache_write_5m_input_price, fmp.sale_uncached_input_price),
             COALESCE(fcp.cache_write_5m_input_cost, fcp.uncached_input_cost)),
            ('cache_write_1h_input',
             COALESCE(fmp.sale_cache_write_1h_input_price, fmp.sale_uncached_input_price),
             COALESCE(fcp.cache_write_1h_input_cost, fcp.uncached_input_cost)),
            ('cache_write_30m_input',
             COALESCE(fmp.sale_cache_write_30m_input_price, fmp.sale_uncached_input_price),
             COALESCE(fcp.cache_write_30m_input_cost, fcp.uncached_input_cost)),
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
          AND (rates.sale IS NULL OR rates.sale < rates.cost)

        UNION ALL

        -- D2. 倍率实体：两边都没有绝对售价。Fast 售价 = Fast 基准价 × 模型倍率。
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
             fmp.uncached_input_price * mp.sale_price_ratio,
             fcp.uncached_input_cost),
            ('cache_read_input',
             COALESCE(fmp.cache_read_input_price, fmp.uncached_input_price) * mp.sale_price_ratio,
             COALESCE(fcp.cache_read_input_cost, fcp.uncached_input_cost)),
            ('cache_write_5m_input',
             COALESCE(fmp.cache_write_5m_input_price, fmp.uncached_input_price) * mp.sale_price_ratio,
             COALESCE(fcp.cache_write_5m_input_cost, fcp.uncached_input_cost)),
            ('cache_write_1h_input',
             COALESCE(fmp.cache_write_1h_input_price, fmp.uncached_input_price) * mp.sale_price_ratio,
             COALESCE(fcp.cache_write_1h_input_cost, fcp.uncached_input_cost)),
            ('cache_write_30m_input',
             COALESCE(fmp.cache_write_30m_input_price, fmp.uncached_input_price) * mp.sale_price_ratio,
             COALESCE(fcp.cache_write_30m_input_cost, fcp.uncached_input_cost)),
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
          AND (rates.sale IS NULL OR rates.sale < rates.cost)
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

COMMENT ON COLUMN public.model_prices.sale_uncached_input_price IS
    '模型对外绝对售价；整组非空时覆盖倍率，Standard 与 Fast 必须同在。整组为空时回退「基准价 × 同行 sale_price_ratio」。两者都空则此行不可售。';

COMMENT ON COLUMN public.model_prices.sale_price_ratio IS
    '模型售价倍率：绝对售价整组留空时，客户售价 = 基准价 × 本倍率。可与绝对售价同行共存，但绝对售价生效时倍率不参与计算。';
