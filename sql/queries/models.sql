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
-- ListModelsPage 按状态/关键字过滤后分页列出 model，供 admin 管理台展示；status、q 为 NULL 时不过滤。
SELECT *
FROM models
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR model_id ILIKE '%' || sqlc.narg('q')::text || '%'
    OR display_name ILIKE '%' || sqlc.narg('q')::text || '%'
    OR owned_by ILIKE '%' || sqlc.narg('q')::text || '%'
  )
ORDER BY model_id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountModels :one
-- CountModels 返回与 ListModelsPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM models
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR model_id ILIKE '%' || sqlc.narg('q')::text || '%'
    OR display_name ILIKE '%' || sqlc.narg('q')::text || '%'
    OR owned_by ILIKE '%' || sqlc.narg('q')::text || '%'
  );

-- name: CreateModel :one
-- CreateModel 创建 admin 手工模型；source 固定 manual（models.dev 同步永不覆盖 manual 行）。
-- model_id 全局唯一由 DB 唯一约束保证；价格基线/canonical_id/release_date 等同步元数据不在此设置。
INSERT INTO models (
    model_id,
    display_name,
    owned_by,
    status,
    lab,
    max_output_tokens,
    source
)
VALUES (
    sqlc.arg(model_id),
    sqlc.arg(display_name),
    sqlc.arg(owned_by),
    sqlc.arg(status),
    sqlc.narg(lab),
    sqlc.narg(max_output_tokens),
    'manual'
)
RETURNING *;

-- name: UpdateModel :one
-- UpdateModel 更新 model 的展示元数据与启停状态；model_id 作为对外稳定标识不可变，
-- source/canonical_id/价格基线不在此修改。
UPDATE models
SET display_name = sqlc.arg(display_name),
    owned_by = sqlc.arg(owned_by),
    status = sqlc.arg(status),
    lab = sqlc.narg(lab),
    max_output_tokens = sqlc.narg(max_output_tokens),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: LookupModelByModelID :one
-- LookupModelByModelID 按对外模型 ID 读取模型完整元数据（含能力架构 Layer 1 列）。
SELECT *
FROM models
WHERE model_id = sqlc.arg(model_id);

-- name: ListCanonicalModels :many
-- ListCanonicalModels 列出全部带 canonical_id 的模型，供 models.dev 同步做合并与缺失检测。
SELECT id, model_id, canonical_id, source, status, removed_upstream_at
FROM models
WHERE canonical_id IS NOT NULL
ORDER BY canonical_id ASC;

-- name: UpsertSeedModelByCanonicalID :one
-- UpsertSeedModelByCanonicalID 按 canonical_id upsert models.dev 种子模型。
-- 新模型默认 disabled、source=seed_models_dev；仅 source=seed_models_dev 的已存在行才覆盖元数据，
-- source=manual/import 行永不被覆盖（WHERE 守护，竞态下也安全）；覆盖时清除上游删除标记。
INSERT INTO models (
    model_id,
    display_name,
    owned_by,
    status,
    canonical_id,
    lab,
    context_window_tokens,
    max_output_tokens,
    input_price_usd_per_million_tokens,
    output_price_usd_per_million_tokens,
    release_date,
    source,
    removed_upstream_at
)
VALUES (
    sqlc.arg(model_id),
    sqlc.arg(display_name),
    sqlc.arg(owned_by),
    'disabled',
    sqlc.arg(canonical_id),
    sqlc.narg(lab),
    sqlc.narg(context_window_tokens),
    sqlc.narg(max_output_tokens),
    sqlc.narg(input_price_usd_per_million_tokens),
    sqlc.narg(output_price_usd_per_million_tokens),
    sqlc.narg(release_date),
    'seed_models_dev',
    NULL
)
ON CONFLICT (canonical_id) DO UPDATE
SET display_name = EXCLUDED.display_name,
    lab = EXCLUDED.lab,
    context_window_tokens = EXCLUDED.context_window_tokens,
    max_output_tokens = EXCLUDED.max_output_tokens,
    input_price_usd_per_million_tokens = EXCLUDED.input_price_usd_per_million_tokens,
    output_price_usd_per_million_tokens = EXCLUDED.output_price_usd_per_million_tokens,
    release_date = EXCLUDED.release_date,
    removed_upstream_at = NULL,
    updated_at = now()
WHERE models.source = 'seed_models_dev'
RETURNING *;

-- name: MarkSeedModelRemovedUpstream :one
-- MarkSeedModelRemovedUpstream 把 models.dev 已删除的种子模型标记为 disabled + removed_upstream_at；
-- 仅作用于 source=seed_models_dev 且尚未标记的行，manual 行与已标记行不动（不自动删除本地数据）。
UPDATE models
SET status = 'disabled',
    removed_upstream_at = now(),
    updated_at = now()
WHERE canonical_id = sqlc.arg(canonical_id)
    AND source = 'seed_models_dev'
    AND removed_upstream_at IS NULL
RETURNING *;

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
