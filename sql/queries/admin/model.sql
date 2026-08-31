-- name: ListModelCapabilities :many
-- ListModelCapabilities 列出指定模型已声明的全部能力。
SELECT *
FROM model_capabilities
WHERE model_id = sqlc.arg(model_id)
ORDER BY capability_key ASC;

-- name: ListModelsByCapability :many
-- ListModelsByCapability 反查声明了指定能力的模型及其支持级别（cap-tags 与闸门用）。
SELECT *
FROM model_capabilities
WHERE capability_key = sqlc.arg(capability_key)
ORDER BY model_id ASC;

-- name: UpsertModelCapability :one
-- UpsertModelCapability 写入或覆盖模型能力声明（admin 与采纳用）。能力已去 source（阶段 14 Q4）。
INSERT INTO model_capabilities (
    model_id,
    capability_key,
    support_level,
    limits,
    updated_by
)
VALUES (
    sqlc.arg(model_id),
    sqlc.arg(capability_key),
    sqlc.arg(support_level),
    sqlc.arg(limits),
    sqlc.arg(updated_by)
)
ON CONFLICT (model_id, capability_key) DO UPDATE
SET support_level = excluded.support_level,
    limits = excluded.limits,
    updated_by = excluded.updated_by,
    updated_at = now()
RETURNING *;

-- name: DeleteModelCapability :exec
-- DeleteModelCapability 删除指定模型对某能力的声明（admin 手工撤销）。
DELETE FROM model_capabilities
WHERE model_id = sqlc.arg(model_id)
    AND capability_key = sqlc.arg(capability_key);

-- name: DeleteModelCapabilitiesByModel :exec
-- DeleteModelCapabilitiesByModel 清空某模型的全部能力声明（「从目录刷新」整体覆盖前置）。
DELETE FROM model_capabilities
WHERE model_id = sqlc.arg(model_id);

-- name: ListModelCatalogPage :many
-- ListModelCatalogPage 分页/搜索目录条目，连带能力提示数与已采纳次数；q/lab 为 NULL 时不过滤。
SELECT
    mc.*,
    (SELECT COUNT(*) FROM model_catalog_capabilities cc WHERE cc.canonical_id = mc.canonical_id) AS capability_count,
    (SELECT COUNT(*) FROM model_catalog_links l WHERE l.canonical_id = mc.canonical_id) AS adopted_count
FROM model_catalog mc
WHERE (
        sqlc.narg('q')::text IS NULL
        OR mc.canonical_id ILIKE '%' || sqlc.narg('q')::text || '%'
        OR mc.display_name ILIKE '%' || sqlc.narg('q')::text || '%'
    )
  AND (sqlc.narg('lab')::text IS NULL OR mc.lab = sqlc.narg('lab')::text)
ORDER BY mc.canonical_id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountModelCatalog :one
-- CountModelCatalog 返回与 ListModelCatalogPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM model_catalog mc
WHERE (
        sqlc.narg('q')::text IS NULL
        OR mc.canonical_id ILIKE '%' || sqlc.narg('q')::text || '%'
        OR mc.display_name ILIKE '%' || sqlc.narg('q')::text || '%'
    )
  AND (sqlc.narg('lab')::text IS NULL OR mc.lab = sqlc.narg('lab')::text);

-- name: GetModelCatalogEntry :one
-- GetModelCatalogEntry 按 canonical_id 读取单条目录详情（连带已采纳次数）。
SELECT
    mc.*,
    (SELECT COUNT(*) FROM model_catalog_links l WHERE l.canonical_id = mc.canonical_id) AS adopted_count
FROM model_catalog mc
WHERE mc.canonical_id = sqlc.arg(canonical_id);

-- name: ListModelCatalogCapabilities :many
-- ListModelCatalogCapabilities 列出某目录条目的能力提示（采纳预填 / 刷新 diff 用）。
SELECT canonical_id, capability_key, support_level, limits
FROM model_catalog_capabilities
WHERE canonical_id = sqlc.arg(canonical_id)
ORDER BY capability_key ASC;

-- name: CreateModelCatalogLink :one
-- CreateModelCatalogLink 建立「模型 ← 目录条目」采纳关联（采纳事务的一部分）。
INSERT INTO model_catalog_links (
    model_id,
    canonical_id,
    adopted_fingerprint
)
VALUES (
    sqlc.arg(model_id),
    sqlc.arg(canonical_id),
    sqlc.arg(adopted_fingerprint)
)
RETURNING *;

-- name: GetModelCatalogLink :one
-- GetModelCatalogLink 读取某模型的采纳关联（刷新/提醒前置查询）。
SELECT *
FROM model_catalog_links
WHERE model_id = sqlc.arg(model_id);

-- name: UpdateModelCatalogLinkBaseline :exec
-- UpdateModelCatalogLinkBaseline 「从目录刷新」后把采纳基线指纹更新为最新，并清空忽略/稍后提醒状态。
UPDATE model_catalog_links
SET adopted_fingerprint = sqlc.arg(adopted_fingerprint),
    dismissed_fingerprint = NULL,
    reminder_snooze_until = NULL,
    updated_at = now()
WHERE model_id = sqlc.arg(model_id);

-- name: SetModelCatalogLinkDismissed :exec
-- SetModelCatalogLinkDismissed 忽略本次更新：记下被忽略的目录指纹；目录再变到新指纹会重新提醒。
UPDATE model_catalog_links
SET dismissed_fingerprint = sqlc.arg(dismissed_fingerprint),
    updated_at = now()
WHERE model_id = sqlc.arg(model_id);

-- name: SetModelCatalogLinkMuted :exec
-- SetModelCatalogLinkMuted 永久忽略更新（true）/ 取消静音（false）。
UPDATE model_catalog_links
SET reminder_muted = sqlc.arg(reminder_muted),
    updated_at = now()
WHERE model_id = sqlc.arg(model_id);

-- name: SetModelCatalogLinkSnooze :exec
-- SetModelCatalogLinkSnooze 稍后提醒：此时间之前不提醒。
UPDATE model_catalog_links
SET reminder_snooze_until = sqlc.narg(reminder_snooze_until),
    updated_at = now()
WHERE model_id = sqlc.arg(model_id);

