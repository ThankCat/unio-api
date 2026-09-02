-- name: FindModelCandidates :many
-- FindModelCandidates 按请求模型与入口协议查找可用 channel 候选。
-- 供给的根是 Model：能服务该模型的渠道即候选。
--   0. 供给形态：credential 型渠道持一份 API Key（存量形态）；pool 型渠道自身不持凭据，
--      凭据在其下的订阅账号上，故池内至少要有一个 enabled 账号才算「能服务」——
--      零账号池允许存在（建池后还没导号），但不产生候选；
--   1. 供给过滤：model/channel/provider/binding 四级 enabled + 凭据有效 + 协议匹配；
--      协议用 protocols 数组包含判定，一条渠道可同时服务 openai 与 anthropic；
--   2. 已定价过滤：候选必须有 model_prices 基准价（base，INNER JOIN 保证），且渠道成本可解析——
--      「有 channel_prices 绝对成本覆盖」 OR 「有 channel_cost_multipliers 价格倍率」（否则排除，不参与计费）；
--   3. 带回基准价（base）：既是客户售价的回退基数（× 模型倍率 sale_price_ratio），也是渠道成本基数
--      （× 价格倍率 × 充值倍率，DEC-031 同一基数）；
--      成本三来源：绝对覆盖 cost（若有）/ 价格倍率 mult + 充值倍率 recharge（供 Go 侧
--      ScaleProviderCostByFactors 派生真实成本与毛利结算），带回来源行 id 作 pin。
-- 成本解析优先级（Go 侧）：绝对覆盖 > 基准价 × 价格倍率 × 充值倍率（缺省 1.0）；排序/策略在 Go 侧完成，此处仅给稳定 priority 基序。
SELECT
    m.id AS model_db_id,
    m.model_id AS requested_model_id,
    m.max_output_tokens AS model_max_output_tokens,
    p.id AS provider_id,
    p.slug AS provider_slug,
    -- 倍率路径成本按 provider 结算币种记账（D2 修订）：基准价数值 × 倍率 = 原币金额，
    -- 比较/报表时按当日汇率折算；充值倍率只承载「名义额度 → 原币支出」的折价，不再folding汇率。
    p.currency AS provider_currency,
    c.adapter_key AS adapter_key,
    sqlc.arg(ingress_protocol)::text AS protocol,
    c.id AS channel_id,
    c.name AS channel_name,
    p.origin,
    p.origin_revision AS provider_origin_revision,
    p.status_revision AS provider_status_revision,
    c.config_revision AS channel_config_revision,
    c.capacity_revision AS channel_capacity_revision,
    c.credential,
    c.supply_form,
    -- 账号并发回落链的中间档：账号 concurrency_limit 为 NULL 时用它，仍为 NULL 时用全局默认。
    c.account_default_concurrency,
    c.supports_openai_fast,
    c.response_timeout_ms,
    c.first_token_timeout_ms,
    c.priority,
    c.concurrency_limit AS channel_concurrency_limit,
    c.sticky_enabled AS channel_sticky_enabled,
    c.sticky_ttl_ms AS channel_sticky_ttl_ms,
    cm.upstream_model,
    base.id AS model_price_id,
    base.currency AS base_currency,
    base.pricing_unit AS base_pricing_unit,
    base.uncached_input_price,
    base.cache_read_input_price,
    base.cache_creation_5m_input_price,
    base.cache_creation_1h_input_price,
    base.cache_creation_30m_input_price,
    base.output_price,
    base.reasoning_output_price,
    COALESCE(fast_base.id, 0)::bigint AS fast_model_price_service_tier_id,
    fast_base.uncached_input_price AS fast_uncached_input_price,
    fast_base.cache_read_input_price AS fast_cache_read_input_price,
    fast_base.cache_creation_5m_input_price AS fast_cache_creation_5m_input_price,
    fast_base.cache_creation_1h_input_price AS fast_cache_creation_1h_input_price,
    fast_base.cache_creation_30m_input_price AS fast_cache_creation_30m_input_price,
    fast_base.output_price AS fast_output_price,
    fast_base.reasoning_output_price AS fast_reasoning_output_price,
    fast_base.sale_uncached_input_price AS fast_sale_uncached_input_price,
    fast_base.sale_cache_read_input_price AS fast_sale_cache_read_input_price,
    fast_base.sale_cache_creation_5m_input_price AS fast_sale_cache_creation_5m_input_price,
    fast_base.sale_cache_creation_1h_input_price AS fast_sale_cache_creation_1h_input_price,
    fast_base.sale_cache_creation_30m_input_price AS fast_sale_cache_creation_30m_input_price,
    fast_base.sale_output_price AS fast_sale_output_price,
    fast_base.sale_reasoning_output_price AS fast_sale_reasoning_output_price,
    -- 售价的两种表达都挂在模型自己的价格行上，是两套独立实体：绝对售价整组非空时
    -- Standard 与 Fast 都只走绝对售价；否则 Go 侧回退 base × sale_price_ratio。
    -- 两者都空则该行不可售，候选会被排除。Fast 与 Standard 必须走同一套实体，不能混算。
    -- 倍率不分档，故这里只带一列。
    base.sale_price_ratio,
    base.sale_uncached_input_price,
    base.sale_cache_read_input_price,
    base.sale_cache_creation_5m_input_price,
    base.sale_cache_creation_1h_input_price,
    base.sale_cache_creation_30m_input_price,
    base.sale_output_price,
    base.sale_reasoning_output_price,
    base.long_context_enabled AS base_long_context_enabled,
    base.long_context_threshold AS base_long_context_threshold,
    base.long_context_input_multiplier AS base_long_context_input_multiplier,
    base.long_context_output_multiplier AS base_long_context_output_multiplier,
    -- LEFT JOIN 引入的可空 id/text 列用 COALESCE 归一（0/'' = 该来源缺失），避免 sqlc 误判为非空导致 Scan NULL 失败；
    -- 数值成本列保持原样（pgtype.Numeric 可承载 NULL）。Go 侧按 id != 0 判定来源是否命中。
    COALESCE(cost.id, 0)::bigint AS channel_price_id,
    COALESCE(cost.currency, '')::text AS cost_currency,
    COALESCE(cost.pricing_unit, '')::text AS cost_pricing_unit,
    cost.uncached_input_cost,
    cost.cache_read_input_cost,
    cost.cache_creation_5m_input_cost,
    cost.cache_creation_1h_input_cost,
    cost.cache_creation_30m_input_cost,
    cost.output_cost,
    cost.reasoning_output_cost,
    COALESCE(fast_cost.id, 0)::bigint AS fast_channel_price_service_tier_id,
    fast_cost.uncached_input_cost AS fast_uncached_input_cost,
    fast_cost.cache_read_input_cost AS fast_cache_read_input_cost,
    fast_cost.cache_creation_5m_input_cost AS fast_cache_creation_5m_input_cost,
    fast_cost.cache_creation_1h_input_cost AS fast_cache_creation_1h_input_cost,
    fast_cost.cache_creation_30m_input_cost AS fast_cache_creation_30m_input_cost,
    fast_cost.output_cost AS fast_output_cost,
    fast_cost.reasoning_output_cost AS fast_reasoning_output_cost,
    COALESCE(mult.id, 0)::bigint AS channel_cost_multiplier_id,
    mult.multiplier AS cost_multiplier,
    COALESCE(recharge.id, 0)::bigint AS provider_recharge_rate_id,
    recharge.rate AS provider_recharge_rate
