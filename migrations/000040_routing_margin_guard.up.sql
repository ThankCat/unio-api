-- P3 商业毛利硬门槛：所有相关配置在事务提交前统一校验。
-- 触发器延迟到事务末执行，使 route + route_channels 和价格窗口的多步写入可以原子完成。
CREATE OR REPLACE FUNCTION public.assert_non_negative_route_margins()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $$
DECLARE
    violation record;
BEGIN
    SELECT v.* INTO violation
    FROM (
        -- 绝对渠道成本覆盖：逐计价分项比较，空的可选分项按 billing fallback 规则归一。
        SELECT
            rt.id AS route_id,
            cm.channel_id,
            cm.model_id,
            rates.component,
            rates.sale,
            rates.cost
        FROM routes rt
        JOIN route_channels rc ON rc.route_id = rt.id
        JOIN channels c ON c.id = rc.channel_id AND c.status = 'enabled'
        JOIN providers p ON p.id = c.provider_id AND p.status = 'enabled'
        JOIN channel_models cm ON cm.channel_id = c.id AND cm.status = 'enabled'
        JOIN models m ON m.id = cm.model_id AND m.status = 'enabled'
        JOIN model_prices mp ON mp.model_id = m.id AND mp.status = 'enabled'
        JOIN channel_prices cp ON cp.channel_id = c.id AND cp.model_id = m.id AND cp.status = 'enabled'
        CROSS JOIN LATERAL (VALUES
            ('uncached_input',       mp.uncached_input_price * rt.price_ratio, cp.uncached_input_cost),
            ('cache_read_input',     COALESCE(mp.cache_read_input_price, mp.uncached_input_price) * rt.price_ratio, COALESCE(cp.cache_read_input_cost, cp.uncached_input_cost)),
            ('cache_write_5m_input', COALESCE(mp.cache_write_5m_input_price, mp.uncached_input_price) * rt.price_ratio, COALESCE(cp.cache_write_5m_input_cost, cp.uncached_input_cost)),
            ('cache_write_1h_input', COALESCE(mp.cache_write_1h_input_price, mp.uncached_input_price) * rt.price_ratio, COALESCE(cp.cache_write_1h_input_cost, cp.uncached_input_cost)),
            ('cache_write_30m_input', COALESCE(mp.cache_write_30m_input_price, mp.uncached_input_price) * rt.price_ratio, COALESCE(cp.cache_write_30m_input_cost, cp.uncached_input_cost)),
            ('output',               mp.output_price * rt.price_ratio, cp.output_cost),
            ('reasoning_output',     COALESCE(mp.reasoning_output_price, mp.output_price) * rt.price_ratio, COALESCE(cp.reasoning_output_cost, cp.output_cost))
        ) AS rates(component, sale, cost)
        WHERE rt.status = 'enabled'
          AND mp.effective_from < COALESCE(cp.effective_to, 'infinity'::timestamptz)
          AND cp.effective_from < COALESCE(mp.effective_to, 'infinity'::timestamptz)
          AND (
              mp.currency <> cp.currency
              OR mp.pricing_unit <> cp.pricing_unit
              OR rates.sale < rates.cost
          )

        UNION ALL

        -- 倍率成本路径：基准价两侧相消，线路倍率必须覆盖价格倍率和所有重叠充值倍率。
        SELECT
            rt.id AS route_id,
            cm.channel_id,
            cm.model_id,
            CASE WHEN crf.id IS NULL THEN 'cost_multiplier' ELSE 'cost_multiplier_x_recharge' END AS component,
            rt.price_ratio AS sale,
            ccm.multiplier * COALESCE(crf.factor, 1) AS cost
        FROM routes rt
        JOIN route_channels rc ON rc.route_id = rt.id
        JOIN channels c ON c.id = rc.channel_id AND c.status = 'enabled'
        JOIN providers p ON p.id = c.provider_id AND p.status = 'enabled'
        JOIN channel_models cm ON cm.channel_id = c.id AND cm.status = 'enabled'
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
        WHERE rt.status = 'enabled'
          AND mp.effective_from < COALESCE(ccm.effective_to, 'infinity'::timestamptz)
          AND ccm.effective_from < COALESCE(mp.effective_to, 'infinity'::timestamptz)
          AND rt.price_ratio < ccm.multiplier * COALESCE(crf.factor, 1)
    ) v
    LIMIT 1;

    IF FOUND THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            CONSTRAINT = 'ck_non_negative_route_margin',
            MESSAGE = format(
                'negative margin: route=%s channel=%s model=%s component=%s sale=%s cost=%s',
                violation.route_id, violation.channel_id, violation.model_id,
                violation.component, violation.sale, violation.cost
            ),
            DETAIL = json_build_object(
                'route_id', violation.route_id,
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

CREATE CONSTRAINT TRIGGER trg_routes_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON routes DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_route_margins();
CREATE CONSTRAINT TRIGGER trg_route_channels_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON route_channels DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_route_margins();
CREATE CONSTRAINT TRIGGER trg_models_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON models DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_route_margins();
CREATE CONSTRAINT TRIGGER trg_channels_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON channels DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_route_margins();
CREATE CONSTRAINT TRIGGER trg_providers_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON providers DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_route_margins();
CREATE CONSTRAINT TRIGGER trg_channel_models_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON channel_models DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_route_margins();
CREATE CONSTRAINT TRIGGER trg_model_prices_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON model_prices DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_route_margins();
CREATE CONSTRAINT TRIGGER trg_channel_prices_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON channel_prices DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_route_margins();
CREATE CONSTRAINT TRIGGER trg_channel_cost_multipliers_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON channel_cost_multipliers DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_route_margins();
CREATE CONSTRAINT TRIGGER trg_channel_recharge_factors_margin_guard
AFTER INSERT OR UPDATE OR DELETE ON channel_recharge_factors DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_non_negative_route_margins();
