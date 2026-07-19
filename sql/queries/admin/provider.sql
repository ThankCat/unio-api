-- name: ListProviders :many
-- ListProviders 列出全部 provider，按 id 升序，供 admin 管理台展示。
SELECT id, slug, name, status, created_at, updated_at, archived_at
FROM providers
ORDER BY id;

-- name: ListProvidersPage :many
-- ListProvidersPage 按状态/关键字过滤后分页列出 provider；status、q 为 NULL 时不过滤。
SELECT id, slug, name, status, created_at, updated_at, archived_at
FROM providers
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR slug ILIKE '%' || sqlc.narg('q')::text || '%'
    OR name ILIKE '%' || sqlc.narg('q')::text || '%'
  )
ORDER BY id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountProviders :one
-- CountProviders 返回与 ListProvidersPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM providers
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR slug ILIKE '%' || sqlc.narg('q')::text || '%'
    OR name ILIKE '%' || sqlc.narg('q')::text || '%'
  );

-- name: GetProvider :one
-- GetProvider 按 id 读取单个 provider。
SELECT id, slug, name, status, created_at, updated_at, archived_at
FROM providers
WHERE id = $1
LIMIT 1;

-- name: CreateProvider :one
-- CreateProvider 创建 provider；slug 全局唯一由 DB 唯一约束保证。
INSERT INTO providers (slug, name, status)
VALUES (sqlc.arg(slug), sqlc.arg(name), sqlc.arg(status))
RETURNING id, slug, name, status, created_at, updated_at, archived_at;

-- name: UpdateProvider :one
-- UpdateProvider 更新 provider 的展示名与启停状态；slug 作为稳定业务标识不可变。
UPDATE providers
SET name = sqlc.arg(name), status = sqlc.arg(status), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, slug, name, status, created_at, updated_at, archived_at;

-- name: DeleteProvider :execrows
-- DeleteProvider 物理删除 provider，用于清理录错且从未使用的脏数据。
-- provider 无自身配置子表，故不级联：名下仍有 channel，或被 request/cost 历史（NO ACTION 外键）
-- 引用时，DB 拒绝删除（23503），上层降级为 conflict，提示先删渠道或改用停用。
DELETE FROM providers WHERE id = sqlc.arg(id);

-- name: ArchiveProviderCascade :execrows
-- ArchiveProviderCascade 归档 provider：单事务内把名下未归档渠道一并归档（释放渠道名：追加
-- __archived_<id> 后缀）、从所有线路候选池移除这些渠道（route_channels 纯配置、无账务外键，可安全删），
-- 再把 provider 置 archived。slug 与 provider.name 不变（服务商标识稳定，归档大概率不再复用同 slug）。
-- 返回 providers 受影响行数（0 = provider 不存在或已归档）。恢复不向下级联，需逐个恢复渠道。
WITH archived_channels AS (
    UPDATE channels
    SET status = 'archived', archived_at = now(), name = name || '__archived_' || id::text
    WHERE provider_id = sqlc.arg(id) AND status <> 'archived'
    RETURNING id
),
cleared_pools AS (
    DELETE FROM route_channels WHERE channel_id IN (SELECT id FROM archived_channels)
)
UPDATE providers
SET status = 'archived', archived_at = now()
WHERE providers.id = sqlc.arg(id) AND providers.status <> 'archived';

