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
-- CreateModelPrice 创建一条模型基准售价（DEC-026）。客户最终售价 = 本基准价 × 线路倍率。
-- 启用窗口重叠由 ex_model_prices_enabled_window 保证，违反报 23P01。
INSERT INTO model_prices (
    model_id,
    currency,
    pricing_unit,
    uncached_input_price,
    cache_read_input_price,
    cache_write_5m_input_price,
    cache_write_1h_input_price,
    cache_write_30m_input_price,
    output_price,
    reasoning_output_price,
    status,
    effective_from,
    effective_to,
    long_context_enabled,
    long_context_threshold,
    long_context_input_multiplier,
    long_context_output_multiplier
)
VALUES (
    sqlc.arg(model_id),
    sqlc.arg(currency),
    sqlc.arg(pricing_unit),
    sqlc.arg(uncached_input_price),
    sqlc.arg(cache_read_input_price),
    sqlc.arg(cache_write_5m_input_price),
    sqlc.arg(cache_write_1h_input_price),
    sqlc.arg(cache_write_30m_input_price),
    sqlc.arg(output_price),
    sqlc.arg(reasoning_output_price),
    sqlc.arg(status),
    sqlc.arg(effective_from),
    sqlc.arg(effective_to),
    sqlc.arg(long_context_enabled),
    sqlc.arg(long_context_threshold),
    sqlc.arg(long_context_input_multiplier),
    sqlc.arg(long_context_output_multiplier)
)
RETURNING *;

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
    mp.cache_write_5m_input_price,
    mp.cache_write_1h_input_price,
    mp.cache_write_30m_input_price,
    mp.output_price,
    mp.reasoning_output_price,
    mp.status,
    mp.effective_from,
    mp.effective_to,
    mp.created_at,
    mp.updated_at,
    mp.long_context_enabled,
    mp.long_context_threshold,
    mp.long_context_input_multiplier,
    mp.long_context_output_multiplier,
    m.model_id AS model_external_id,
    m.display_name AS model_display_name
