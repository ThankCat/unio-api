-- 售价倍率从全局设置下沉到模型级：每条 model_prices 行自带 sale_price_ratio，
-- 与同行的 sale_* 绝对售价构成售价的两种表达（绝对售价优先）。
--
--   改造前：客户售价 = sale_*（整组非空时） 或 基准价 × app_settings['gateway.model_sale_price_ratio']
--   改造后：客户售价 = sale_*（整组非空时） 或 基准价 × model_prices.sale_price_ratio
--
-- 全局倍率对所有模型一视同仁，给不了单个模型定价；而改基准价会连带改变成本基数
-- （DEC-031：真实成本 = 基准价 × 渠道成本倍率 × 充值倍率），不是定价手段。
-- 两种表达都保留是因为它们并不等价：倍率让售价随基准价变动自动跟随（上游涨价后
-- 毛利率恒定），绝对售价是钉死的数字。

-- 1) 新列。可空——与 sale_* 至少配一项即可，见 ck_model_prices_sale_configured。
ALTER TABLE public.model_prices
    ADD COLUMN sale_price_ratio numeric(20,10);

COMMENT ON COLUMN public.model_prices.sale_price_ratio IS
    '模型售价倍率：客户售价 = 基准价 × 本倍率。与 sale_* 绝对售价至少配一项，绝对售价优先。';

-- 2) 回填。存量行取删除前的全局倍率，保证改造前后计费结果逐位相同；
--    取不到设置时按 1.0（等于按基准价原价卖，不会亏本）。
UPDATE public.model_prices
SET sale_price_ratio = COALESCE(
        (SELECT (value ->> 'ratio')::numeric
         FROM public.app_settings
         WHERE key = 'gateway.model_sale_price_ratio'),
        1.0
    )
WHERE sale_price_ratio IS NULL;

-- 3) 立即落地回填产生的 DEFERRED 约束触发器事件。
--    两个原因：一是毛利守卫是 CONSTRAINT TRIGGER ... INITIALLY DEFERRED，回填留下
--    pending trigger events 会让后面的 ALTER TABLE 直接报
--    「cannot ALTER TABLE because it has pending trigger events」；
--    二是此刻生效的仍是 000041 的旧守卫（读 app_settings 的全局倍率），用它校验回填
--    结果恰好就是在证明「回填后与改造前等价」——毛利关系不变，这正是回填的目标。
SET CONSTRAINTS ALL IMMEDIATE;

-- 4) 倍率必须为正：0 是白送，负数是倒付钱。
ALTER TABLE public.model_prices
    ADD CONSTRAINT ck_model_prices_sale_ratio_positive
    CHECK (((sale_price_ratio IS NULL) OR (sale_price_ratio > (0)::numeric)));

-- 5) 售价必须可解析：倍率与绝对售价至少给一个，否则这条价格行卖不出去。
--    只判 sale_uncached_input_price 是因为 ck_model_prices_sale_all_or_none 已保证
--    绝对售价整组给齐或整组留空，故这一列非空即代表整组已配。
ALTER TABLE public.model_prices
    ADD CONSTRAINT ck_model_prices_sale_configured
    CHECK (((sale_price_ratio IS NOT NULL) OR (sale_uncached_input_price IS NOT NULL)));

