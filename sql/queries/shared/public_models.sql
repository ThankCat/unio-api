-- name: ListPublicModels :many
-- ListPublicModels 面向 console 与 website 两个公开面的在售模型目录：
-- enabled 模型 × 当前生效基准售价（无生效价格 = 不可售，INNER JOIN 自然剔除）
-- × Fast 档（可选）× 能力声明聚合。售价解析（绝对售价 or 基准 × 倍率）在 service 层
-- 复用 core/billing.ResolveCustomerPrice，保证「标价即结算单价」。
--
-- ex_model_prices_enabled_window 排除约束保证同一模型 + 币种 + 计价单位最多一个
-- 生效窗口；当前只运营 USD 单币种，故本查询按「每模型一行」消费。
SELECT
    m.id,
    m.model_id,
    m.display_name,
    m.owned_by,
    m.family,
    m.description,
    m.knowledge_cutoff,
    m.context_window_tokens,
    m.max_output_tokens,
    m.release_date,
    mp.currency,
    mp.pricing_unit,
    mp.uncached_input_price,
    mp.cache_read_input_price,
    mp.cache_creation_5m_input_price,
    mp.cache_creation_1h_input_price,
    mp.cache_creation_30m_input_price,
    mp.output_price,
    mp.reasoning_output_price,
    mp.sale_price_ratio,
    mp.sale_uncached_input_price,
    mp.sale_cache_read_input_price,
    mp.sale_cache_creation_5m_input_price,
    mp.sale_cache_creation_1h_input_price,
    mp.sale_cache_creation_30m_input_price,
    mp.sale_output_price,
    mp.sale_reasoning_output_price,
    mp.long_context_enabled,
    mp.long_context_threshold,
    mp.long_context_input_multiplier,
    mp.long_context_output_multiplier,
    t.uncached_input_price AS fast_uncached_input_price,
    t.cache_read_input_price AS fast_cache_read_input_price,
    t.cache_creation_5m_input_price AS fast_cache_creation_5m_input_price,
    t.cache_creation_1h_input_price AS fast_cache_creation_1h_input_price,
    t.cache_creation_30m_input_price AS fast_cache_creation_30m_input_price,
    t.output_price AS fast_output_price,
    t.reasoning_output_price AS fast_reasoning_output_price,
    t.sale_uncached_input_price AS fast_sale_uncached_input_price,
    t.sale_cache_read_input_price AS fast_sale_cache_read_input_price,
    t.sale_cache_creation_5m_input_price AS fast_sale_cache_creation_5m_input_price,
    t.sale_cache_creation_1h_input_price AS fast_sale_cache_creation_1h_input_price,
    t.sale_cache_creation_30m_input_price AS fast_sale_cache_creation_30m_input_price,
    t.sale_output_price AS fast_sale_output_price,
    t.sale_reasoning_output_price AS fast_sale_reasoning_output_price,
    (t.model_price_id IS NOT NULL)::boolean AS fast_configured,
    EXISTS (
        SELECT 1 FROM model_labs l
        WHERE l.slug = m.owned_by AND l.logo_svg <> ''
    ) AS lab_has_logo,
    mp.effective_from AS price_effective_from
FROM models m
JOIN model_prices mp
    ON mp.model_id = m.id
    AND mp.status = 'enabled'
    AND mp.effective_from <= now()
    AND (mp.effective_to IS NULL OR mp.effective_to > now())
LEFT JOIN model_price_service_tiers t
    ON t.model_price_id = mp.id
    AND t.service_tier = 'fast'
WHERE m.status = 'enabled'
ORDER BY m.owned_by ASC, m.release_date DESC NULLS LAST, m.model_id ASC;

-- name: ListPublicModelPriceWindows :many
-- ListPublicModelPriceWindows 取 since 之后仍有效过的全部价格窗口，用于重建折扣走势。
--
-- 不需要额外的采样任务：改价的实现就是「关闭旧窗口（写 effective_to）+ 新建一条」，
-- model_prices 本身就是一份逐次调价的完整事实账，按窗口回放即可精确还原任意时刻的价格。
-- 只取 enabled：被撤销的窗口从未对客户生效过，不该出现在对外走势里。
SELECT
    mp.model_id,
    m.model_id AS model_key,
    mp.effective_from,
    mp.effective_to,
    mp.uncached_input_price,
    mp.output_price,
    mp.sale_price_ratio,
    mp.sale_uncached_input_price,
    mp.sale_output_price
FROM model_prices mp
JOIN models m ON m.id = mp.model_id
WHERE m.status = 'enabled'
  AND mp.status = 'enabled'
  AND mp.effective_from < now()
  AND (mp.effective_to IS NULL OR mp.effective_to > sqlc.arg(since)::timestamptz)
ORDER BY mp.model_id ASC, mp.effective_from ASC;

-- name: ListPublicModelCapabilities :many
-- ListPublicModelCapabilities 列出全部在售模型的能力声明（service 层按 model_id 分组）。
-- 与 ListPublicModels 相同的在售口径，避免为下架模型搬运能力。
SELECT
    mc.model_id,
    mc.capability_key,
    mc.support_level
FROM model_capabilities mc
JOIN models m ON m.id = mc.model_id
WHERE m.status = 'enabled'
ORDER BY mc.model_id ASC, mc.capability_key ASC;