-- name: ArchiveProviderWithReplacement :one
-- Atomically add one external healthy replacement to affected routes, archive all target channels, then archive provider.
WITH replacement AS (
    SELECT c.id
    FROM channels c
    JOIN providers p ON p.id = c.provider_id
    WHERE c.id = sqlc.arg(replacement_channel_id)
      AND c.provider_id <> sqlc.arg(id)
      AND c.status = 'enabled'
      AND c.credential_valid
      AND c.credential <> ''
      AND c.base_url <> ''
      AND p.status = 'enabled'
),
affected_routes AS (
    SELECT DISTINCT rc.route_id
    FROM route_channels rc
    JOIN channels c ON c.id = rc.channel_id
    WHERE c.provider_id = sqlc.arg(id)
),
added AS (
    INSERT INTO route_channels (route_id, channel_id)
    SELECT ar.route_id, replacement.id
    FROM affected_routes ar CROSS JOIN replacement
    ON CONFLICT (route_id, channel_id) DO NOTHING
    RETURNING route_id
),
archived_channels AS (
    UPDATE channels
    SET status = 'archived', archived_at = now(), name = name || '__archived_' || id::text
    WHERE provider_id = sqlc.arg(id)
      AND status <> 'archived'
      AND EXISTS (SELECT 1 FROM replacement)
      AND (SELECT COUNT(*) FROM added) >= 0
    RETURNING channels.id
),
cleared_pools AS (
    DELETE FROM route_channels WHERE channel_id IN (SELECT id FROM archived_channels)
    RETURNING route_id
),
archived_provider AS (
    UPDATE providers
    SET status = 'archived', archived_at = now()
    WHERE providers.id = sqlc.arg(id)
      AND providers.status <> 'archived'
      AND EXISTS (SELECT 1 FROM replacement)
      AND (SELECT COUNT(*) FROM cleared_pools) >= 0
    RETURNING providers.id
)
SELECT COUNT(*)::bigint FROM archived_provider;

-- name: ListEnabledRoutesEmptiedByProvider :many
-- 归档目标 provider 全部渠道后将失去最后一个显式池成员的启用线路。
SELECT DISTINCT rt.id, rt.name
FROM routes rt
JOIN route_channels target ON target.route_id = rt.id
JOIN channels target_channel ON target_channel.id = target.channel_id AND target_channel.provider_id = sqlc.arg(provider_id)
WHERE rt.status = 'enabled'
  AND NOT EXISTS (
      SELECT 1
      FROM route_channels other
      JOIN channels other_channel ON other_channel.id = other.channel_id
      WHERE other.route_id = rt.id
        AND other_channel.provider_id <> sqlc.arg(provider_id)
  )
ORDER BY rt.id;

-- name: RestoreProvider :execrows
-- RestoreProvider 取消归档 provider：archived → disabled（archived_at 清空）。不向下级联恢复渠道。
UPDATE providers
SET status = 'disabled', archived_at = NULL
WHERE id = sqlc.arg(id) AND status = 'archived';

-- §3.2 服务商聚合视图只读运维聚合。轻聚合：无 12 卡，表 + 4 Tab 抽屉。
-- provider 维度天然由 request_attempts.provider_id 归因（每次尝试记录 provider）。
-- 区间 [from,to) 半开；attempt 粒度性能/成功率；延迟由 completed_at-started_at 推导（毫秒）。

-- name: ProvidersOpsTable :many
-- ProvidersOpsTable 服务商运维主表（分页）：静态元数据 + 渠道/模型/线路数；指标在详情页聚合。
SELECT
    p.id,
    p.slug,
    p.name,
    p.status,
    p.created_at,
    (SELECT COUNT(*) FROM channels c WHERE c.provider_id = p.id) AS channel_total,
    (
        SELECT COUNT(DISTINCT cm.model_id)
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id
        WHERE c.provider_id = p.id AND cm.status = 'enabled'
    ) AS models_count,
    (
        SELECT COUNT(DISTINCT rt.id)
        FROM routes rt
        JOIN route_channels rc ON rc.route_id = rt.id
        JOIN channels c ON c.id = rc.channel_id
        WHERE c.provider_id = p.id
    ) AS routes_count
FROM providers p
WHERE (sqlc.narg('status')::text IS NULL OR p.status = sqlc.narg('status')::text)
  AND (sqlc.narg('search')::text IS NULL OR p.name ILIKE '%' || sqlc.narg('search')::text || '%' OR p.slug ILIKE '%' || sqlc.narg('search')::text || '%')