FROM channel_models cm
JOIN models m ON m.id = cm.model_id
JOIN channels c ON c.id = cm.channel_id
JOIN providers p ON p.id = c.provider_id
JOIN LATERAL (
    -- base: 模型当前生效的基准价（DEC-026/DEC-031，售价与成本的唯一基数）。
    -- 客户售价 = base × 模型倍率 sale_price_ratio；渠道真实成本（倍率路径）= base × 价格倍率 × 充值倍率。
    SELECT mp.id, mp.currency, mp.pricing_unit,
        mp.uncached_input_price, mp.cache_read_input_price,
        mp.cache_creation_5m_input_price, mp.cache_creation_1h_input_price,
        mp.cache_creation_30m_input_price,
        mp.output_price, mp.reasoning_output_price,
        mp.sale_price_ratio,
        mp.sale_uncached_input_price, mp.sale_cache_read_input_price,
        mp.sale_cache_creation_5m_input_price, mp.sale_cache_creation_1h_input_price,
        mp.sale_cache_creation_30m_input_price,
        mp.sale_output_price, mp.sale_reasoning_output_price,
        mp.long_context_enabled, mp.long_context_threshold,
        mp.long_context_input_multiplier, mp.long_context_output_multiplier
    FROM model_prices mp
    WHERE mp.model_id = m.id
      AND mp.status = 'enabled'
      AND mp.effective_from <= sqlc.arg(at_time)
      AND (mp.effective_to IS NULL OR mp.effective_to > sqlc.arg(at_time))
    ORDER BY mp.effective_from DESC, mp.id DESC
    LIMIT 1
) base ON TRUE
LEFT JOIN model_price_service_tiers fast_base
    ON fast_base.model_price_id = base.id AND fast_base.service_tier = 'fast'