-- name: CreateModelPrice :one
-- CreateModelPrice 创建 Standard 基准售价与可选 Fast 精确价格子记录，单条语句保证原子性。
-- replace_overlapping_enabled=true 时先锁定 model，并停用同币种/单位下所有重叠启用窗口；
-- 已经开始的旧窗口截断到新窗口起点，未来窗口保留原时间但停用。未确认替换时仍由排斥约束拒绝重叠。
WITH locked_model AS (
SELECT models.id AS locked_model_id
FROM models
WHERE models.id = sqlc.arg(model_id)
FOR UPDATE
), replaced_prices AS (
UPDATE model_prices mp
SET status = 'disabled',
    effective_to = CASE
        WHEN mp.effective_from < sqlc.arg(effective_from)::timestamptz
            THEN sqlc.arg(effective_from)::timestamptz
        ELSE mp.effective_to
    END,
    updated_at = now()
FROM locked_model
WHERE sqlc.arg(replace_overlapping_enabled)::boolean
  AND sqlc.arg(status)::text = 'enabled'
  AND mp.model_id = locked_model.locked_model_id
  AND mp.currency = sqlc.arg(currency)::text
  AND mp.pricing_unit = sqlc.arg(pricing_unit)::text
  AND mp.status = 'enabled'
  AND mp.effective_from < COALESCE(sqlc.narg(effective_to)::timestamptz, 'infinity'::timestamptz)
  AND sqlc.arg(effective_from)::timestamptz < COALESCE(mp.effective_to, 'infinity'::timestamptz)
RETURNING mp.id AS replaced_price_id
), replacement_barrier AS (
SELECT count(replaced_price_id) AS replaced_count FROM replaced_prices
), created_price AS (
INSERT INTO model_prices (
    model_id,
    currency,
    pricing_unit,
    uncached_input_price,
    cache_read_input_price,
    cache_creation_5m_input_price,
    cache_creation_1h_input_price,
    cache_creation_30m_input_price,
    output_price,
    reasoning_output_price,
    sale_price_ratio,
    sale_uncached_input_price,
    sale_cache_read_input_price,
    sale_cache_creation_5m_input_price,
    sale_cache_creation_1h_input_price,
    sale_cache_creation_30m_input_price,
    sale_output_price,
    sale_reasoning_output_price,
    status,
    effective_from,
    effective_to,
    long_context_enabled,
    long_context_threshold,
    long_context_input_multiplier,
    long_context_output_multiplier
)
SELECT
    sqlc.arg(model_id),
    sqlc.arg(currency),
    sqlc.arg(pricing_unit),
    sqlc.arg(uncached_input_price),
    sqlc.arg(cache_read_input_price),
    sqlc.arg(cache_creation_5m_input_price),
    sqlc.arg(cache_creation_1h_input_price),
    sqlc.arg(cache_creation_30m_input_price),
    sqlc.arg(output_price),
    sqlc.arg(reasoning_output_price),
    sqlc.narg(sale_price_ratio),
    sqlc.narg(sale_uncached_input_price),
    sqlc.narg(sale_cache_read_input_price),
    sqlc.narg(sale_cache_creation_5m_input_price),
    sqlc.narg(sale_cache_creation_1h_input_price),
    sqlc.narg(sale_cache_creation_30m_input_price),
    sqlc.narg(sale_output_price),
    sqlc.narg(sale_reasoning_output_price),
    sqlc.arg(status),
    sqlc.arg(effective_from),
    sqlc.arg(effective_to),
    sqlc.arg(long_context_enabled),
    sqlc.arg(long_context_threshold),
    sqlc.arg(long_context_input_multiplier),
    sqlc.arg(long_context_output_multiplier)
FROM locked_model
CROSS JOIN replacement_barrier
RETURNING *
), created_fast AS (
INSERT INTO model_price_service_tiers (
    model_price_id,
    service_tier,
    uncached_input_price,
    cache_read_input_price,
    cache_creation_5m_input_price,
    cache_creation_1h_input_price,
    cache_creation_30m_input_price,
    output_price,
    reasoning_output_price,
    sale_uncached_input_price,
    sale_cache_read_input_price,
    sale_cache_creation_5m_input_price,
    sale_cache_creation_1h_input_price,
    sale_cache_creation_30m_input_price,
    sale_output_price,
    sale_reasoning_output_price,
    reference_source,
    reference_checked_at
)
SELECT
    created_price.id,
    'fast',
    sqlc.arg(fast_uncached_input_price),
    sqlc.narg(fast_cache_read_input_price),
    sqlc.narg(fast_cache_creation_5m_input_price),
    sqlc.narg(fast_cache_creation_1h_input_price),
    sqlc.narg(fast_cache_creation_30m_input_price),
    sqlc.arg(fast_output_price),
    sqlc.narg(fast_reasoning_output_price),
    sqlc.narg(fast_sale_uncached_input_price),
    sqlc.narg(fast_sale_cache_read_input_price),
    sqlc.narg(fast_sale_cache_creation_5m_input_price),
    sqlc.narg(fast_sale_cache_creation_1h_input_price),
    sqlc.narg(fast_sale_cache_creation_30m_input_price),
    sqlc.narg(fast_sale_output_price),
    sqlc.narg(fast_sale_reasoning_output_price),
    sqlc.narg(fast_reference_source),
    sqlc.narg(fast_reference_checked_at)
FROM created_price
WHERE sqlc.arg(fast_configured)::boolean
RETURNING *
)
SELECT
    created_price.*,
    COALESCE(created_fast.id, 0)::bigint AS fast_service_tier_id,
    created_fast.uncached_input_price AS fast_uncached_input_price,
    created_fast.cache_read_input_price AS fast_cache_read_input_price,
    created_fast.cache_creation_5m_input_price AS fast_cache_creation_5m_input_price,
    created_fast.cache_creation_1h_input_price AS fast_cache_creation_1h_input_price,
    created_fast.cache_creation_30m_input_price AS fast_cache_creation_30m_input_price,
    created_fast.output_price AS fast_output_price,
    created_fast.reasoning_output_price AS fast_reasoning_output_price,
    created_fast.sale_uncached_input_price AS fast_sale_uncached_input_price,
    created_fast.sale_cache_read_input_price AS fast_sale_cache_read_input_price,
    created_fast.sale_cache_creation_5m_input_price AS fast_sale_cache_creation_5m_input_price,
    created_fast.sale_cache_creation_1h_input_price AS fast_sale_cache_creation_1h_input_price,
    created_fast.sale_cache_creation_30m_input_price AS fast_sale_cache_creation_30m_input_price,
    created_fast.sale_output_price AS fast_sale_output_price,
    created_fast.sale_reasoning_output_price AS fast_sale_reasoning_output_price,
    created_fast.reference_source AS fast_reference_source,
    created_fast.reference_checked_at AS fast_reference_checked_at
FROM created_price
LEFT JOIN created_fast ON created_fast.model_price_id = created_price.id;

-- name: GetModelPrice :one
-- GetModelPrice 按主键读取单条模型基准售价。
SELECT * FROM model_prices WHERE id = sqlc.arg(id) LIMIT 1;

-- name: ListModelPricesByModel :many
-- ListModelPricesByModel 列出某模型的全部基准售价（含历史与停用），连带模型对外 ID/展示名，供 admin 管理台展示。
SELECT
    mp.id,
    mp.model_id,
    mp.currency,
    mp.pricing_unit,
    mp.uncached_input_price,
    mp.cache_read_input_price,
    mp.cache_creation_5m_input_price,
    mp.cache_creation_1h_input_price,
    mp.cache_creation_30m_input_price,
    mp.output_price,
    mp.reasoning_output_price,
    -- 售价两种表达：倍率（乘在基准价上）与绝对售价（整组给齐时优先）。
    mp.sale_price_ratio,
    mp.sale_uncached_input_price,
    mp.sale_cache_read_input_price,
    mp.sale_cache_creation_5m_input_price,
    mp.sale_cache_creation_1h_input_price,
    mp.sale_cache_creation_30m_input_price,
    mp.sale_output_price,
    mp.sale_reasoning_output_price,
    mp.status,
    mp.effective_from,
    mp.effective_to,
    mp.created_at,
    mp.updated_at,
    mp.long_context_enabled,
    mp.long_context_threshold,
    mp.long_context_input_multiplier,
    mp.long_context_output_multiplier,
    COALESCE(fast.id, 0)::bigint AS fast_service_tier_id,
    fast.uncached_input_price AS fast_uncached_input_price,
    fast.cache_read_input_price AS fast_cache_read_input_price,
    fast.cache_creation_5m_input_price AS fast_cache_creation_5m_input_price,
    fast.cache_creation_1h_input_price AS fast_cache_creation_1h_input_price,
    fast.cache_creation_30m_input_price AS fast_cache_creation_30m_input_price,
    fast.output_price AS fast_output_price,
    fast.reasoning_output_price AS fast_reasoning_output_price,
    fast.sale_uncached_input_price AS fast_sale_uncached_input_price,
    fast.sale_cache_read_input_price AS fast_sale_cache_read_input_price,
    fast.sale_cache_creation_5m_input_price AS fast_sale_cache_creation_5m_input_price,
    fast.sale_cache_creation_1h_input_price AS fast_sale_cache_creation_1h_input_price,
    fast.sale_cache_creation_30m_input_price AS fast_sale_cache_creation_30m_input_price,
    fast.sale_output_price AS fast_sale_output_price,
    fast.sale_reasoning_output_price AS fast_sale_reasoning_output_price,
    fast.reference_source AS fast_reference_source,
    fast.reference_checked_at AS fast_reference_checked_at,
    m.model_id AS model_external_id,
    m.display_name AS model_display_name