-- ---------------------------------------------------------------------------
-- 毛利守卫重写（承接 000041）
-- ---------------------------------------------------------------------------
-- 与 000041 的差异：
--   1. 倍率不再来自 app_settings 的单个全局值，改为逐行读 mp.sale_price_ratio；
--   2. Fast 档（分支 D）售价侧补上 fmp.sale_*。000041 只算 `基准价 × 倍率`，
--      而运行时 routing 是会读 Fast 绝对售价的——守卫拿一个实际不生效的数去校验，
--      给 Fast 配了绝对售价后亏本组合能写进库。这里一并修掉；
--   3. 分支 D 把「售价解析不出来」（NULL）也判为违规：模型只配了 Standard 绝对售价
--      而没有倍率时，未配绝对售价的 Fast 档没有任何可用售价来源，属于配置不完整，
--      在配置期拦住比等运行时回落 Standard 更清楚。
--
-- 长上下文仍不需校验：售价与成本使用同一 LongContextPolicy 的同一对倍率，
-- 逐项比较下 `售价 × k >= 成本 × k` 与 `售价 >= 成本` 等价，结论完全等同 Standard。
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
        --    此分支下 sale_price_ratio 必非空（ck_model_prices_sale_configured）。
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

        -- D. Fast 档：售价与成本各有独立来源，与 Standard 无约分关系，必须单独比。
        --    仅在两侧都配置了 Fast 时校验；缺任一侧时结算会回落 Standard，由上面分支覆盖。
        --    售价侧与分支 A 同构：Fast 绝对售价 > Standard 绝对售价 > Fast 基准价 × 模型倍率。
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
             COALESCE(fmp.sale_uncached_input_price,
                      fmp.uncached_input_price * mp.sale_price_ratio),
             fcp.uncached_input_cost),
            ('cache_read_input',
             COALESCE(fmp.sale_cache_read_input_price, fmp.sale_uncached_input_price,
                      COALESCE(fmp.cache_read_input_price, fmp.uncached_input_price) * mp.sale_price_ratio),
             COALESCE(fcp.cache_read_input_cost, fcp.uncached_input_cost)),
            ('cache_write_5m_input',
             COALESCE(fmp.sale_cache_write_5m_input_price, fmp.sale_uncached_input_price,
                      COALESCE(fmp.cache_write_5m_input_price, fmp.uncached_input_price) * mp.sale_price_ratio),
             COALESCE(fcp.cache_write_5m_input_cost, fcp.uncached_input_cost)),
            ('cache_write_1h_input',
             COALESCE(fmp.sale_cache_write_1h_input_price, fmp.sale_uncached_input_price,
                      COALESCE(fmp.cache_write_1h_input_price, fmp.uncached_input_price) * mp.sale_price_ratio),
             COALESCE(fcp.cache_write_1h_input_cost, fcp.uncached_input_cost)),
            ('cache_write_30m_input',
             COALESCE(fmp.sale_cache_write_30m_input_price, fmp.sale_uncached_input_price,
                      COALESCE(fmp.cache_write_30m_input_price, fmp.uncached_input_price) * mp.sale_price_ratio),
             COALESCE(fcp.cache_write_30m_input_cost, fcp.uncached_input_cost)),
            ('output',
             COALESCE(fmp.sale_output_price, fmp.output_price * mp.sale_price_ratio),
             fcp.output_cost),
            ('reasoning_output',
             COALESCE(fmp.sale_reasoning_output_price, fmp.sale_output_price,
                      COALESCE(fmp.reasoning_output_price, fmp.output_price) * mp.sale_price_ratio),
             COALESCE(fcp.reasoning_output_cost, fcp.output_cost))
        ) AS rates(component, sale, cost)
        WHERE mp.effective_from < COALESCE(cp.effective_to, 'infinity'::timestamptz)
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

-- 绝对售价两列的注释还写着「回退全局售价倍率」，回退目标已改为同行的 sale_price_ratio。
COMMENT ON COLUMN public.model_prices.sale_uncached_input_price IS
    '模型对外绝对售价；整组为空时回退「基准价 × 同行 sale_price_ratio」。可选分项为空按 billing fallback 归一。';
COMMENT ON COLUMN public.model_price_service_tiers.sale_uncached_input_price IS
    'Fast 档对外绝对售价；整组为空时回退「该档基准价 × 模型 sale_price_ratio」（与 Standard 共用倍率）。';

-- 全局倍率不存在了，挂在 app_settings 上的那个守卫随之卸掉。
-- model_prices 上的守卫（000041 已建）继续覆盖倍率改动。
DROP TRIGGER IF EXISTS trg_app_settings_sale_ratio_margin_guard ON public.app_settings;

-- 清掉存量设置行。注意：只删库不改代码是无效的——appsettings 的 SeedDefaults 会在
-- 下次启动时按 registry 把默认值写回来，所以本行删除必须与代码侧的注册删除同批上线。
DELETE FROM public.app_settings WHERE key = 'gateway.model_sale_price_ratio';
