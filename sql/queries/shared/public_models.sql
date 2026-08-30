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
-- ListPublicModelPriceWindows 取 since 之后仍生效过的全部价格窗口，用于重建折扣走势。
--
-- 不需要额外的采样任务：改价的实现就是「关闭旧窗口（写 effective_to）+ 新建一条」，
-- model_prices 本身就是一份逐次调价的完整事实账，按窗口回放即可精确还原任意时刻的价格。
--
-- 停用窗口必须参与回放：替换售价会把旧窗口停用，但它在停用前真实计费过，
-- 只回放 enabled 会让每次调价把之前的走势整段抹掉（图表只剩「上次调价以来」）。
-- 停用窗口的生效终点取 LEAST(effective_to, updated_at)——updated_at 即停用动作时刻，
-- 手动停用（不写 effective_to）也能收口；从未开始就被撤销的窗口区间为空，自然不出现。
WITH windows AS (
    SELECT
        mp.model_id,
        m.model_id AS model_key,
        mp.status AS window_status,
        mp.effective_from,
        (CASE
            WHEN mp.status = 'enabled' THEN mp.effective_to
            ELSE LEAST(COALESCE(mp.effective_to, mp.updated_at), mp.updated_at)
        END)::timestamptz AS effective_until,
        mp.uncached_input_price,
        mp.output_price,
        mp.sale_price_ratio,
        mp.sale_uncached_input_price,
        mp.sale_output_price
    FROM model_prices mp
    JOIN models m ON m.id = mp.model_id
    WHERE m.status = 'enabled'
)
SELECT
    model_id,
    model_key,
    window_status,
    effective_from,
    effective_until,
    uncached_input_price,
    output_price,
    sale_price_ratio,
    sale_uncached_input_price,
    sale_output_price
FROM windows
WHERE effective_from < now()
  AND (effective_until IS NULL OR effective_until > sqlc.arg(since)::timestamptz)
ORDER BY model_id ASC, effective_from ASC;

-- name: ListPublicModelCapabilities :many
-- ListPublicModelCapabilities 列出全部在售模型的能力声明（service 层按 model_id 分组）。
-- 与 ListPublicModels 相同的在售口径，避免为下架模型搬运能力。
-- 带出字典图标与展示名：console/website 统一用后端下发的图标 + 文案展示能力，前端不自备图标库。
-- 排序跟随字典 sort_order（模态在前、功能在后），前端按序直接渲染。
SELECT
    mc.model_id,
    mc.capability_key,
    mc.support_level,
    ck.icon_svg,
    ck.display_name
FROM model_capabilities mc
JOIN models m ON m.id = mc.model_id
JOIN capability_keys ck ON ck.key = mc.capability_key
WHERE m.status = 'enabled'
ORDER BY mc.model_id ASC, ck.sort_order ASC, mc.capability_key ASC;
