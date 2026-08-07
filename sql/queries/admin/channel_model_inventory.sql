-- Channel model inventory: upstream discovery snapshots, dynamic catalog matching and model verification jobs.

-- name: CreateChannelModelDiscoveryRun :one
INSERT INTO channel_model_discovery_runs (
    channel_id, source, channel_config_revision, provider_origin_revision, provider_status_revision
)
SELECT c.id, sqlc.arg(source), c.config_revision, p.origin_revision, p.status_revision
FROM channels c
JOIN providers p ON p.id = c.provider_id
WHERE c.id = sqlc.arg(channel_id) AND c.status <> 'archived'
RETURNING *;

-- name: GetChannelModelDiscoveryRun :one
SELECT * FROM channel_model_discovery_runs
WHERE id = sqlc.arg(run_id) AND channel_id = sqlc.arg(channel_id);

-- name: ListChannelModelDiscoveryRuns :many
SELECT * FROM channel_model_discovery_runs
WHERE channel_id = sqlc.arg(channel_id)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountChannelModelDiscoveryRuns :one
SELECT COUNT(*) FROM channel_model_discovery_runs WHERE channel_id = sqlc.arg(channel_id);

-- name: ClaimNextChannelModelDiscoveryRun :one
WITH candidate AS (
    SELECT id
    FROM channel_model_discovery_runs
    WHERE status = 'queued' AND next_attempt_at <= now()
    ORDER BY CASE source WHEN 'setup' THEN 0 WHEN 'manual' THEN 1 ELSE 2 END, next_attempt_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE channel_model_discovery_runs r
SET status = 'running', attempt_count = attempt_count + 1,
    started_at = COALESCE(started_at, now()), error_code = NULL, message = NULL
FROM candidate
WHERE r.id = candidate.id
RETURNING r.*;

-- name: GetChannelModelDiscoveryExecutionSnapshot :one
SELECT
    r.*,
    c.provider_id,
    c.protocol,
    c.adapter_key,
    c.credential,
    c.status AS channel_status,
    c.config_revision AS current_channel_config_revision,
    p.slug AS provider_slug,
    p.origin,
    p.origin_revision AS current_provider_origin_revision,
    p.status_revision AS current_provider_status_revision
FROM channel_model_discovery_runs r
JOIN channels c ON c.id = r.channel_id
JOIN providers p ON p.id = c.provider_id
WHERE r.id = sqlc.arg(run_id);

-- name: InsertChannelModelDiscoveryItem :exec
INSERT INTO channel_model_discovery_items (run_id, upstream_model, owned_by, upstream_created_at)
VALUES (sqlc.arg(run_id), sqlc.arg(upstream_model), sqlc.narg(owned_by), sqlc.narg(upstream_created_at))
ON CONFLICT (run_id, upstream_model) DO NOTHING;

-- name: FinishChannelModelDiscoveryRun :one
WITH current_revisions AS (
    SELECT c.config_revision, p.origin_revision, p.status_revision
    FROM channels c JOIN providers p ON p.id = c.provider_id
    WHERE c.id = (SELECT channel_id FROM channel_model_discovery_runs WHERE id = sqlc.arg(run_id))
)
UPDATE channel_model_discovery_runs r
SET status = CASE WHEN
        r.channel_config_revision = current_revisions.config_revision
        AND r.provider_origin_revision = current_revisions.origin_revision
        AND r.provider_status_revision = current_revisions.status_revision
    THEN 'succeeded' ELSE 'stale' END,
    model_count = sqlc.arg(model_count), warning_code = sqlc.narg(warning_code),
    error_code = CASE WHEN
        r.channel_config_revision = current_revisions.config_revision
        AND r.provider_origin_revision = current_revisions.origin_revision
        AND r.provider_status_revision = current_revisions.status_revision
    THEN NULL ELSE 'stale_revision' END,
    message = CASE WHEN
        r.channel_config_revision = current_revisions.config_revision
        AND r.provider_origin_revision = current_revisions.origin_revision
        AND r.provider_status_revision = current_revisions.status_revision
    THEN NULL ELSE '渠道或 Provider 配置已变化，本次发现结果仅保留为历史记录' END,
    completed_at = now()
FROM current_revisions
WHERE r.id = sqlc.arg(run_id) AND r.status = 'running'
RETURNING r.*;

-- name: RetryChannelModelDiscoveryRun :one
UPDATE channel_model_discovery_runs
SET status = 'queued', next_attempt_at = now() + make_interval(secs => sqlc.arg(backoff_seconds)::int),
    error_code = sqlc.arg(error_code), message = sqlc.arg(message)
WHERE id = sqlc.arg(run_id) AND status = 'running'
RETURNING *;

-- name: FailChannelModelDiscoveryRun :one
UPDATE channel_model_discovery_runs
SET status = 'failed', error_code = sqlc.arg(error_code), message = sqlc.arg(message), completed_at = now()
WHERE id = sqlc.arg(run_id) AND status = 'running'
RETURNING *;

-- name: GetLatestSuccessfulChannelModelDiscoveryRun :one
SELECT * FROM channel_model_discovery_runs
WHERE channel_id = sqlc.arg(channel_id) AND status = 'succeeded'
ORDER BY completed_at DESC, id DESC
LIMIT 1;

-- name: GetLatestChannelModelDiscoveryRun :one
SELECT * FROM channel_model_discovery_runs
WHERE channel_id = sqlc.arg(channel_id)
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: ListChannelModelDiscoveryItems :many
SELECT * FROM channel_model_discovery_items
WHERE run_id = sqlc.arg(run_id)
ORDER BY upstream_model;

-- name: EnqueueDueChannelModelDiscoveries :many
INSERT INTO channel_model_discovery_runs (
    channel_id, source, channel_config_revision, provider_origin_revision, provider_status_revision
)
SELECT c.id, 'scheduled', c.config_revision, p.origin_revision, p.status_revision
FROM channels c
JOIN providers p ON p.id = c.provider_id
WHERE c.status = 'enabled'
  AND NOT EXISTS (
      SELECT 1 FROM channel_model_discovery_runs active
      WHERE active.channel_id = c.id AND active.status IN ('queued', 'running')
  )
  AND NOT EXISTS (
      SELECT 1 FROM channel_model_discovery_runs recent
      WHERE recent.channel_id = c.id
        AND recent.created_at > now() - make_interval(secs => sqlc.arg(interval_seconds)::int)
  )
ORDER BY c.id
ON CONFLICT DO NOTHING
RETURNING *;

-- name: DeleteOldChannelModelDiscoveryRuns :execrows
DELETE FROM channel_model_discovery_runs target
WHERE target.channel_id = sqlc.arg(target_channel_id)
  AND target.status IN ('succeeded', 'failed', 'stale')
  AND target.id IN (
      SELECT old.id FROM channel_model_discovery_runs old
      WHERE old.channel_id = sqlc.arg(target_channel_id)
        AND old.status IN ('succeeded', 'failed', 'stale')
      ORDER BY old.created_at DESC, old.id DESC
      OFFSET sqlc.arg(keep_per_channel)
  )
  AND target.id <> COALESCE((
      SELECT succeeded.id FROM channel_model_discovery_runs succeeded
      WHERE succeeded.channel_id = sqlc.arg(target_channel_id) AND succeeded.status = 'succeeded'
      ORDER BY succeeded.completed_at DESC, succeeded.id DESC
      LIMIT 1
  ), 0);

-- name: GetChannelModelInventoryContext :one
SELECT c.id AS channel_id, c.name AS channel_name, c.status AS channel_status,
    c.config_revision, c.provider_id, c.protocol, c.adapter_key,
    p.slug AS provider_slug, p.origin_revision, p.status_revision
FROM channels c JOIN providers p ON p.id = c.provider_id
WHERE c.id = sqlc.arg(channel_id);

-- name: ListChannelModelInventoryBindings :many
SELECT cm.id, cm.channel_id, cm.model_id, cm.upstream_model, cm.status,
    cm.created_at, cm.updated_at, m.model_id AS model_external_id,
    m.display_name AS model_display_name, m.status AS model_status,
    l.canonical_id AS adopted_canonical_id
FROM channel_models cm
JOIN models m ON m.id = cm.model_id
LEFT JOIN model_catalog_links l ON l.model_id = m.id
WHERE cm.channel_id = sqlc.arg(channel_id)
ORDER BY cm.upstream_model, m.model_id;

-- name: ListChannelModelInventoryMatches :many
WITH requested AS (
    SELECT DISTINCT btrim(value) AS upstream_model
    FROM unnest(sqlc.arg(upstream_models)::text[]) AS value
    WHERE btrim(value) <> ''
)
SELECT r.upstream_model,
    exact_model.id AS exact_model_id,
    exact_model.model_id AS exact_model_external_id,
    exact_model.display_name AS exact_model_display_name,
    exact_model.status AS exact_model_status,
    exact_link.canonical_id AS exact_model_canonical_id,
    mc.canonical_id AS catalog_canonical_id,
    mc.lab AS catalog_lab,
    mc.display_name AS catalog_display_name,
    (mc.removed_upstream_at IS NOT NULL)::boolean AS catalog_removed_upstream,
    adopted.id AS adopted_model_id,
    adopted.model_id AS adopted_model_external_id,
    adopted.display_name AS adopted_model_display_name,
    adopted.status AS adopted_model_status
FROM requested r
LEFT JOIN models exact_model ON lower(exact_model.model_id) = lower(r.upstream_model)
LEFT JOIN model_catalog_links exact_link ON exact_link.model_id = exact_model.id
LEFT JOIN model_catalog mc
    ON lower(regexp_replace(mc.canonical_id, '^.*/', '')) = lower(r.upstream_model)
LEFT JOIN model_catalog_links adopted_link ON adopted_link.canonical_id = mc.canonical_id
LEFT JOIN models adopted ON adopted.id = adopted_link.model_id
ORDER BY r.upstream_model, mc.removed_upstream_at NULLS FIRST, mc.canonical_id, adopted.id;

-- name: ListLatestChannelModelVerificationItems :many
SELECT DISTINCT ON (i.model_id, i.upstream_model)
    i.*, r.channel_id, r.channel_config_revision, r.provider_origin_revision, r.provider_status_revision,
    r.status AS run_status, r.created_at AS run_created_at
FROM channel_model_verification_items i
JOIN channel_model_verification_runs r ON r.id = i.run_id
WHERE r.channel_id = sqlc.arg(channel_id) AND i.status IN ('succeeded', 'failed', 'skipped', 'stale')
ORDER BY i.model_id, i.upstream_model, i.created_at DESC, i.id DESC;

-- name: CreateChannelModelVerificationRun :one
INSERT INTO channel_model_verification_runs (
    channel_id, source, channel_config_revision, provider_origin_revision, provider_status_revision, total_count
)
SELECT c.id, sqlc.arg(source), c.config_revision, p.origin_revision, p.status_revision, sqlc.arg(total_count)
FROM channels c JOIN providers p ON p.id = c.provider_id
WHERE c.id = sqlc.arg(channel_id) AND c.status <> 'archived'
RETURNING *;

-- name: CreateChannelModelVerificationItem :one
INSERT INTO channel_model_verification_items (run_id, model_id, upstream_model)
SELECT r.id, cm.model_id,
    COALESCE(NULLIF(btrim(sqlc.narg(upstream_model)::text), ''), cm.upstream_model)
FROM channel_model_verification_runs r
JOIN channel_models cm ON cm.channel_id = r.channel_id AND cm.model_id = sqlc.arg(model_id)
WHERE r.id = sqlc.arg(run_id) AND r.status = 'queued'
RETURNING *;

-- name: GetChannelModelVerificationRun :one
SELECT * FROM channel_model_verification_runs
WHERE id = sqlc.arg(run_id) AND channel_id = sqlc.arg(channel_id);

-- name: ClaimNextChannelModelVerificationRun :one
WITH candidate AS (
    SELECT id FROM channel_model_verification_runs
    WHERE status = 'queued'
    ORDER BY CASE source WHEN 'setup' THEN 0 ELSE 1 END, created_at, id
    FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE channel_model_verification_runs r
SET status = 'running', started_at = now()
FROM candidate
WHERE r.id = candidate.id
RETURNING r.*;

-- name: GetChannelModelVerificationExecutionSnapshot :one
SELECT r.*, c.provider_id, c.protocol, c.adapter_key, c.credential,
    c.status AS channel_status, c.config_revision AS current_channel_config_revision,
    p.slug AS provider_slug, p.origin,
    p.origin_revision AS current_provider_origin_revision,
    p.status_revision AS current_provider_status_revision
FROM channel_model_verification_runs r
JOIN channels c ON c.id = r.channel_id
JOIN providers p ON p.id = c.provider_id
WHERE r.id = sqlc.arg(run_id);

-- name: ListChannelModelVerificationItems :many
SELECT * FROM channel_model_verification_items
WHERE run_id = sqlc.arg(run_id)
ORDER BY id;

-- name: CompleteChannelModelVerificationItem :one
UPDATE channel_model_verification_items
SET status = CASE WHEN sqlc.arg(success)::boolean THEN 'succeeded' ELSE 'failed' END,
    success = sqlc.arg(success), http_status = sqlc.arg(http_status),
    error_code = sqlc.narg(error_code), message = sqlc.narg(message), latency_ms = sqlc.arg(latency_ms),
    provider_probe_record_id = sqlc.narg(provider_probe_record_id), completed_at = now()
WHERE id = sqlc.arg(item_id) AND status = 'queued'
RETURNING *;

-- name: SkipRemainingChannelModelVerificationItems :execrows
UPDATE channel_model_verification_items
SET status = 'skipped', success = false, error_code = sqlc.arg(error_code),
    message = sqlc.arg(message), completed_at = now()
WHERE run_id = sqlc.arg(run_id) AND status = 'queued';

-- name: FinishChannelModelVerificationRun :one
WITH totals AS (
    SELECT COUNT(*)::int AS total_count,
        COUNT(*) FILTER (WHERE status = 'succeeded')::int AS succeeded_count,
        COUNT(*) FILTER (WHERE status IN ('failed', 'skipped'))::int AS failed_count
    FROM channel_model_verification_items WHERE run_id = sqlc.arg(run_id)
), current_revisions AS (
    SELECT c.config_revision, p.origin_revision, p.status_revision
    FROM channels c JOIN providers p ON p.id = c.provider_id
    WHERE c.id = (SELECT channel_id FROM channel_model_verification_runs WHERE id = sqlc.arg(run_id))
)
UPDATE channel_model_verification_runs r
SET status = CASE
        WHEN r.channel_config_revision <> current_revisions.config_revision
          OR r.provider_origin_revision <> current_revisions.origin_revision
          OR r.provider_status_revision <> current_revisions.status_revision THEN 'stale'
        WHEN totals.failed_count > 0 THEN 'failed'
        ELSE 'succeeded' END,
    total_count = totals.total_count, succeeded_count = totals.succeeded_count,
    failed_count = totals.failed_count,
    error_code = CASE WHEN
        r.channel_config_revision <> current_revisions.config_revision
        OR r.provider_origin_revision <> current_revisions.origin_revision
        OR r.provider_status_revision <> current_revisions.status_revision
        THEN 'stale_revision' ELSE sqlc.narg(error_code) END,
    message = CASE WHEN
        r.channel_config_revision <> current_revisions.config_revision
        OR r.provider_origin_revision <> current_revisions.origin_revision
        OR r.provider_status_revision <> current_revisions.status_revision
        THEN '渠道或 Provider 配置已变化，本次验证不能用于启用绑定' ELSE sqlc.narg(message) END,
    completed_at = now()
FROM totals, current_revisions
WHERE r.id = sqlc.arg(run_id) AND r.status = 'running'
RETURNING r.*;

-- name: GetCurrentChannelModelVerificationEvidence :one
SELECT i.*
FROM channel_model_verification_items i
JOIN channel_model_verification_runs r ON r.id = i.run_id
JOIN channels c ON c.id = r.channel_id
JOIN providers p ON p.id = c.provider_id
WHERE i.id = sqlc.arg(item_id)
  AND r.channel_id = sqlc.arg(channel_id)
  AND i.model_id = sqlc.arg(model_id)
  AND i.upstream_model = sqlc.arg(upstream_model)
  AND i.status = 'succeeded' AND i.success = true
  AND r.status IN ('succeeded', 'failed')
  AND r.channel_config_revision = c.config_revision
  AND r.provider_origin_revision = p.origin_revision
  AND r.provider_status_revision = p.status_revision;