FROM model_prices mp
JOIN models m ON m.id = mp.model_id
LEFT JOIN model_price_service_tiers fast
    ON fast.model_price_id = mp.id AND fast.service_tier = 'fast'
WHERE mp.model_id = sqlc.arg(model_id)
ORDER BY mp.effective_from DESC, mp.id DESC;

-- name: ListEnabledModelPriceWindows :many
-- ListEnabledModelPriceWindows 取某 model 全部启用中的价格生效窗口，供「窗口不重叠」校验；exclude_id 用于更新时排除自身（创建时传 0）。
SELECT id, effective_from, effective_to
FROM model_prices
WHERE model_id = sqlc.arg(model_id)
    AND status = 'enabled'
    AND id <> sqlc.arg(exclude_id);

-- name: UpdateModelPriceWindow :one
-- UpdateModelPriceWindow 调整生效结束时间与启停状态；金额不可改（改价请新建一条），账务可复算。
UPDATE model_prices
SET effective_to = sqlc.arg(effective_to),
    status = sqlc.arg(status),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: LookupModelByID :one
-- LookupModelByID 按内部主键读取模型完整元数据（含能力架构 Layer 1 列）。
SELECT *
FROM models
WHERE id = sqlc.arg(id);

-- name: ListModelsPage :many
-- ListModelsPage 按状态/关键字过滤后分页列出 model，并连带采纳目录追更状态（阶段 14）。
-- status、q 为 NULL 时不过滤；has_update_only=true 时仅列「应提醒」的采纳模型。
-- catalog_* 字段对未采纳模型为 NULL；update_available/should_remind 见 model_catalog_links 设计。
WITH enriched AS (
    SELECT
        m.id,
        m.model_id,
        m.display_name,
        m.owned_by,
        m.status,
        m.description,
        m.knowledge_cutoff,
        m.max_output_tokens,
        m.context_window_tokens,
        m.input_price_usd_per_million_tokens,
        m.output_price_usd_per_million_tokens,
        m.release_date,
        m.family,
        m.disabled_reason,
        m.disabled_at,
        m.source,
        m.created_at,
        m.updated_at,
        l.canonical_id AS catalog_canonical_id,
        l.adopted_fingerprint,
        l.reminder_muted,
        l.reminder_snooze_until,
        l.dismissed_fingerprint,
        mc.fingerprint AS catalog_fingerprint,
        (mc.removed_upstream_at IS NOT NULL)::boolean AS catalog_removed_upstream,
        (
            l.model_id IS NOT NULL
            AND (mc.fingerprint IS DISTINCT FROM l.adopted_fingerprint OR mc.removed_upstream_at IS NOT NULL)
        )::boolean AS update_available,
        (
            l.model_id IS NOT NULL
            AND (mc.fingerprint IS DISTINCT FROM l.adopted_fingerprint OR mc.removed_upstream_at IS NOT NULL)
            AND NOT l.reminder_muted
            AND l.dismissed_fingerprint IS DISTINCT FROM mc.fingerprint
            AND (l.reminder_snooze_until IS NULL OR now() >= l.reminder_snooze_until)
        )::boolean AS should_remind
    FROM models m
    LEFT JOIN model_catalog_links l ON l.model_id = m.id
    LEFT JOIN model_catalog mc ON mc.canonical_id = l.canonical_id
)
SELECT *
FROM enriched
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR model_id ILIKE '%' || sqlc.narg('q')::text || '%'
    OR display_name ILIKE '%' || sqlc.narg('q')::text || '%'
    OR owned_by ILIKE '%' || sqlc.narg('q')::text || '%'
  )
  AND (NOT sqlc.arg('has_update_only')::bool OR should_remind)
ORDER BY model_id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountModels :one
-- CountModels 返回与 ListModelsPage 相同过滤条件下的总条数（含 has_update_only）。
SELECT COUNT(*) AS total
FROM models m
LEFT JOIN model_catalog_links l ON l.model_id = m.id
LEFT JOIN model_catalog mc ON mc.canonical_id = l.canonical_id
WHERE (sqlc.narg('status')::text IS NULL OR m.status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR m.model_id ILIKE '%' || sqlc.narg('q')::text || '%'
    OR m.display_name ILIKE '%' || sqlc.narg('q')::text || '%'
    OR m.owned_by ILIKE '%' || sqlc.narg('q')::text || '%'
  )
  AND (
    NOT sqlc.arg('has_update_only')::bool
    OR (
        l.model_id IS NOT NULL
        AND (mc.fingerprint IS DISTINCT FROM l.adopted_fingerprint OR mc.removed_upstream_at IS NOT NULL)
        AND NOT l.reminder_muted
        AND l.dismissed_fingerprint IS DISTINCT FROM mc.fingerprint
        AND (l.reminder_snooze_until IS NULL OR now() >= l.reminder_snooze_until)
    )
  );

-- name: CreateModel :one
-- CreateModel 创建 admin 空白手建模型；source 固定 manual。
-- model_id 全局唯一由 DB 唯一约束保证；元数据（上下文/价格基线/发布日期/简介/知识截止）可选填，纯展示不参与计费（阶段 14 Q5）。
INSERT INTO models (
    model_id,
    display_name,
    owned_by,
    status,
    description,
    knowledge_cutoff,
    max_output_tokens,
    context_window_tokens,
    input_price_usd_per_million_tokens,
    output_price_usd_per_million_tokens,
    release_date,
    source
)
VALUES (
    sqlc.arg(model_id),
    sqlc.arg(display_name),
    sqlc.arg(owned_by),
    sqlc.arg(status),
    sqlc.arg(description),
    sqlc.arg(knowledge_cutoff),
    sqlc.narg(max_output_tokens),
    sqlc.narg(context_window_tokens),
    sqlc.narg(input_price_usd_per_million_tokens),
    sqlc.narg(output_price_usd_per_million_tokens),
    sqlc.narg(release_date),
    'manual'
)
RETURNING *;

-- name: CreateModelFromCatalog :one
-- CreateModelFromCatalog 从 models.dev 目录采纳创建模型；source=catalog（采纳后仍完全可编辑）。
-- 与 model_capabilities、model_catalog_links 在同一事务内写入（见 service 层采纳事务）。
-- model_id 采纳界面可自由填写（默认去前缀模型名），全局唯一由 DB 约束保证。
INSERT INTO models (
    model_id,
    display_name,
    owned_by,
    family,
    status,
    description,
    knowledge_cutoff,
    max_output_tokens,
    context_window_tokens,
    input_price_usd_per_million_tokens,
    output_price_usd_per_million_tokens,
    release_date,
    source
)
VALUES (
    sqlc.arg(model_id),
    sqlc.arg(display_name),
    sqlc.arg(owned_by),
    sqlc.arg(family),
    sqlc.arg(status),
    sqlc.arg(description),
    sqlc.arg(knowledge_cutoff),
    sqlc.narg(max_output_tokens),
    sqlc.narg(context_window_tokens),
    sqlc.narg(input_price_usd_per_million_tokens),
    sqlc.narg(output_price_usd_per_million_tokens),
    sqlc.narg(release_date),
    'catalog'
)
RETURNING *;

-- name: UpdateModel :one
-- UpdateModel 更新 model 的展示元数据与启停状态；model_id 作为对外稳定标识不可变，source 不在此修改。
-- 元数据（上下文/价格基线/发布日期/简介/知识截止）可编辑，也可被「从目录刷新」覆盖；纯展示不参与计费。
UPDATE models
SET display_name = sqlc.arg(display_name),
    owned_by = sqlc.arg(owned_by),
    status = sqlc.arg(status),
    description = sqlc.arg(description),
    knowledge_cutoff = sqlc.arg(knowledge_cutoff),
    max_output_tokens = sqlc.narg(max_output_tokens),
    context_window_tokens = sqlc.narg(context_window_tokens),
    input_price_usd_per_million_tokens = sqlc.narg(input_price_usd_per_million_tokens),
    output_price_usd_per_million_tokens = sqlc.narg(output_price_usd_per_million_tokens),
    release_date = sqlc.narg(release_date),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: RefreshAdoptedModelFromCatalog :one
