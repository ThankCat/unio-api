-- name: UpsertModelCatalogEntry :one
-- UpsertModelCatalogEntry 按 canonical_id 全量 upsert 目录条目；覆盖时刷新 fingerprint/synced_at 并清除下架标记。
INSERT INTO model_catalog (
    canonical_id,
    lab,
    family,
    display_name,
    description,
    knowledge_cutoff,
    context_window_tokens,
    input_limit_tokens,
    max_output_tokens,
    input_price_usd_per_million_tokens,
    output_price_usd_per_million_tokens,
    cache_read_price_usd_per_million_tokens,
    cache_write_price_usd_per_million_tokens,
    open_weights,
    modalities_input,
    modalities_output,
    release_date,
    last_updated,
    fingerprint,
    removed_upstream_at,
    synced_at
)
VALUES (
    sqlc.arg(canonical_id),
    sqlc.arg(lab),
    sqlc.arg(family),
    sqlc.arg(display_name),
    sqlc.arg(description),
    sqlc.arg(knowledge_cutoff),
    sqlc.narg(context_window_tokens),
    sqlc.narg(input_limit_tokens),
    sqlc.narg(max_output_tokens),
    sqlc.narg(input_price_usd_per_million_tokens),
    sqlc.narg(output_price_usd_per_million_tokens),
    sqlc.narg(cache_read_price_usd_per_million_tokens),
    sqlc.narg(cache_write_price_usd_per_million_tokens),
    sqlc.narg(open_weights),
    sqlc.arg(modalities_input),
    sqlc.arg(modalities_output),
    sqlc.narg(release_date),
    sqlc.narg(last_updated),
    sqlc.arg(fingerprint),
    NULL,
    now()
)
ON CONFLICT (canonical_id) DO UPDATE
SET lab = EXCLUDED.lab,
    family = EXCLUDED.family,
    display_name = EXCLUDED.display_name,
    description = EXCLUDED.description,
    knowledge_cutoff = EXCLUDED.knowledge_cutoff,
    context_window_tokens = EXCLUDED.context_window_tokens,
    input_limit_tokens = EXCLUDED.input_limit_tokens,
    max_output_tokens = EXCLUDED.max_output_tokens,
    input_price_usd_per_million_tokens = EXCLUDED.input_price_usd_per_million_tokens,
    output_price_usd_per_million_tokens = EXCLUDED.output_price_usd_per_million_tokens,
    cache_read_price_usd_per_million_tokens = EXCLUDED.cache_read_price_usd_per_million_tokens,
    cache_write_price_usd_per_million_tokens = EXCLUDED.cache_write_price_usd_per_million_tokens,
    open_weights = EXCLUDED.open_weights,
    modalities_input = EXCLUDED.modalities_input,
    modalities_output = EXCLUDED.modalities_output,
    release_date = EXCLUDED.release_date,
    last_updated = EXCLUDED.last_updated,
    fingerprint = EXCLUDED.fingerprint,
    removed_upstream_at = NULL,
    synced_at = now(),
    updated_at = now()
RETURNING *;

-- name: DeleteModelCatalogCapabilities :exec
-- DeleteModelCatalogCapabilities 清空某目录条目的全部能力提示（同步刷新时先删后插）。
DELETE FROM model_catalog_capabilities
WHERE canonical_id = sqlc.arg(canonical_id);

-- name: InsertModelCatalogCapability :exec
-- InsertModelCatalogCapability 写入一条目录能力提示（同步刷新时配合 DeleteModelCatalogCapabilities）。
INSERT INTO model_catalog_capabilities (
    canonical_id,
    capability_key,
    support_level,
    limits
)
VALUES (
    sqlc.arg(canonical_id),
    sqlc.arg(capability_key),
    sqlc.arg(support_level),
    sqlc.arg(limits)
)
ON CONFLICT (canonical_id, capability_key) DO UPDATE
SET support_level = excluded.support_level,
    limits = excluded.limits;

-- name: MarkModelCatalogRemovedUpstream :execrows
-- MarkModelCatalogRemovedUpstream 标记 models.dev 已下架的目录条目（不删本地行）；已标记的不重复更新。
UPDATE model_catalog
SET removed_upstream_at = now(),
    updated_at = now()
WHERE canonical_id = sqlc.arg(canonical_id)
    AND removed_upstream_at IS NULL;

-- name: ListModelCatalogCanonicalIDs :many
-- ListModelCatalogCanonicalIDs 列出当前目录全部 canonical_id（含已下架），供同步推导「feed 不含 → 标记下架」。
SELECT canonical_id, removed_upstream_at
FROM model_catalog
ORDER BY canonical_id ASC;

-- name: UpsertModelLab :exec
-- UpsertModelLab 按 slug 登记出品方；已存在时只补名称，不动图标
-- （图标由 UpdateModelLabLogo 单独维护，避免目录同步把已抓到的图标覆盖成空）。
INSERT INTO model_labs (slug, name)
VALUES (sqlc.arg(slug), sqlc.arg(name))
ON CONFLICT (slug) DO UPDATE
SET name = EXCLUDED.name,
    updated_at = now();

-- name: ListModelLabsNeedingLogo :many
-- ListModelLabsNeedingLogo 列出需要抓取图标的出品方：从未同步过，或上次同步早于给定时间。
-- 有图标的不重复抓：厂商 logo 极少变动，每次目录同步都全量重抓只是白费上游带宽。
SELECT slug
FROM model_labs
WHERE logo_synced_at IS NULL
   OR logo_synced_at < sqlc.arg(stale_before)
ORDER BY slug;

-- name: UpdateModelLabLogo :exec
-- UpdateModelLabLogo 写入图标内容并打上同步时间。
-- 空串是合法结果，表示「上游确实没有这个图标」——同样要记时间，否则每次同步都会重试。
UPDATE model_labs
SET logo_svg = sqlc.arg(logo_svg),
    logo_synced_at = now(),
    updated_at = now()
WHERE slug = sqlc.arg(slug);
