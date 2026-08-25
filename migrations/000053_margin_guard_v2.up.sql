-- 毛利硬门槛（模型为中心版本）：任何配置写入若造成售价低于成本，事务提交前拒绝。
--
-- 与 000037 的差异：
--   1. 供给面不再经 routes/route_channels，改为 channel_models 直连；
--   2. 售价有两条解析路径——模型绝对售价（逐项比较）与「基准价 × 全局售价倍率」
--      （可约分为标量比较）。绝对售价那条无法约分，因为售价侧不再含基准价这个公因子；
--   3. 新增 Fast 档校验。Fast 售价来自 model_price_service_tiers、成本来自
--      channel_price_service_tiers，是与 Standard 无关的独立价格对，原实现未覆盖。
--
-- 长上下文不需校验：售价与成本使用同一 LongContextPolicy 的同一对倍率，
-- 逐项比较下 `售价 × k >= 成本 × k` 与 `售价 >= 成本` 等价，结论完全等同 Standard。
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
        --    仅在模型未配置绝对售价时适用；配了绝对售价走分支 C。
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
        --    仅在两侧都配置了 Fast 时校验；缺任一侧时结算会回落 Standard，由上面分支覆盖。
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

CREATE CONSTRAINT TRIGGER trg_models_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON models DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_margins();
CREATE CONSTRAINT TRIGGER trg_channels_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON channels DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_margins();
CREATE CONSTRAINT TRIGGER trg_providers_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON providers DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_margins();
CREATE CONSTRAINT TRIGGER trg_channel_models_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON channel_models DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_margins();
CREATE CONSTRAINT TRIGGER trg_model_prices_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON model_prices DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_margins();
CREATE CONSTRAINT TRIGGER trg_model_price_service_tiers_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON model_price_service_tiers DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_margins();
CREATE CONSTRAINT TRIGGER trg_channel_prices_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON channel_prices DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_margins();
CREATE CONSTRAINT TRIGGER trg_channel_price_service_tiers_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON channel_price_service_tiers DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_margins();
CREATE CONSTRAINT TRIGGER trg_channel_cost_multipliers_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON channel_cost_multipliers DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_margins();
CREATE CONSTRAINT TRIGGER trg_channel_recharge_factors_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON channel_recharge_factors DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_margins();

-- 全局售价倍率也在守卫范围内：它是倍率路径下售价公式的一半，
-- 把它调低到某个模型的成本线以下，和直接改错模型价格是同一件事。
-- WHEN 收窄到这一个 key，其余设置（超时、熔断等）与毛利无关，不必为它们跑全量扫描。
CREATE CONSTRAINT TRIGGER trg_app_settings_sale_ratio_margin_guard
AFTER INSERT OR UPDATE ON app_settings DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
WHEN (NEW.key = 'gateway.model_sale_price_ratio')
EXECUTE FUNCTION assert_non_negative_margins();