LEFT JOIN LATERAL (
    -- cost: 命中渠道当前生效的绝对成本覆盖（channel_prices，优先级最高，可空）。
    SELECT cp.id, cp.currency, cp.pricing_unit,
        cp.uncached_input_cost, cp.cache_read_input_cost,
        cp.cache_creation_5m_input_cost, cp.cache_creation_1h_input_cost,
        cp.cache_creation_30m_input_cost,
        cp.output_cost, cp.reasoning_output_cost
    FROM channel_prices cp
    WHERE cp.channel_id = c.id
      AND cp.model_id = m.id
      AND cp.status = 'enabled'
      AND cp.effective_from <= sqlc.arg(at_time)
      AND (cp.effective_to IS NULL OR cp.effective_to > sqlc.arg(at_time))
    ORDER BY cp.effective_from DESC, cp.id DESC
    LIMIT 1
) cost ON TRUE
LEFT JOIN channel_price_service_tiers fast_cost
    ON fast_cost.channel_price_id = cost.id AND fast_cost.service_tier = 'fast'
LEFT JOIN LATERAL (
    -- mult: 渠道当前生效的价格倍率，优先逐模型覆盖、回退渠道默认（可空）。
    SELECT ccm.id, ccm.multiplier
    FROM channel_cost_multipliers ccm
    WHERE ccm.channel_id = c.id
      AND (ccm.model_id = m.id OR ccm.model_id IS NULL)
      AND ccm.status = 'enabled'
      AND ccm.effective_from <= sqlc.arg(at_time)
      AND (ccm.effective_to IS NULL OR ccm.effective_to > sqlc.arg(at_time))
    ORDER BY (ccm.model_id IS NULL) ASC, ccm.effective_from DESC, ccm.id DESC
    LIMIT 1
) mult ON TRUE
LEFT JOIN LATERAL (
    -- recharge: 服务商当前生效的充值汇率（服务商级，其下所有渠道共享）。
    SELECT prr.id, prr.rate
    FROM provider_recharge_rates prr
    WHERE prr.provider_id = p.id
      AND prr.status = 'enabled'
      AND prr.effective_from <= sqlc.arg(at_time)
      AND (prr.effective_to IS NULL OR prr.effective_to > sqlc.arg(at_time))
    ORDER BY prr.effective_from DESC, prr.id DESC
    LIMIT 1
) recharge ON TRUE
WHERE m.model_id = sqlc.arg(requested_model_id)
  AND sqlc.arg(ingress_protocol)::text = ANY(c.protocols)
  AND m.status = 'enabled'
  AND cm.status = 'enabled'
  AND c.status = 'enabled'
  AND c.credential_valid
  AND p.status = 'enabled'
  -- 池型渠道的供给单元是账号：一个可调度账号都没有时不产生候选，与「渠道被熔断」是两回事，
  -- 运维界面据此把「池空」和 breaker open 显示成两个事实。账号自身 enabled 即可，
  -- 渠道与服务商两层已由上面的条件保证（绑定式语义：可调度 = 账号 ∧ 渠道 ∧ 服务商）。
  AND (
      c.supply_form <> 'pool'
      OR EXISTS (
          SELECT 1 FROM subscription_accounts sa
          WHERE sa.channel_id = c.id AND sa.status = 'enabled'
      )
  )
  -- 已定价（DEC-031）：base 基准价 INNER JOIN 已保证存在；成本可解析 = 绝对覆盖存在 OR 价格倍率存在。
  AND (cost.id IS NOT NULL OR mult.id IS NOT NULL)
  -- D-02 严格拦截：服务商未配置当前生效充值汇率时，其渠道不进入路由候选（真实成本口径不明，禁止产生新结算）。
  AND recharge.id IS NOT NULL
ORDER BY
    c.priority ASC,
    c.id ASC;