FROM model_prices mp
JOIN models m ON m.id = mp.model_id
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
        m.max_output_tokens,
        m.context_window_tokens,
        m.input_price_usd_per_million_tokens,
        m.output_price_usd_per_million_tokens,
        m.release_date,
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
-- model_id 全局唯一由 DB 唯一约束保证；元数据（上下文/价格基线/发布日期）可选填，纯展示不参与计费（阶段 14 Q5）。
INSERT INTO models (
    model_id,
    display_name,
    owned_by,
    status,
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
    status,
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
-- 元数据（上下文/价格基线/发布日期）可编辑，也可被「从目录刷新」覆盖；纯展示不参与计费。
UPDATE models
SET display_name = sqlc.arg(display_name),
    owned_by = sqlc.arg(owned_by),
    status = sqlc.arg(status),
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
-- 级联清理 model 自身的配置子表：model_prices（基准售价）、channel_prices（渠道-模型成本价）、
-- channel_models（模型绑定）、channel_cost_multipliers 的逐模型覆盖行（model_id=本 model，DEC-027）；
-- model_capabilities、user_model_policies、model_catalog_links 由 ON DELETE CASCADE 自动清理，无需在此显式删除。
-- 这些价格/绑定/倍率覆盖表都是追加式配置（无删除接口，只能停用），若不在此一并清理，任何配过价/倍率覆盖的
-- model 都会被自身配置行永久挡住删除（均无请求/账务语义，属纯配置）。
-- 注意 channel_cost_multipliers.model_id 可空：NULL=渠道默认倍率（对全部模型生效，不随单个 model 删除），
-- 非空=对本 model 的覆盖；WHERE model_id = id 只删覆盖行，渠道默认行保留不动。
-- 外键均为默认 NO ACTION（约束在语句末校验），故 CTE 删子表 + 删主体在单条语句内原子完成：
-- 子配置先删除，语句末 models 的删除不会留下悬挂引用。若 model 或其子配置仍被请求/账务快照
-- （cost_snapshots/price_snapshots/settlement_recovery_jobs 等）引用，整条语句报 23503 全部回滚，
-- 上层降级为 conflict，提示改用停用——保住计费/审计链路。返回值为 models 行的受影响数（0 表示 model 不存在）。
WITH deleted_model_prices AS (
    DELETE FROM model_prices WHERE model_prices.model_id = sqlc.arg(id)
),
deleted_channel_prices AS (
    DELETE FROM channel_prices WHERE channel_prices.model_id = sqlc.arg(id)
),
deleted_channel_models AS (
    DELETE FROM channel_models WHERE channel_models.model_id = sqlc.arg(id)
),
deleted_channel_cost_multiplier_overrides AS (
    DELETE FROM channel_cost_multipliers WHERE channel_cost_multipliers.model_id = sqlc.arg(id)
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
-- 可售/可用渠道：enabled 绑定 + 渠道 enabled + 有 enabled 价格（§3.4.8）。

-- name: ModelsOpsTable :many
-- ModelsOpsTable 模型商品运维主表（分页）：静态元数据 + 渠道/基准价；请求/毛利等指标在详情页聚合。
SELECT
    m.id,
    m.model_id,
    m.display_name,
    m.owned_by,
    m.status,
    m.created_at,
    m.max_output_tokens,
    m.context_window_tokens,
    (SELECT COUNT(*) FROM channel_models cm WHERE cm.model_id = m.id AND cm.status = 'enabled') AS bindings_total,
    (
        SELECT COUNT(*)
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id
        WHERE cm.model_id = m.id AND cm.status = 'enabled' AND c.status = 'enabled'
          -- DEC-031 可售对齐路由：有 channel_prices 绝对覆盖 OR （模型有生效基准价 AND 该渠道对本模型有生效价格倍率——含默认 model_id IS NULL）。
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
    -- has_price（DEC-031）：模型有生效基准价 AND 至少一条 enabled 绑定可解析成本（绝对覆盖 或 价格倍率）；与路由「可卖」对齐，消灭假「不可售」。
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
    -- 基准售价（DEC-026 model_prices 当前生效行）：客户售价 = 基准 × 线路倍率；无基准时各列为 NULL（前端显示「缺价」）。
    -- CASE 包裹让 sqlc 把 base_currency 推断为可空（pgtype.Text）：LATERAL 无命中行时该列为 NULL，避免扫描进 string 报错。
    CASE WHEN base.currency IS NOT NULL THEN base.currency END AS base_currency,
    base.uncached_input_price AS base_uncached_input_price,
    base.cache_read_input_price AS base_cache_read_input_price,
    base.cache_write_5m_input_price AS base_cache_write_5m_input_price,
    base.cache_write_1h_input_price AS base_cache_write_1h_input_price,
    base.cache_write_30m_input_price AS base_cache_write_30m_input_price,
    base.output_price AS base_output_price,
    base.reasoning_output_price AS base_reasoning_output_price
FROM models m
LEFT JOIN LATERAL (
    -- base: 模型当前生效的基准售价（mirror FindRouteCandidates 的 base LATERAL）；LEFT 保证无基准价的模型仍出现在列表。
    SELECT mp.currency, mp.uncached_input_price, mp.cache_read_input_price,
        mp.cache_write_5m_input_price, mp.cache_write_1h_input_price,
        mp.cache_write_30m_input_price,
        mp.output_price, mp.reasoning_output_price
    FROM model_prices mp
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
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    COALESCE(SUM(u.output_tokens_total) FILTER (WHERE r.status = 'succeeded'), 0)::bigint AS output_tokens,
    COALESCE(SUM(u.uncached_input_tokens + u.cache_read_input_tokens + u.cache_write_5m_input_tokens + u.cache_write_1h_input_tokens + u.cache_write_30m_input_tokens), 0)::bigint AS input_tokens,
    COALESCE(SUM(u.cache_read_input_tokens), 0)::bigint AS cache_read_tokens,
    COALESCE(SUM(u.cache_write_5m_input_tokens + u.cache_write_1h_input_tokens + u.cache_write_30m_input_tokens), 0)::bigint AS cache_write_tokens,
    COALESCE(SUM(
        CASE WHEN r.status = 'succeeded' AND r.completed_at IS NOT NULL
             THEN EXTRACT(EPOCH FROM (r.completed_at - COALESCE(r.response_started_at, r.started_at))) END
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
        SELECT SUM(cs.total_cost_amount)
        FROM cost_snapshots cs
        WHERE cs.model_id = sqlc.arg('model_id') AND cs.currency = 'USD'
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
            SELECT 1 FROM channel_prices p
            WHERE p.channel_id = c.id AND p.model_id = sqlc.arg('model_id') AND p.status = 'enabled'
              AND p.effective_from <= now() AND (p.effective_to IS NULL OR p.effective_to > now())
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
    price.output_cost AS output_cost
FROM channel_models cm
JOIN channels c ON c.id = cm.channel_id
LEFT JOIN LATERAL (
    SELECT
        COALESCE(
            abs.uncached_input_cost,
            CASE
                WHEN base.uncached_input_price IS NOT NULL AND mult.multiplier IS NOT NULL
                THEN base.uncached_input_price * mult.multiplier * COALESCE(recharge.factor, 1::numeric)
            END
        ) AS uncached_input_cost,
        COALESCE(
            abs.output_cost,
            CASE
                WHEN base.output_price IS NOT NULL AND mult.multiplier IS NOT NULL
                THEN base.output_price * mult.multiplier * COALESCE(recharge.factor, 1::numeric)
            END
        ) AS output_cost
    FROM (SELECT 1) AS _
    LEFT JOIN LATERAL (
        SELECT p.uncached_input_cost, p.output_cost
        FROM channel_prices p
        WHERE p.channel_id = c.id
          AND p.model_id = sqlc.arg('model_id')
          AND p.status = 'enabled'
          AND p.effective_from <= now()
          AND (p.effective_to IS NULL OR p.effective_to > now())
        ORDER BY p.effective_from DESC, p.id DESC
        LIMIT 1
    ) abs ON TRUE
    LEFT JOIN LATERAL (
        SELECT mp.uncached_input_price, mp.output_price
        FROM model_prices mp
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
        SELECT crf.factor
        FROM channel_recharge_factors crf
        WHERE crf.channel_id = c.id
          AND crf.status = 'enabled'
          AND crf.effective_from <= now()
          AND (crf.effective_to IS NULL OR crf.effective_to > now())
        ORDER BY crf.effective_from DESC, crf.id DESC
        LIMIT 1
    ) recharge ON TRUE
) price ON TRUE
LEFT JOIN request_attempts a
    ON a.channel_id = cm.channel_id
    AND a.upstream_model = cm.upstream_model
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
WHERE cm.model_id = sqlc.arg('model_id')
GROUP BY c.id, c.name, c.status, cm.status, cm.upstream_model, c.priority,
    price.uncached_input_cost, price.output_cost
ORDER BY attempt_total DESC, c.priority, c.id;

-- name: ModelOpsPerformanceTimeseries :many
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
GROUP BY bucket
ORDER BY bucket;

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