-- RefreshAdoptedModelFromCatalog 用目录最新值覆盖采纳模型的元数据（不动 model_id/display_name 可选）。
-- 「从目录刷新」事务的一部分；能力与 link 基线由同事务的其他查询处理。
UPDATE models
SET display_name = sqlc.arg(display_name),
    owned_by = sqlc.arg(owned_by),
    family = sqlc.arg(family),
    description = sqlc.arg(description),
    knowledge_cutoff = sqlc.arg(knowledge_cutoff),
    max_output_tokens = sqlc.narg(max_output_tokens),
    context_window_tokens = sqlc.narg(context_window_tokens),
    input_price_usd_per_million_tokens = sqlc.narg(input_price_usd_per_million_tokens),
    output_price_usd_per_million_tokens = sqlc.narg(output_price_usd_per_million_tokens),
    release_date = sqlc.narg(release_date),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: LookupModelByModelID :one
-- LookupModelByModelID 按对外模型 ID 读取模型完整元数据（含能力架构 Layer 1 列）。
SELECT *
FROM models
WHERE model_id = sqlc.arg(model_id);

-- name: DeleteModelCascade :execrows
-- DeleteModelCascade 物理删除 model，用于清理录错且从未使用的脏数据，并在同一条语句内
-- 级联清理 model 自身的配置子表：model_prices（基准售价）及其 Fast 档子行（model_price_service_tiers）、
-- channel_prices（渠道-模型成本价）及其 Fast 档子行（channel_price_service_tiers）、
-- channel_models（模型绑定）、channel_cost_multipliers 的逐模型覆盖行（model_id=本 model，DEC-027）；
-- model_capabilities、model_catalog_links 由 ON DELETE CASCADE 自动清理，无需在此显式删除。
-- 这些价格/绑定/倍率覆盖表都是追加式配置（无删除接口，只能停用），若不在此一并清理，任何配过价/倍率覆盖的
-- model 都会被自身配置行永久挡住删除（均无请求/账务语义，属纯配置）。
-- 运维观测数据同样不该挡住删除：channel_model_verification_items（渠道模型验证结果）随模型一并删除；
-- provider_probe_records 是不可变探测事实、可能被 provider_ledger_entries（探测成本）引用，不删行，
-- 仅把可空的 model_id 置 NULL 解除归因，渠道级事实（upstream_model 文本、成本）原样保留。
-- 注意 channel_cost_multipliers.model_id 可空：NULL=渠道默认倍率（对全部模型生效，不随单个 model 删除），
-- 非空=对本 model 的覆盖；WHERE model_id = id 只删覆盖行，渠道默认行保留不动。
-- 外键均为默认 NO ACTION（约束在语句末校验），故 CTE 删子表 + 删主体在单条语句内原子完成：
-- 子配置先删除，语句末 models 的删除不会留下悬挂引用。若 model 或其子配置仍被请求/账务快照
-- （cost_snapshots/price_snapshots/settlement_recovery_jobs 等）引用，整条语句报 23503 全部回滚，
-- 上层降级为 conflict，提示改用停用——保住计费/审计链路。返回值为 models 行的受影响数（0 表示 model 不存在）。
WITH deleted_model_price_service_tiers AS (
    DELETE FROM model_price_service_tiers
    WHERE model_price_service_tiers.model_price_id IN (
        SELECT model_prices.id FROM model_prices WHERE model_prices.model_id = sqlc.arg(id)
    )
),
deleted_model_prices AS (
    DELETE FROM model_prices WHERE model_prices.model_id = sqlc.arg(id)
),
deleted_channel_price_service_tiers AS (
    DELETE FROM channel_price_service_tiers
    WHERE channel_price_service_tiers.channel_price_id IN (
        SELECT channel_prices.id FROM channel_prices WHERE channel_prices.model_id = sqlc.arg(id)
    )
),
deleted_channel_prices AS (
    DELETE FROM channel_prices WHERE channel_prices.model_id = sqlc.arg(id)
),
deleted_channel_models AS (
    DELETE FROM channel_models WHERE channel_models.model_id = sqlc.arg(id)
),
deleted_channel_cost_multiplier_overrides AS (
    DELETE FROM channel_cost_multipliers WHERE channel_cost_multipliers.model_id = sqlc.arg(id)
),
deleted_verification_items AS (
    DELETE FROM channel_model_verification_items WHERE channel_model_verification_items.model_id = sqlc.arg(id)
),
detached_probe_records AS (
    UPDATE provider_probe_records SET model_id = NULL WHERE provider_probe_records.model_id = sqlc.arg(id)
)
DELETE FROM models WHERE models.id = sqlc.arg(id);

-- name: GetModelCatalogState :one
-- GetModelCatalogState 读取单个模型的采纳目录追更状态（供模型详情 catalog 子对象）。
-- 未采纳模型无行返回（上层视为 catalog=null）。
SELECT
    l.canonical_id,
    l.adopted_fingerprint,
    l.reminder_muted,
    l.reminder_snooze_until,
    l.dismissed_fingerprint,
    mc.fingerprint AS catalog_fingerprint,
    (mc.removed_upstream_at IS NOT NULL)::boolean AS catalog_removed_upstream,
    (
        mc.fingerprint IS DISTINCT FROM l.adopted_fingerprint OR mc.removed_upstream_at IS NOT NULL
    )::boolean AS update_available,
    (
        (mc.fingerprint IS DISTINCT FROM l.adopted_fingerprint OR mc.removed_upstream_at IS NOT NULL)
        AND NOT l.reminder_muted
        AND l.dismissed_fingerprint IS DISTINCT FROM mc.fingerprint
        AND (l.reminder_snooze_until IS NULL OR now() >= l.reminder_snooze_until)
    )::boolean AS should_remind
FROM model_catalog_links l
JOIN model_catalog mc ON mc.canonical_id = l.canonical_id
WHERE l.model_id = sqlc.arg(model_id);

-- §3.4 模型商品控制台只读运维聚合。
-- 模型口径：request_records.requested_model_id(文本) = models.model_id。请求/性能为 request 粒度。
-- 成本按 cost_snapshots.model_id（数值 FK）归因；收入按 ledger_entries(debit) JOIN request 归因；仅 USD。
-- 基础供给候选：enabled 绑定 + 渠道 enabled + 可解析成本；不代表模型已定价可卖（§3.4.8）。

