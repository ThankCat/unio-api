-- Migration 000052 down: cache_creation -> cache_write 全量回滚

-- 1) usage_records
ALTER TABLE public.usage_records RENAME COLUMN cache_creation_5m_input_tokens TO cache_write_5m_input_tokens;
ALTER TABLE public.usage_records RENAME COLUMN cache_creation_5m_input_tokens_state TO cache_write_5m_input_tokens_state;
ALTER TABLE public.usage_records RENAME COLUMN cache_creation_1h_input_tokens TO cache_write_1h_input_tokens;
ALTER TABLE public.usage_records RENAME COLUMN cache_creation_1h_input_tokens_state TO cache_write_1h_input_tokens_state;
ALTER TABLE public.usage_records RENAME COLUMN cache_creation_30m_input_tokens TO cache_write_30m_input_tokens;
ALTER TABLE public.usage_records RENAME COLUMN cache_creation_30m_input_tokens_state TO cache_write_30m_input_tokens_state;

-- 2) settlement_recovery_jobs
ALTER TABLE public.settlement_recovery_jobs RENAME COLUMN usage_cache_creation_5m_input_tokens TO usage_cache_write_5m_input_tokens;
ALTER TABLE public.settlement_recovery_jobs RENAME COLUMN usage_cache_creation_5m_input_tokens_state TO usage_cache_write_5m_input_tokens_state;
ALTER TABLE public.settlement_recovery_jobs RENAME COLUMN usage_cache_creation_1h_input_tokens TO usage_cache_write_1h_input_tokens;
ALTER TABLE public.settlement_recovery_jobs RENAME COLUMN usage_cache_creation_1h_input_tokens_state TO usage_cache_write_1h_input_tokens_state;
ALTER TABLE public.settlement_recovery_jobs RENAME COLUMN usage_cache_creation_30m_input_tokens TO usage_cache_write_30m_input_tokens;
ALTER TABLE public.settlement_recovery_jobs RENAME COLUMN usage_cache_creation_30m_input_tokens_state TO usage_cache_write_30m_input_tokens_state;
ALTER TABLE public.settlement_recovery_jobs RENAME COLUMN cache_creation_5m_input_price TO cache_write_5m_input_price;
ALTER TABLE public.settlement_recovery_jobs RENAME COLUMN cache_creation_1h_input_price TO cache_write_1h_input_price;
ALTER TABLE public.settlement_recovery_jobs RENAME COLUMN cache_creation_30m_input_price TO cache_write_30m_input_price;

-- 3) cost_snapshots
ALTER TABLE public.cost_snapshots RENAME COLUMN cache_creation_5m_input_cost TO cache_write_5m_input_cost;
ALTER TABLE public.cost_snapshots RENAME COLUMN cache_creation_5m_input_cost_amount TO cache_write_5m_input_cost_amount;
ALTER TABLE public.cost_snapshots RENAME COLUMN cache_creation_1h_input_cost TO cache_write_1h_input_cost;
ALTER TABLE public.cost_snapshots RENAME COLUMN cache_creation_1h_input_cost_amount TO cache_write_1h_input_cost_amount;
ALTER TABLE public.cost_snapshots RENAME COLUMN cache_creation_30m_input_cost TO cache_write_30m_input_cost;
ALTER TABLE public.cost_snapshots RENAME COLUMN cache_creation_30m_input_cost_amount TO cache_write_30m_input_cost_amount;

-- 4) price_snapshots
ALTER TABLE public.price_snapshots RENAME COLUMN cache_creation_5m_input_price TO cache_write_5m_input_price;
ALTER TABLE public.price_snapshots RENAME COLUMN cache_creation_1h_input_price TO cache_write_1h_input_price;
ALTER TABLE public.price_snapshots RENAME COLUMN cache_creation_30m_input_price TO cache_write_30m_input_price;

-- 5) model_prices
ALTER TABLE public.model_prices RENAME COLUMN cache_creation_5m_input_price TO cache_write_5m_input_price;
ALTER TABLE public.model_prices RENAME COLUMN cache_creation_1h_input_price TO cache_write_1h_input_price;
ALTER TABLE public.model_prices RENAME COLUMN cache_creation_30m_input_price TO cache_write_30m_input_price;
ALTER TABLE public.model_prices RENAME COLUMN sale_cache_creation_5m_input_price TO sale_cache_write_5m_input_price;
ALTER TABLE public.model_prices RENAME COLUMN sale_cache_creation_1h_input_price TO sale_cache_write_1h_input_price;
ALTER TABLE public.model_prices RENAME COLUMN sale_cache_creation_30m_input_price TO sale_cache_write_30m_input_price;