ORDER BY
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'name') IN ('', 'name') AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN p.name END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'name') IN ('', 'name') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN p.name END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN p.created_at END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN p.created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'channels' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(*) FROM channels c WHERE c.provider_id = p.id
    ) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'channels' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(*) FROM channels c WHERE c.provider_id = p.id
    ) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'models' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(DISTINCT cm.model_id)
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id
        WHERE c.provider_id = p.id AND cm.status = 'enabled'
    ) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'models' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(DISTINCT cm.model_id)
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id
        WHERE c.provider_id = p.id AND cm.status = 'enabled'
    ) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'routes' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(DISTINCT rt.id)
        FROM routes rt
        JOIN route_channels rc ON rc.route_id = rt.id
        JOIN channels c ON c.id = rc.channel_id
        WHERE c.provider_id = p.id
    ) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'routes' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(DISTINCT rt.id)
        FROM routes rt
        JOIN route_channels rc ON rc.route_id = rt.id
        JOIN channels c ON c.id = rc.channel_id
        WHERE c.provider_id = p.id
    ) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN p.status END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN p.status END ASC NULLS LAST,
  p.name
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ProvidersOpsTableCount :one
SELECT COUNT(*) AS total
FROM providers p
WHERE (sqlc.narg('status')::text IS NULL OR p.status = sqlc.narg('status')::text)
  AND (sqlc.narg('search')::text IS NULL OR p.name ILIKE '%' || sqlc.narg('search')::text || '%' OR p.slug ILIKE '%' || sqlc.narg('search')::text || '%');