-- name: ModelsOpsTable :many
-- ModelsOpsTable 模型商品运维主表（分页）：静态元数据 + 渠道/基准价；请求/毛利等指标在详情页聚合。
SELECT
    m.id,
    m.model_id,
    m.display_name,
    m.owned_by,
    m.status,
    m.family,
    m.description,
    m.knowledge_cutoff,
    m.disabled_reason,
    m.created_at,
    m.max_output_tokens,
    m.context_window_tokens,
    (SELECT COUNT(*) FROM channel_models cm WHERE cm.model_id = m.id AND cm.status = 'enabled') AS bindings_total,
    (
        SELECT COUNT(*)
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id
        WHERE cm.model_id = m.id AND cm.status = 'enabled' AND c.status = 'enabled'
          -- DEC-031 成本可解析：有 channel_prices 绝对覆盖 OR （模型有生效基准价 AND 该渠道对本模型有生效价格倍率——含默认 model_id IS NULL）。
          AND (
              EXISTS (
                  SELECT 1 FROM channel_prices p
                  WHERE p.channel_id = cm.channel_id AND p.model_id = m.id AND p.status = 'enabled'
                    AND p.effective_from <= now() AND (p.effective_to IS NULL OR p.effective_to > now())
              )
              OR (
                  EXISTS (
                      SELECT 1 FROM model_prices mp
                      WHERE mp.model_id = m.id AND mp.status = 'enabled'
                        AND mp.effective_from <= now() AND (mp.effective_to IS NULL OR mp.effective_to > now())
                  )
                  AND EXISTS (
                      SELECT 1 FROM channel_cost_multipliers ccm
                      WHERE ccm.channel_id = cm.channel_id
                        AND (ccm.model_id = m.id OR ccm.model_id IS NULL)
                        AND ccm.status = 'enabled'
                        AND ccm.effective_from <= now() AND (ccm.effective_to IS NULL OR ccm.effective_to > now())
                  )
              )
          )
    ) AS bindings_available,
    (
        SELECT COUNT(*)
        FROM model_capabilities mc
        WHERE mc.model_id = m.id
          AND mc.support_level IN ('full', 'limited')
    ) AS capabilities_declared_count,
    -- has_price（DEC-031）：模型有生效基准价 AND 至少一条 enabled 绑定可解析成本（绝对覆盖或价格倍率）。
    -- 外层 EXISTS (SELECT 1 WHERE <复合布尔>) 让 sqlc 推断为非空 bool（裸复合布尔默认可空 pgtype.Bool）。
    EXISTS (SELECT 1 WHERE
        EXISTS (
            SELECT 1 FROM model_prices mp
            WHERE mp.model_id = m.id AND mp.status = 'enabled'
              AND mp.effective_from <= now() AND (mp.effective_to IS NULL OR mp.effective_to > now())
        )
        AND EXISTS (
            SELECT 1
            FROM channel_models cm
            JOIN channels c ON c.id = cm.channel_id
            WHERE cm.model_id = m.id AND cm.status = 'enabled' AND c.status = 'enabled'
              AND (
                  EXISTS (
                      SELECT 1 FROM channel_prices p
                      WHERE p.channel_id = cm.channel_id AND p.model_id = m.id AND p.status = 'enabled'
                        AND p.effective_from <= now() AND (p.effective_to IS NULL OR p.effective_to > now())
                  )
                  OR EXISTS (
                      SELECT 1 FROM channel_cost_multipliers ccm
                      WHERE ccm.channel_id = cm.channel_id
                        AND (ccm.model_id = m.id OR ccm.model_id IS NULL)
                        AND ccm.status = 'enabled'
                        AND ccm.effective_from <= now() AND (ccm.effective_to IS NULL OR ccm.effective_to > now())
                  )
              )
        )
    ) AS has_price,
    -- 基准售价（DEC-026 model_prices 当前生效行）；无基准时各列为 NULL（前端显示「缺价」）。
    -- CASE 包裹让 sqlc 把 base_currency 推断为可空（pgtype.Text）：LATERAL 无命中行时该列为 NULL，避免扫描进 string 报错。
    CASE WHEN base.currency IS NOT NULL THEN base.currency END AS base_currency,
    base.uncached_input_price AS base_uncached_input_price,
    base.cache_read_input_price AS base_cache_read_input_price,
    base.cache_creation_5m_input_price AS base_cache_creation_5m_input_price,
    base.cache_creation_1h_input_price AS base_cache_creation_1h_input_price,
    base.cache_creation_30m_input_price AS base_cache_creation_30m_input_price,
    base.output_price AS base_output_price,
    base.reasoning_output_price AS base_reasoning_output_price,
    -- 售价：绝对售价整组非空时整组覆盖，否则基准价 × sale_price_ratio。两套实体可共存，不能混算。
    base.sale_price_ratio AS base_sale_price_ratio,
    base.sale_uncached_input_price AS base_sale_uncached_input_price,
    base.sale_cache_read_input_price AS base_sale_cache_read_input_price,
    base.sale_cache_creation_5m_input_price AS base_sale_cache_creation_5m_input_price,
    base.sale_cache_creation_1h_input_price AS base_sale_cache_creation_1h_input_price,
    base.sale_cache_creation_30m_input_price AS base_sale_cache_creation_30m_input_price,
    base.sale_output_price AS base_sale_output_price,
    base.sale_reasoning_output_price AS base_sale_reasoning_output_price,
    -- 长上下文阶梯：LEFT JOIN 无基准价时 COALESCE 为 false，避免 sqlc 扫 NULL 进 bool。
    COALESCE(base.long_context_enabled, false) AS base_long_context_enabled,
    base.long_context_threshold AS base_long_context_threshold,
    base.long_context_input_multiplier AS base_long_context_input_multiplier,
    base.long_context_output_multiplier AS base_long_context_output_multiplier,
    COALESCE(base.fast_price_configured, false)::boolean AS base_fast_price_configured,
    base.fast_uncached_input_price AS base_fast_uncached_input_price,
    base.fast_cache_read_input_price AS base_fast_cache_read_input_price,
    base.fast_cache_creation_5m_input_price AS base_fast_cache_creation_5m_input_price,
    base.fast_cache_creation_1h_input_price AS base_fast_cache_creation_1h_input_price,
    base.fast_cache_creation_30m_input_price AS base_fast_cache_creation_30m_input_price,
    base.fast_output_price AS base_fast_output_price,
    base.fast_reasoning_output_price AS base_fast_reasoning_output_price,
    base.fast_sale_uncached_input_price AS base_fast_sale_uncached_input_price,
    base.fast_sale_cache_read_input_price AS base_fast_sale_cache_read_input_price,
    base.fast_sale_cache_creation_5m_input_price AS base_fast_sale_cache_creation_5m_input_price,
    base.fast_sale_cache_creation_1h_input_price AS base_fast_sale_cache_creation_1h_input_price,
    base.fast_sale_cache_creation_30m_input_price AS base_fast_sale_cache_creation_30m_input_price,
    base.fast_sale_output_price AS base_fast_sale_output_price,
    base.fast_sale_reasoning_output_price AS base_fast_sale_reasoning_output_price
FROM models m
LEFT JOIN LATERAL (
    -- base: 模型当前生效的基准售价（mirror FindRouteCandidates 的 base LATERAL）；LEFT 保证无基准价的模型仍出现在列表。
    SELECT mp.currency, mp.uncached_input_price, mp.cache_read_input_price,
        mp.cache_creation_5m_input_price, mp.cache_creation_1h_input_price,
        mp.cache_creation_30m_input_price,
        mp.output_price, mp.reasoning_output_price,
        mp.sale_price_ratio,
        mp.sale_uncached_input_price,
        mp.sale_cache_read_input_price,
        mp.sale_cache_creation_5m_input_price,
        mp.sale_cache_creation_1h_input_price,
        mp.sale_cache_creation_30m_input_price,
        mp.sale_output_price,
        mp.sale_reasoning_output_price,
        mp.long_context_enabled, mp.long_context_threshold,
        mp.long_context_input_multiplier, mp.long_context_output_multiplier,
        fast.id IS NOT NULL AS fast_price_configured,
        fast.uncached_input_price AS fast_uncached_input_price,
        fast.cache_read_input_price AS fast_cache_read_input_price,
        fast.cache_creation_5m_input_price AS fast_cache_creation_5m_input_price,
        fast.cache_creation_1h_input_price AS fast_cache_creation_1h_input_price,
        fast.cache_creation_30m_input_price AS fast_cache_creation_30m_input_price,
        fast.output_price AS fast_output_price,
        fast.reasoning_output_price AS fast_reasoning_output_price,
        fast.sale_uncached_input_price AS fast_sale_uncached_input_price,
        fast.sale_cache_read_input_price AS fast_sale_cache_read_input_price,
        fast.sale_cache_creation_5m_input_price AS fast_sale_cache_creation_5m_input_price,
        fast.sale_cache_creation_1h_input_price AS fast_sale_cache_creation_1h_input_price,
        fast.sale_cache_creation_30m_input_price AS fast_sale_cache_creation_30m_input_price,
        fast.sale_output_price AS fast_sale_output_price,
        fast.sale_reasoning_output_price AS fast_sale_reasoning_output_price
    FROM model_prices mp
    LEFT JOIN model_price_service_tiers fast
      ON fast.model_price_id = mp.id AND fast.service_tier = 'fast'
    WHERE mp.model_id = m.id
      AND mp.status = 'enabled'
      AND mp.effective_from <= now()
      AND (mp.effective_to IS NULL OR mp.effective_to > now())
    ORDER BY mp.effective_from DESC, mp.id DESC
    LIMIT 1
) base ON TRUE
WHERE (sqlc.narg('status')::text IS NULL OR m.status = sqlc.narg('status')::text)
  AND (sqlc.narg('search')::text IS NULL OR m.model_id ILIKE '%' || sqlc.narg('search')::text || '%' OR m.display_name ILIKE '%' || sqlc.narg('search')::text || '%')
