-- name: ModelExistsByID :one
-- ModelExistsByID 判断指定对外模型 ID 是否存在且启用。
SELECT EXISTS (
    SELECT 1
    FROM models m
    WHERE m.model_id = sqlc.arg(requested_model_id)
    AND m.status = 'enabled'
) AS exists;

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
-- 级联清理 model 自身的配置子表：channel_prices（渠道-模型价）、channel_models（模型绑定）；
-- model_capabilities、project_model_policies、model_catalog_links
-- 由 ON DELETE CASCADE 自动清理，无需在此显式删除。
-- 外键均为默认 NO ACTION（约束在语句末校验），故 CTE 删子表 + 删主体在单条语句内原子完成：
-- 子配置先删除，语句末 models 的删除不会留下悬挂引用。若 model 或其子配置仍被请求/账务快照
-- （cost_snapshots/price_snapshots/settlement_recovery_jobs 等）引用，整条语句报 23503 全部回滚，
-- 上层降级为 conflict，提示改用停用。返回值为 models 行的受影响数（0 表示 model 不存在）。
WITH deleted_channel_prices AS (
    DELETE FROM channel_prices WHERE channel_prices.model_id = sqlc.arg(id)
),
deleted_channel_models AS (
    DELETE FROM channel_models WHERE channel_models.model_id = sqlc.arg(id)
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

-- name: ListAvailableModelsForProject :many
-- ListAvailableModelsForProject 列出指定项目当前可见且可路由的模型，并附带该模型已声明的
-- cap-tags（能力架构 Layer 2，support_level<>'unsupported' 的 capability_key 去重升序）。
-- cap-tags 取模型级声明，不下钻到 channel override（不向客户暴露 channel 维度收紧）。
-- 未声明任何能力的模型 capability_keys 为空数组（unprovisioned）。
WITH project_scope AS (
    SELECT sqlc.arg(project_id)::BIGINT AS project_id
),
project_policy_mode AS (
    SELECT EXISTS (
        SELECT 1
        FROM project_model_policies pmp
        JOIN project_scope ps ON ps.project_id = pmp.project_id
        WHERE pmp.visibility = 'allowed'
    ) AS has_allow_list
)
SELECT
    m.id,
    m.model_id,
    m.display_name,
    m.owned_by,
    COALESCE(
        array_agg(DISTINCT mc.capability_key)
            FILTER (WHERE mc.capability_key IS NOT NULL AND mc.support_level <> 'unsupported'),
        '{}'
    )::text[] AS capability_keys
FROM models m
JOIN channel_models cm ON cm.model_id = m.id
JOIN channels c ON c.id = cm.channel_id
JOIN providers p ON p.id = c.provider_id
LEFT JOIN model_capabilities mc ON mc.model_id = m.id
JOIN project_scope ps ON ps.project_id > 0
WHERE m.status = 'enabled'
    AND cm.status = 'enabled'
    AND c.status = 'enabled'
    AND p.status = 'enabled'
    AND NOT EXISTS (
        SELECT 1
        FROM project_model_policies denied
        JOIN project_scope ps ON ps.project_id = denied.project_id
        WHERE denied.model_id = m.id
            AND denied.visibility = 'denied'
    )
    AND (
        NOT (SELECT has_allow_list FROM project_policy_mode)
        OR EXISTS (
            SELECT 1
            FROM project_model_policies allowed
            JOIN project_scope ps ON ps.project_id = allowed.project_id
            WHERE allowed.model_id = m.id
                AND allowed.visibility = 'allowed'
        )
    )
GROUP BY m.id, m.model_id, m.display_name, m.owned_by
ORDER BY m.model_id ASC;