-- name: ProviderOpsDetail :one
-- ProviderOpsDetail 单服务商详情概览：渠道数 + attempt 聚合 + Token/利润/TPS。
-- 全部用标量子查询，避免 CROSS JOIN + COUNT 混用导致 GROUP BY 错误，且区间内无 attempt 时仍返回一行。
WITH money AS (
    SELECT
        COALESCE(SUM(
            u.uncached_input_tokens + u.cache_read_input_tokens
            + u.cache_write_5m_input_tokens + u.cache_write_1h_input_tokens + u.cache_write_30m_input_tokens
            + u.output_tokens_total
        ), 0)::bigint AS tokens_total,
        COALESCE(SUM(le.amount) FILTER (WHERE le.entry_type = 'debit' AND le.currency = 'USD'), 0)::numeric AS revenue_usd,
        COALESCE(SUM(cs.total_cost_amount) FILTER (WHERE cs.currency = 'USD'), 0)::numeric AS cost_usd
    FROM request_records r
    LEFT JOIN usage_records u ON u.request_record_id = r.id
    LEFT JOIN cost_snapshots cs ON cs.request_record_id = r.id
    LEFT JOIN ledger_entries le ON le.request_record_id = r.id
    WHERE r.final_provider_id = sqlc.arg('provider_id')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
),
tps AS (
    SELECT COALESCE(
        SUM(u.output_tokens_total)::float8 / NULLIF(SUM(
            CASE
                WHEN a.completed_at IS NOT NULL
                THEN EXTRACT(EPOCH FROM (a.completed_at - COALESCE(a.response_started_at, a.started_at)))
            END
        ), 0),
        0
    )::float8 AS avg_tps
    FROM request_attempts a
    JOIN usage_records u ON u.request_record_id = a.request_record_id
    WHERE a.provider_id = sqlc.arg('provider_id')
      AND a.status = 'succeeded'
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
),
attempts AS (
    SELECT *
    FROM request_attempts a
    WHERE a.provider_id = sqlc.arg('provider_id')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
)
SELECT
    (SELECT COUNT(*) FROM channels c WHERE c.provider_id = sqlc.arg('provider_id')) AS channel_total,
    (SELECT COUNT(*) FROM channels c WHERE c.provider_id = sqlc.arg('provider_id') AND c.status = 'enabled') AS channel_enabled,
    (SELECT COUNT(*) FROM attempts WHERE status = 'succeeded' OR fault_party = 'upstream') AS attempt_total,
    (SELECT COUNT(*) FROM attempts WHERE status = 'succeeded') AS attempt_succeeded,
    (SELECT COUNT(*) FROM attempts WHERE status = 'failed' AND (error_code ILIKE '%timeout%' OR error_code = 'context_deadline_exceeded')) AS timeout_total,
    (SELECT COUNT(*) FROM attempts WHERE status = 'succeeded' AND completed_at IS NOT NULL) AS latency_sample,
    (SELECT COALESCE(AVG(CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 FROM attempts) AS latency_avg,
    (SELECT COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 FROM attempts) AS latency_p50,
    (SELECT COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 FROM attempts) AS latency_p90,
    (SELECT COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 FROM attempts) AS latency_p95,
    (SELECT COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 FROM attempts) AS latency_p99,
    (SELECT tokens_total FROM money) AS tokens_total,
    (SELECT revenue_usd FROM money) AS revenue_usd,
    (SELECT cost_usd FROM money) AS cost_usd,
    (SELECT avg_tps FROM tps) AS avg_tps;

-- name: ProviderOpsChannelCatalog :many
-- ProviderOpsChannelCatalog 服务商渠道清单（列表 Tip，无指标）。
SELECT c.id, c.name, c.status
FROM channels c
WHERE c.provider_id = sqlc.arg('provider_id')
ORDER BY c.name, c.id;

-- name: ProviderOpsModelCatalog :many
-- ProviderOpsModelCatalog 服务商绑定模型清单（列表 Tip）。
SELECT DISTINCT m.model_id, m.display_name
FROM models m
JOIN channel_models cm ON cm.model_id = m.id AND cm.status = 'enabled'
JOIN channels c ON c.id = cm.channel_id
WHERE c.provider_id = sqlc.arg('provider_id')
ORDER BY m.model_id
LIMIT 500;

-- name: ProviderOpsRouteCatalog :many
-- ProviderOpsRouteCatalog 引用本服务商渠道的线路清单（列表 Tip）。
SELECT DISTINCT rt.id, rt.name, rt.status, rt.mode
FROM routes rt
JOIN route_channels rc ON rc.route_id = rt.id
JOIN channels c ON c.id = rc.channel_id
WHERE c.provider_id = sqlc.arg('provider_id')
ORDER BY rt.name, rt.id
LIMIT 500;

-- name: ProviderOpsChannels :many
-- ProviderOpsChannels 单服务商下渠道精简子列表 + attempt 指标（抽屉渠道 Tab）。
SELECT
    c.id,
    c.name,
    c.base_url,
    c.status,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream') AS attempt_total,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS attempt_succeeded,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded' AND a.completed_at IS NOT NULL) AS latency_sample,
    COALESCE(AVG(CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p50,
    COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p90,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p99
FROM channels c
LEFT JOIN request_attempts a
    ON a.channel_id = c.id
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
WHERE c.provider_id = sqlc.arg('provider_id')
GROUP BY c.id, c.name, c.base_url, c.status
ORDER BY attempt_total DESC, c.id;

-- name: ProviderOpsPerformanceTimeseries :many
-- ProviderOpsPerformanceTimeseries 单服务商 attempt 趋势（抽屉性能 Tab）。
SELECT
    date_trunc(sqlc.arg('unit')::text, created_at)::timestamptz AS bucket,
    COUNT(*) FILTER (WHERE status = 'succeeded' OR fault_party = 'upstream') AS attempt_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS attempt_succeeded,
    COALESCE(AVG(CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg
FROM request_attempts
WHERE provider_id = sqlc.arg('provider_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY bucket
ORDER BY bucket;

-- name: ProviderOpsErrors :many
-- ProviderOpsErrors 单服务商错误明细（抽屉错误 Tab，分页）。
SELECT
    a.created_at,
    c.name AS channel_name,
    a.upstream_model,
    a.error_code,
    a.upstream_status_code,
    r.request_id
FROM request_attempts a
JOIN request_records r ON r.id = a.request_record_id
JOIN channels c ON c.id = a.channel_id
WHERE a.provider_id = sqlc.arg('provider_id')
  AND a.status = 'failed'
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY a.created_at DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ProviderOpsErrorsCount :one
SELECT COUNT(*) AS total
FROM request_attempts a
WHERE a.provider_id = sqlc.arg('provider_id')
  AND a.status = 'failed'
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz);