ORDER BY
  CASE WHEN sqlc.narg('sort_field')::text = 'name' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN m.model_id END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'name' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN m.model_id END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'context' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN m.context_window_tokens END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'context' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN m.context_window_tokens END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'max_output' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN m.max_output_tokens END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'max_output' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN m.max_output_tokens END ASC NULLS LAST,
  -- bindings 排序与 bindings_available 口径一致（DEC-031：绝对覆盖 OR 基准价+价格倍率）。
  CASE WHEN sqlc.narg('sort_field')::text = 'bindings' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(*)
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id
        WHERE cm.model_id = m.id AND cm.status = 'enabled' AND c.status = 'enabled'
          AND (
              EXISTS (
                  SELECT 1 FROM channel_prices p
                  WHERE p.channel_id = cm.channel_id AND p.model_id = m.id AND p.status = 'enabled'
                    AND p.effective_from <= now() AND (p.effective_to IS NULL OR p.effective_to > now())
              )
              OR (
                  EXISTS (
                      SELECT 1 FROM model_prices mp
                      WHERE mp.model_id = m.id AND mp.status = 'enabled'
                        AND mp.effective_from <= now() AND (mp.effective_to IS NULL OR mp.effective_to > now())
                  )
                  AND EXISTS (
                      SELECT 1 FROM channel_cost_multipliers ccm
                      WHERE ccm.channel_id = cm.channel_id
                        AND (ccm.model_id = m.id OR ccm.model_id IS NULL)
                        AND ccm.status = 'enabled'
                        AND ccm.effective_from <= now() AND (ccm.effective_to IS NULL OR ccm.effective_to > now())
                  )
              )
          )
    ) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'bindings' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(*)
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id
        WHERE cm.model_id = m.id AND cm.status = 'enabled' AND c.status = 'enabled'
          AND (
              EXISTS (
                  SELECT 1 FROM channel_prices p
                  WHERE p.channel_id = cm.channel_id AND p.model_id = m.id AND p.status = 'enabled'
                    AND p.effective_from <= now() AND (p.effective_to IS NULL OR p.effective_to > now())
              )
              OR (
                  EXISTS (
                      SELECT 1 FROM model_prices mp
                      WHERE mp.model_id = m.id AND mp.status = 'enabled'
                        AND mp.effective_from <= now() AND (mp.effective_to IS NULL OR mp.effective_to > now())
                  )
                  AND EXISTS (
                      SELECT 1 FROM channel_cost_multipliers ccm
                      WHERE ccm.channel_id = cm.channel_id
                        AND (ccm.model_id = m.id OR ccm.model_id IS NULL)
                        AND ccm.status = 'enabled'
                        AND ccm.effective_from <= now() AND (ccm.effective_to IS NULL OR ccm.effective_to > now())
                  )
              )
          )
    ) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN m.created_at END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN m.created_at END ASC NULLS LAST,
  m.model_id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ModelsOpsTableCount :one
SELECT COUNT(*) AS total
FROM models m
WHERE (sqlc.narg('status')::text IS NULL OR m.status = sqlc.narg('status')::text)
  AND (sqlc.narg('search')::text IS NULL OR m.model_id ILIKE '%' || sqlc.narg('search')::text || '%' OR m.display_name ILIKE '%' || sqlc.narg('search')::text || '%');

-- name: ModelOpsDetail :one
-- ModelOpsDetail 单模型详情概览：请求/成功率/延迟/token/缓存/TPS/毛利（USD）。
SELECT
    COUNT(r.id) FILTER (WHERE r.status IN ('succeeded', 'failed')) AS request_total,
    COUNT(r.id) FILTER (WHERE r.status = 'succeeded') AS request_succeeded,
    COALESCE(AVG(CASE WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p50,
    COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY
        CASE WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p90,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY
        CASE WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p99,
    -- Gateway TTFT 只对流式请求有意义（口径同 overview.sql）：非流式没有首 token 时刻。
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN r.stream = TRUE AND r.gateway_first_token_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (r.gateway_first_token_at - r.started_at)) * 1000)::float8 END), 0)::float8 AS gateway_ttft_p95,
    COUNT(*) FILTER (WHERE r.stream = TRUE AND r.gateway_first_token_at IS NOT NULL) AS gateway_ttft_sample,
    COALESCE(SUM(u.output_tokens_total) FILTER (WHERE r.status = 'succeeded'), 0)::bigint AS output_tokens,
    COALESCE(SUM(u.uncached_input_tokens + u.cache_read_input_tokens + u.cache_creation_5m_input_tokens + u.cache_creation_1h_input_tokens + u.cache_creation_30m_input_tokens), 0)::bigint AS input_tokens,
    COALESCE(SUM(u.cache_read_input_tokens), 0)::bigint AS cache_read_tokens,
    COALESCE(SUM(u.cache_creation_5m_input_tokens + u.cache_creation_1h_input_tokens + u.cache_creation_30m_input_tokens), 0)::bigint AS cache_creation_tokens,
    COALESCE(SUM(
        CASE WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
             THEN EXTRACT(EPOCH FROM (r.completed_at - COALESCE(r.gateway_first_token_at, r.started_at))) END
    ), 0)::float8 AS generation_seconds,
    COALESCE((
        SELECT SUM(le.amount)
        FROM ledger_entries le
        JOIN request_records rr ON rr.id = le.request_record_id
        JOIN models m2 ON m2.model_id = rr.requested_model_id
        WHERE le.entry_type = 'debit' AND le.currency = 'USD' AND m2.id = sqlc.arg('model_id')
          AND (sqlc.narg('from_time')::timestamptz IS NULL OR le.created_at >= sqlc.narg('from_time')::timestamptz)
          AND (sqlc.narg('to_time')::timestamptz IS NULL OR le.created_at < sqlc.narg('to_time')::timestamptz)
    ), 0)::numeric AS revenue_usd,
    COALESCE((
        -- 成本读 USD 归一列（D8）：每笔按结算钉住的汇率折算，跨币种直接求和。
        SELECT SUM(cs.total_cost_amount_usd)
        FROM cost_snapshots cs
        WHERE cs.model_id = sqlc.arg('model_id')
          AND (sqlc.narg('from_time')::timestamptz IS NULL OR cs.created_at >= sqlc.narg('from_time')::timestamptz)
          AND (sqlc.narg('to_time')::timestamptz IS NULL OR cs.created_at < sqlc.narg('to_time')::timestamptz)
    ), 0)::numeric AS cost_usd,
    (SELECT COUNT(*) FROM channel_models cm WHERE cm.model_id = sqlc.arg('model_id') AND cm.status = 'enabled') AS bindings_total,
    -- bindings_available（DEC-031）：绝对覆盖 OR （基准价 + 价格倍率），与 ModelsOpsTable 口径一致。
    (
        SELECT COUNT(*)
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id
        WHERE cm.model_id = sqlc.arg('model_id') AND cm.status = 'enabled' AND c.status = 'enabled'
          AND (
              EXISTS (
                  SELECT 1 FROM channel_prices p
                  WHERE p.channel_id = cm.channel_id AND p.model_id = cm.model_id AND p.status = 'enabled'
                    AND p.effective_from <= now() AND (p.effective_to IS NULL OR p.effective_to > now())
              )
              OR (
                  EXISTS (
                      SELECT 1 FROM model_prices mp
                      WHERE mp.model_id = cm.model_id AND mp.status = 'enabled'
                        AND mp.effective_from <= now() AND (mp.effective_to IS NULL OR mp.effective_to > now())
                  )
                  AND EXISTS (
                      SELECT 1 FROM channel_cost_multipliers ccm
                      WHERE ccm.channel_id = cm.channel_id
                        AND (ccm.model_id = cm.model_id OR ccm.model_id IS NULL)
                        AND ccm.status = 'enabled'
                        AND ccm.effective_from <= now() AND (ccm.effective_to IS NULL OR ccm.effective_to > now())
                  )
              )
          )
    ) AS bindings_available,
    (SELECT status FROM models WHERE id = sqlc.arg('model_id')) AS model_status