-- 6) channel_prices
ALTER TABLE public.channel_prices RENAME COLUMN cache_creation_5m_input_cost TO cache_write_5m_input_cost;
ALTER TABLE public.channel_prices RENAME COLUMN cache_creation_1h_input_cost TO cache_write_1h_input_cost;
ALTER TABLE public.channel_prices RENAME COLUMN cache_creation_30m_input_cost TO cache_write_30m_input_cost;

-- 7) model_price_service_tiers
ALTER TABLE public.model_price_service_tiers RENAME COLUMN cache_creation_5m_input_price TO cache_write_5m_input_price;
ALTER TABLE public.model_price_service_tiers RENAME COLUMN cache_creation_1h_input_price TO cache_write_1h_input_price;
ALTER TABLE public.model_price_service_tiers RENAME COLUMN cache_creation_30m_input_price TO cache_write_30m_input_price;
ALTER TABLE public.model_price_service_tiers RENAME COLUMN sale_cache_creation_5m_input_price TO sale_cache_write_5m_input_price;
ALTER TABLE public.model_price_service_tiers RENAME COLUMN sale_cache_creation_1h_input_price TO sale_cache_write_1h_input_price;
ALTER TABLE public.model_price_service_tiers RENAME COLUMN sale_cache_creation_30m_input_price TO sale_cache_write_30m_input_price;

-- 8) channel_price_service_tiers
ALTER TABLE public.channel_price_service_tiers RENAME COLUMN cache_creation_5m_input_cost TO cache_write_5m_input_cost;
ALTER TABLE public.channel_price_service_tiers RENAME COLUMN cache_creation_1h_input_cost TO cache_write_1h_input_cost;
ALTER TABLE public.channel_price_service_tiers RENAME COLUMN cache_creation_30m_input_cost TO cache_write_30m_input_cost;

-- 9) model_catalog
ALTER TABLE public.model_catalog RENAME COLUMN cache_creation_price_usd_per_million_tokens TO cache_write_price_usd_per_million_tokens;

COMMENT ON COLUMN public.model_catalog.cache_write_price_usd_per_million_tokens IS '缓存写参考价基线（USD/百万 token），仅展示，绝不用于计费。';

-- 10) 恢复 settlement_recovery_jobs 的 6 个约束原名（up 中显式改为 srj_ 短名）
ALTER TABLE public.settlement_recovery_jobs RENAME CONSTRAINT srj_usage_cache_creation_5m_input_tokens_check TO settlement_recovery_jobs_usage_cache_write_5m_input_token_check;
ALTER TABLE public.settlement_recovery_jobs RENAME CONSTRAINT srj_usage_cache_creation_5m_input_tokens_state_check TO settlement_recovery_jobs_usage_cache_write_5m_input_toke_check1;
ALTER TABLE public.settlement_recovery_jobs RENAME CONSTRAINT srj_usage_cache_creation_1h_input_tokens_check TO settlement_recovery_jobs_usage_cache_write_1h_input_token_check;
ALTER TABLE public.settlement_recovery_jobs RENAME CONSTRAINT srj_usage_cache_creation_1h_input_tokens_state_check TO settlement_recovery_jobs_usage_cache_write_1h_input_toke_check1;
ALTER TABLE public.settlement_recovery_jobs RENAME CONSTRAINT srj_usage_cache_creation_30m_input_tokens_check TO settlement_recovery_jobs_usage_cache_write_30m_input_toke_check;
ALTER TABLE public.settlement_recovery_jobs RENAME CONSTRAINT srj_usage_cache_creation_30m_input_tokens_state_check TO settlement_recovery_jobs_usage_cache_write_30m_input_tok_check1;

-- 11) 其余约束反向重命名
DO $$
DECLARE
    r record;
BEGIN
    FOR r IN
        SELECT conrelid::regclass AS tbl, conname
        FROM pg_constraint
        WHERE conname LIKE '%cache_creation%'
    LOOP
        EXECUTE format(
            'ALTER TABLE %s RENAME CONSTRAINT %I TO %I',
            r.tbl, r.conname, replace(r.conname, 'cache_creation', 'cache_write')
        );
    END LOOP;
END $$;

-- 12) 恢复 000044 原版毛利守卫函数（cache_write 列名与维度标签）。
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
