-- name: UpsertModelCatalogEntry :one
-- UpsertModelCatalogEntry 按 canonical_id 全量 upsert 目录条目；覆盖时刷新 fingerprint/synced_at 并清除下架标记。
INSERT INTO model_catalog (
    canonical_id,
    lab,
    display_name,
    context_window_tokens,
    max_output_tokens,
    input_price_usd_per_million_tokens,
    output_price_usd_per_million_tokens,
    release_date,
    fingerprint,
    removed_upstream_at,
    synced_at
)
VALUES (
    sqlc.arg(canonical_id),
    sqlc.arg(lab),
    sqlc.arg(display_name),
    sqlc.narg(context_window_tokens),
    sqlc.narg(max_output_tokens),
    sqlc.narg(input_price_usd_per_million_tokens),
    sqlc.narg(output_price_usd_per_million_tokens),
    sqlc.narg(release_date),
    sqlc.arg(fingerprint),
    NULL,
    now()
)
ON CONFLICT (canonical_id) DO UPDATE
SET lab = EXCLUDED.lab,
    display_name = EXCLUDED.display_name,
    context_window_tokens = EXCLUDED.context_window_tokens,
    max_output_tokens = EXCLUDED.max_output_tokens,
    input_price_usd_per_million_tokens = EXCLUDED.input_price_usd_per_million_tokens,
    output_price_usd_per_million_tokens = EXCLUDED.output_price_usd_per_million_tokens,
    release_date = EXCLUDED.release_date,
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