FROM request_records r
JOIN models m ON m.model_id = r.requested_model_id
LEFT JOIN usage_records u ON u.request_record_id = r.id
WHERE m.id = sqlc.arg('model_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz);

-- name: ModelOpsChannels :many
-- ModelOpsChannels 单模型的承载渠道（绑定）+ attempt 指标（抽屉渠道 Tab，§3.4 最关键）。
SELECT
    c.id AS channel_id,
    c.name AS channel_name,
    c.status AS channel_status,
    cm.status AS binding_status,
    cm.upstream_model,
    c.priority,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream') AS attempt_total,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS attempt_succeeded,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    -- has_price（DEC-031）：该 (channel,model) 可解析成本——有 channel_prices 绝对覆盖 OR （模型有生效基准价 AND 该渠道对本模型有生效价格倍率）；与路由「可卖」对齐。
    -- 外层 EXISTS (SELECT 1 WHERE <复合布尔>) 让 sqlc 推断为非空 bool（裸复合布尔默认可空 pgtype.Bool）。
    EXISTS (SELECT 1 WHERE
        EXISTS (
            SELECT 1 FROM channel_prices has_cp
            WHERE has_cp.channel_id = c.id AND has_cp.model_id = sqlc.arg('model_id') AND has_cp.status = 'enabled'
              AND has_cp.effective_from <= now() AND (has_cp.effective_to IS NULL OR has_cp.effective_to > now())
        )
        OR (
            EXISTS (
                SELECT 1 FROM model_prices mp
                WHERE mp.model_id = sqlc.arg('model_id') AND mp.status = 'enabled'
                  AND mp.effective_from <= now() AND (mp.effective_to IS NULL OR mp.effective_to > now())
            )
            AND EXISTS (
                SELECT 1 FROM channel_cost_multipliers ccm
                WHERE ccm.channel_id = c.id
                  AND (ccm.model_id = sqlc.arg('model_id') OR ccm.model_id IS NULL)
                  AND ccm.status = 'enabled'
                  AND ccm.effective_from <= now() AND (ccm.effective_to IS NULL OR ccm.effective_to > now())
            )
        )
    ) AS has_price,
    -- 展示成本（DEC-031）：绝对覆盖优先，否则 基准价 × 价格倍率 × 充值倍率（缺充值倍率按 1）。
    price.uncached_input_cost AS input_cost,
    price.output_cost AS output_cost,
    -- Fast 档成本同口径。两侧都没有 Fast 价时为 NULL，表示该渠道对本模型不单独区分 Fast。
    price.fast_uncached_input_cost AS fast_input_cost,
    price.fast_output_cost AS fast_output_cost,
    -- 成本币种与 USD 折算（多货币，PLAN 5.5 / D2 修订）：绝对路径与倍率路径成本均按
    -- provider 结算币种记账，折算用与守卫同源的最新汇率——设价面板「看到的」与守卫「执行的」
    -- 永远同一口径；缺汇率时 usd 列为 NULL（前端提示先录汇率）。
    (COALESCE(price.abs_currency, p.currency))::text AS cost_currency,
    (CASE WHEN COALESCE(price.abs_currency, p.currency) = 'USD' THEN price.uncached_input_cost ELSE price.uncached_input_cost / fx.rate END)::numeric AS input_cost_usd,
    (CASE WHEN COALESCE(price.abs_currency, p.currency) = 'USD' THEN price.output_cost ELSE price.output_cost / fx.rate END)::numeric AS output_cost_usd,
    (CASE WHEN COALESCE(price.abs_currency, p.currency) = 'USD' THEN price.fast_uncached_input_cost ELSE price.fast_uncached_input_cost / fx.rate END)::numeric AS fast_input_cost_usd,
    (CASE WHEN COALESCE(price.abs_currency, p.currency) = 'USD' THEN price.fast_output_cost ELSE price.fast_output_cost / fx.rate END)::numeric AS fast_output_cost_usd,
    fx.rate AS cost_fx_rate,
    fx.rate_date AS cost_fx_rate_date
FROM channel_models cm
JOIN channels c ON c.id = cm.channel_id
JOIN providers p ON p.id = c.provider_id
LEFT JOIN LATERAL (
    SELECT
        -- 仅透出绝对路径币种；成本币种合成（COALESCE 到 provider 币种）放外层——
        -- sqlc 解析不了嵌套 LATERAL 对外层 providers 别名的引用。
        abs.currency AS abs_currency,
        COALESCE(
            abs.uncached_input_cost,
            CASE
                WHEN base.uncached_input_price IS NOT NULL AND mult.multiplier IS NOT NULL
                THEN base.uncached_input_price * mult.multiplier * COALESCE(recharge.rate, 1::numeric)
            END
        ) AS uncached_input_cost,
        COALESCE(
            abs.output_cost,
            CASE
                WHEN base.output_price IS NOT NULL AND mult.multiplier IS NOT NULL
                THEN base.output_price * mult.multiplier * COALESCE(recharge.rate, 1::numeric)
            END
        ) AS output_cost,
        -- Fast 成本：渠道 Fast 绝对覆盖优先，否则 Fast 基准价 × 同一组倍率。
        -- 两侧都缺 Fast 价时为 NULL——结算会回落 Standard，不该编一个 Fast 成本出来。
        COALESCE(
            abs.fast_uncached_input_cost,
            CASE
                WHEN base.fast_uncached_input_price IS NOT NULL AND mult.multiplier IS NOT NULL
                THEN base.fast_uncached_input_price * mult.multiplier * COALESCE(recharge.rate, 1::numeric)
            END
        ) AS fast_uncached_input_cost,
        COALESCE(
            abs.fast_output_cost,
            CASE
                WHEN base.fast_output_price IS NOT NULL AND mult.multiplier IS NOT NULL
                THEN base.fast_output_price * mult.multiplier * COALESCE(recharge.rate, 1::numeric)
            END
        ) AS fast_output_cost
    FROM (SELECT 1) AS _
    -- abs 别名用 cprice（channel_prices），p 留给外层 providers（倍率路径成本币种 = provider 币种）。
    LEFT JOIN LATERAL (
        SELECT cprice.currency, cprice.uncached_input_cost, cprice.output_cost,
            fast.uncached_input_cost AS fast_uncached_input_cost,
            fast.output_cost AS fast_output_cost
        FROM channel_prices cprice
        LEFT JOIN channel_price_service_tiers fast
          ON fast.channel_price_id = cprice.id AND fast.service_tier = 'fast'
        WHERE cprice.channel_id = c.id
          AND cprice.model_id = sqlc.arg('model_id')
          AND cprice.status = 'enabled'
          AND cprice.effective_from <= now()
          AND (cprice.effective_to IS NULL OR cprice.effective_to > now())
        ORDER BY cprice.effective_from DESC, cprice.id DESC
        LIMIT 1
    ) abs ON TRUE
    LEFT JOIN LATERAL (
        SELECT mp.uncached_input_price, mp.output_price,
            fast.uncached_input_price AS fast_uncached_input_price,
            fast.output_price AS fast_output_price
        FROM model_prices mp
        LEFT JOIN model_price_service_tiers fast
          ON fast.model_price_id = mp.id AND fast.service_tier = 'fast'
        WHERE mp.model_id = sqlc.arg('model_id')
          AND mp.status = 'enabled'
          AND mp.effective_from <= now()
          AND (mp.effective_to IS NULL OR mp.effective_to > now())
        ORDER BY mp.effective_from DESC, mp.id DESC
        LIMIT 1
    ) base ON TRUE
    LEFT JOIN LATERAL (
        SELECT ccm.multiplier
        FROM channel_cost_multipliers ccm
        WHERE ccm.channel_id = c.id
          AND (ccm.model_id = sqlc.arg('model_id') OR ccm.model_id IS NULL)
          AND ccm.status = 'enabled'
          AND ccm.effective_from <= now()
          AND (ccm.effective_to IS NULL OR ccm.effective_to > now())
        ORDER BY (ccm.model_id IS NULL) ASC, ccm.effective_from DESC, ccm.id DESC
        LIMIT 1
    ) mult ON TRUE
    LEFT JOIN LATERAL (
        -- 充值汇率归属服务商（provider_recharge_rates）；嵌套 LATERAL 不能引用外层 providers 别名，
        -- 用 c.provider_id 定位。
        SELECT prr.rate
        FROM provider_recharge_rates prr
        WHERE prr.provider_id = c.provider_id
          AND prr.status = 'enabled'
          AND prr.effective_from <= now()
          AND (prr.effective_to IS NULL OR prr.effective_to > now())
        ORDER BY prr.effective_from DESC, prr.id DESC
        LIMIT 1
    ) recharge ON TRUE
) price ON TRUE
LEFT JOIN LATERAL (
    SELECT er.rate, er.rate_date
    FROM exchange_rates er
    WHERE er.base_currency = 'USD' AND er.quote_currency = COALESCE(price.abs_currency, p.currency)
    ORDER BY er.rate_date DESC, er.fetched_at DESC
    LIMIT 1
) fx ON COALESCE(price.abs_currency, p.currency) <> 'USD'
LEFT JOIN request_attempts a
    ON a.channel_id = cm.channel_id
    AND a.upstream_model = cm.upstream_model
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
WHERE cm.model_id = sqlc.arg('model_id')
GROUP BY c.id, c.name, c.status, cm.status, cm.upstream_model, c.priority,
    price.uncached_input_cost, price.output_cost,
    price.fast_uncached_input_cost, price.fast_output_cost,
    price.abs_currency, p.currency, fx.rate, fx.rate_date
ORDER BY attempt_total DESC, c.priority, c.id;

-- name: ModelOpsPerformanceTimeseries :many
-- ModelOpsPerformanceTimeseries 单模型分桶时序：请求量、P95 延迟与收入/成本。
--
-- 收入与成本各按自己的时间戳分桶，与 ModelOpsDetail 的过滤口径逐字一致，
-- 这样时序求和等于概览数值；若不一致，页头和图表会互相打脸。
-- 三者的桶取并集：结算可能落在请求所在桶的下一桶，用 UNION 保证那部分金额不被丢掉。
WITH requests AS (
    SELECT
        date_trunc(sqlc.arg('unit')::text, r.created_at)::timestamptz AS bucket,
        COUNT(*) FILTER (WHERE r.status IN ('succeeded', 'failed')) AS request_total,
        COUNT(*) FILTER (WHERE r.status = 'succeeded') AS request_succeeded,
        COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
            CASE WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
                 THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95
    FROM request_records r
    JOIN models m ON m.model_id = r.requested_model_id
    WHERE m.id = sqlc.arg('model_id')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
    GROUP BY 1
), revenue AS (
    SELECT
        date_trunc(sqlc.arg('unit')::text, le.created_at)::timestamptz AS bucket,
        SUM(le.amount) AS revenue_usd
    FROM ledger_entries le
    JOIN request_records rr ON rr.id = le.request_record_id
    JOIN models m2 ON m2.model_id = rr.requested_model_id
    WHERE le.entry_type = 'debit' AND le.currency = 'USD' AND m2.id = sqlc.arg('model_id')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR le.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR le.created_at < sqlc.narg('to_time')::timestamptz)
    GROUP BY 1
), costs AS (
    SELECT
        date_trunc(sqlc.arg('unit')::text, cs.created_at)::timestamptz AS bucket,
        -- 成本读 USD 归一列（D8）。
        SUM(cs.total_cost_amount_usd) AS cost_usd
    FROM cost_snapshots cs
    WHERE cs.model_id = sqlc.arg('model_id')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR cs.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR cs.created_at < sqlc.narg('to_time')::timestamptz)
    GROUP BY 1
), all_buckets AS (
    SELECT bucket FROM requests
    UNION
    SELECT bucket FROM revenue
    UNION
    SELECT bucket FROM costs
)
SELECT
    b.bucket,
    COALESCE(rq.request_total, 0)::bigint AS request_total,
    COALESCE(rq.request_succeeded, 0)::bigint AS request_succeeded,
    COALESCE(rq.latency_p95, 0)::float8 AS latency_p95,
    COALESCE(rv.revenue_usd, 0)::numeric AS revenue_usd,
    COALESCE(ct.cost_usd, 0)::numeric AS cost_usd
