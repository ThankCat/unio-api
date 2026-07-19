-- name: FindRouteCandidates :many
-- FindRouteCandidates 按请求模型、用户策略与线路查找可用 channel 路由候选（DEC-026 售价倍率 + DEC-027 成本倍率 + DEC-031 单基数）。
-- 在既有过滤（model/channel/provider/cm enabled + 协议 + 用户 allow/deny）之上叠加：
--   1. 线路候选池：候选必须属于 route_channels（fixed 即只剩一条）；
--   2. 已定价过滤：候选必须有 model_prices 基准价（base，INNER JOIN 保证），且渠道成本可解析——
--      「有 channel_prices 绝对成本覆盖」 OR 「有 channel_cost_multipliers 价格倍率」（否则排除，不参与计费）；
--   3. 带回基准价（base）：Go 侧 × 线路倍率算客户售价（DEC-026），并 × 价格倍率 × 充值倍率算真实成本（DEC-031 同一基数）；
--      成本三来源：绝对覆盖 cost（若有）/ 价格倍率 mult + 充值倍率 recharge（供 Go 侧 ScaleProviderCostByFactors 派生真实成本与毛利结算），带回来源行 id 作 pin。
-- 成本解析优先级（Go 侧）：绝对覆盖 > 基准价 × 价格倍率 × 充值倍率（缺省 1.0）；排序/策略在 Go 侧完成，此处仅给稳定 priority 基序。
-- DEC-031：退役 model_reference_costs，成本基数复用 base（model_prices）；成本基数 pin = model_price_id（base.id）。
WITH user_scope AS (
    SELECT sqlc.arg(user_id)::BIGINT AS user_id
),
user_policy_mode AS (
    SELECT EXISTS (
        SELECT 1
        FROM user_model_policies ump
        JOIN user_scope us ON us.user_id = ump.user_id
        WHERE ump.visibility = 'allowed'
    ) AS has_allow_list
)
SELECT
    m.id AS model_db_id,
    m.model_id AS requested_model_id,
    m.max_output_tokens AS model_max_output_tokens,
    p.id AS provider_id,
    p.slug AS provider_slug,
    c.adapter_key AS adapter_key,
    c.protocol AS protocol,
    c.id AS channel_id,
    c.name AS channel_name,
    c.base_url,
    c.credential,
    c.timeout_ms,
    c.priority,
    c.rpm_limit AS channel_rpm_limit,
    c.tpm_limit AS channel_tpm_limit,
    c.rpd_limit AS channel_rpd_limit,
    c.concurrency_limit AS channel_concurrency_limit,
    c.upstream_bills_on_disconnect AS channel_bills_on_disconnect,
    cm.upstream_model,
    base.id AS model_price_id,
    base.currency AS base_currency,
    base.pricing_unit AS base_pricing_unit,
    base.uncached_input_price,
    base.cache_read_input_price,
    base.cache_write_5m_input_price,
    base.cache_write_1h_input_price,
    base.cache_write_30m_input_price,
    base.output_price,
    base.reasoning_output_price,
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
    cost.cache_write_5m_input_cost,
    cost.cache_write_1h_input_cost,
    cost.cache_write_30m_input_cost,
    cost.output_cost,
    cost.reasoning_output_cost,
    COALESCE(mult.id, 0)::bigint AS channel_cost_multiplier_id,
    mult.multiplier AS cost_multiplier,
    COALESCE(recharge.id, 0)::bigint AS channel_recharge_factor_id,
    recharge.factor AS recharge_factor
FROM channel_models cm
JOIN models m ON m.id = cm.model_id
JOIN channels c ON c.id = cm.channel_id
JOIN providers p ON p.id = c.provider_id
JOIN user_scope us ON us.user_id > 0
JOIN LATERAL (
    -- base: 模型当前生效的基准价（DEC-026/DEC-031，售价与成本的唯一基数）。
    -- 客户售价 = base × 线路倍率；渠道真实成本（倍率路径）= base × 价格倍率 × 充值倍率。
    SELECT mp.id, mp.currency, mp.pricing_unit,
        mp.uncached_input_price, mp.cache_read_input_price,
        mp.cache_write_5m_input_price, mp.cache_write_1h_input_price,
        mp.cache_write_30m_input_price,
        mp.output_price, mp.reasoning_output_price,
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
LEFT JOIN LATERAL (
    -- cost: 命中渠道当前生效的绝对成本覆盖（channel_prices，优先级最高，可空）。
    SELECT cp.id, cp.currency, cp.pricing_unit,
        cp.uncached_input_cost, cp.cache_read_input_cost,
        cp.cache_write_5m_input_cost, cp.cache_write_1h_input_cost,
        cp.cache_write_30m_input_cost,
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
    -- recharge: 渠道当前生效的充值倍率（账户级，可空；缺省 Go 侧按 1.0）。
    SELECT crf.id, crf.factor
    FROM channel_recharge_factors crf
    WHERE crf.channel_id = c.id
      AND crf.status = 'enabled'
      AND crf.effective_from <= sqlc.arg(at_time)
      AND (crf.effective_to IS NULL OR crf.effective_to > sqlc.arg(at_time))
    ORDER BY crf.effective_from DESC, crf.id DESC
    LIMIT 1
) recharge ON TRUE
WHERE m.model_id = sqlc.arg(requested_model_id)
  AND c.protocol = sqlc.arg(ingress_protocol)
  AND m.status = 'enabled'
  AND cm.status = 'enabled'
  AND c.status = 'enabled'
  AND c.credential_valid
  AND p.status = 'enabled'
  -- 已定价（DEC-031）：base 基准价 INNER JOIN 已保证存在；成本可解析 = 绝对覆盖存在 OR 价格倍率存在。
  AND (cost.id IS NOT NULL OR mult.id IS NOT NULL)
  AND EXISTS (
        SELECT 1
        FROM route_channels rc
        WHERE rc.route_id = sqlc.arg(route_id)
          AND rc.channel_id = c.id
  )
  AND NOT EXISTS (
    SELECT 1
    FROM user_model_policies denied
    JOIN user_scope us ON us.user_id = denied.user_id
    WHERE denied.model_id = m.id
      AND denied.visibility = 'denied'
)
  AND (
    NOT (SELECT has_allow_list FROM user_policy_mode)
        OR EXISTS (
        SELECT 1
        FROM user_model_policies allowed
        JOIN user_scope us ON us.user_id = allowed.user_id
        WHERE allowed.model_id = m.id
          AND allowed.visibility = 'allowed'
    )
    )
ORDER BY
    c.priority ASC,
    c.id ASC;