FROM all_buckets b
LEFT JOIN requests rq ON rq.bucket = b.bucket
LEFT JOIN revenue rv ON rv.bucket = b.bucket
LEFT JOIN costs ct ON ct.bucket = b.bucket
ORDER BY b.bucket;

-- name: ModelOpsErrors :many
-- ModelOpsErrors 单模型失败请求按 error_code 聚合（排障分区）。
-- 与 ModelOpsRequests 互补：明细回答「这一笔怎么了」，聚合回答「主要错在哪」。
-- 口径同其余模型 ops 查询：request 粒度，失败即 status = 'failed'。
-- 错误码为空归一到 'unknown'，否则每条失败自成一组，聚合表退化成明细表。
-- 错误码种类有限，不分页；占比由调用方按总数换算。
SELECT
    COALESCE(NULLIF(r.error_code, ''), 'unknown')::text AS error_code,
    COUNT(*) AS occurrences,
    MAX(r.created_at)::timestamptz AS last_seen_at,
    ((array_agg(r.request_id ORDER BY r.created_at DESC))[1])::text AS sample_request_id,
    COUNT(DISTINCT r.final_channel_id) AS channels_touched
FROM request_records r
JOIN models m ON m.model_id = r.requested_model_id
WHERE m.id = sqlc.arg('model_id')
  AND r.status = 'failed'
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY 1
ORDER BY occurrences DESC, last_seen_at DESC;

-- name: ModelOpsRequests :many
-- ModelOpsRequests 单模型最近请求（抽屉请求 Tab，分页）。
SELECT
    r.request_id,
    r.created_at,
    r.status,
    r.error_code,
    r.final_channel_id,
    CASE WHEN r.completed_at IS NOT NULL THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8 END AS latency_ms
FROM request_records r
JOIN models m ON m.model_id = r.requested_model_id
WHERE m.id = sqlc.arg('model_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY r.created_at DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ModelOpsRequestsCount :one
SELECT COUNT(*) AS total
FROM request_records r
JOIN models m ON m.model_id = r.requested_model_id
WHERE m.id = sqlc.arg('model_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz);
