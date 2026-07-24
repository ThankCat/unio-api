-- name: ListRouteChannelsDetailed :many
-- ListRouteChannelsDetailed 列出某线路渠道池，连带渠道展示名/provider，供 admin 管理台展示。
SELECT
    rc.channel_id,
    c.name AS channel_name,
    c.provider_id,
    p.slug AS provider_slug
FROM route_channels rc
JOIN channels c ON c.id = rc.channel_id
JOIN providers p ON p.id = c.provider_id
WHERE rc.route_id = sqlc.arg(route_id)
ORDER BY rc.channel_id;

-- name: AddRouteChannel :exec
-- AddRouteChannel 把一条渠道加入线路池；重复加入由主键幂等忽略。
INSERT INTO route_channels (route_id, channel_id)
VALUES (sqlc.arg(route_id), sqlc.arg(channel_id))
ON CONFLICT (route_id, channel_id) DO NOTHING;

-- name: CountRouteChannels :one
SELECT COUNT(*) FROM route_channels WHERE route_id = sqlc.arg(route_id);

-- name: DeleteRouteChannels :exec
-- DeleteRouteChannels 清空某线路的渠道池（设置渠道池前先清空，整体在事务内重建）。
DELETE FROM route_channels WHERE route_id = sqlc.arg(route_id);

-- name: CreateRoute :one
-- CreateRoute 创建线路；price_ratio 是客户售价倍率（DEC-026：客户售价 = 模型基准价 × 倍率）；
-- rpm/tpm/rpd_limit 是线路级限流上限（DEC-027：NULL=继承线路默认限流，0=不限，>0=上限）；
-- sticky_enabled 是会话粘性开关（NULL=继承系统设置默认）；
-- balanced/fixed 的渠道数量约束由 service 层校验。
INSERT INTO routes (name, mode, status, description, price_ratio, rpm_limit, tpm_limit, rpd_limit, sticky_enabled)
VALUES (
    sqlc.arg(name),
    sqlc.arg(mode),
    sqlc.arg(status),
    sqlc.narg(description),
    sqlc.arg(price_ratio),
    sqlc.narg(rpm_limit),
    sqlc.narg(tpm_limit),
    sqlc.narg(rpd_limit),
    sqlc.narg(sticky_enabled)
)
RETURNING *;

-- name: ListRoutes :many
-- ListRoutes 列出全部线路，供 admin 管理台展示。
SELECT * FROM routes ORDER BY id ASC;

-- name: UpdateRoute :one
-- UpdateRoute 更新线路的名称/策略/启停/简介/售价倍率/线路级限流上限/会话粘性开关。
UPDATE routes
SET name = sqlc.arg(name),
    mode = sqlc.arg(mode),
    status = sqlc.arg(status),
    description = sqlc.narg(description),
    price_ratio = sqlc.arg(price_ratio),
    rpm_limit = sqlc.narg(rpm_limit),
    tpm_limit = sqlc.narg(tpm_limit),
    rpd_limit = sqlc.narg(rpd_limit),
    sticky_enabled = sqlc.narg(sticky_enabled),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteRoute :execrows
-- DeleteRoute 删除线路；被 api_keys/users 引用时由 DB 外键拒绝（23503）。
DELETE FROM routes WHERE id = sqlc.arg(id);

-- name: CountApiKeysByRoute :one
-- CountApiKeysByRoute 统计绑定到某线路的 api_key 数量，供线路归档护栏（有 key 则拦截）。
SELECT COUNT(*) AS total FROM api_keys WHERE route_id = sqlc.arg(route_id);

-- name: ArchiveRoute :execrows
-- ArchiveRoute 归档线路（要求已无绑定 key，服务层护栏保证）：置 archived + 释放全局唯一线路名
-- （追加 __archived_<id> 后缀）。route_channels 保留（线路已隐藏，便于恢复）。
UPDATE routes
SET status = 'archived', archived_at = now(), name = name || '__archived_' || id::text
WHERE routes.id = sqlc.arg(id) AND routes.status <> 'archived';

-- name: ArchiveRouteWithKeyMigration :execrows
-- ArchiveRouteWithKeyMigration 单事务内先把源线路全部 api_key 迁到目标线路，再归档源线路
-- （§4B 入口②「迁移并归档」）。目标线路有效性（存在且 enabled、非自身）由服务层先校验。
WITH migrated AS (
    UPDATE api_keys SET route_id = sqlc.arg(target_route_id), updated_at = now()
    WHERE route_id = sqlc.arg(id)
)
UPDATE routes
SET status = 'archived', archived_at = now(), name = name || '__archived_' || id::text
WHERE routes.id = sqlc.arg(id) AND routes.status <> 'archived';

-- name: RestoreRoute :execrows
-- RestoreRoute 取消归档线路：archived → disabled（archived_at 清空）。route_channels 原样保留；
-- 归档前已无 key，恢复后仍无 key，需手动绑定或迁入。
UPDATE routes
SET status = 'disabled', archived_at = NULL
WHERE id = sqlc.arg(id) AND status = 'archived';

-- name: ListEmptyRoutesWithKeys :many
-- ListEmptyRoutesWithKeys 列出「候选池为空但仍有绑定 key」的非归档线路，供归档后预警断供。
SELECT rt.id, rt.name,
    (SELECT COUNT(*) FROM api_keys k WHERE k.route_id = rt.id) AS key_count
FROM routes rt
WHERE rt.status <> 'archived'
  AND NOT EXISTS (SELECT 1 FROM route_channels rc WHERE rc.route_id = rt.id)
  AND EXISTS (SELECT 1 FROM api_keys k WHERE k.route_id = rt.id)
ORDER BY rt.id;

-- §3.5 线路路由作战台只读运维聚合。
-- 归因（线路必填 §3.1）：每条请求归属其 API Key 绑定的 api_keys.route_id（线路必填，无默认回落）。
-- request 粒度。fallback：同 request 有 >1 次 attempt 且最终成功；no_channel：error_code 命中无可用渠道码。

-- name: RoutesOpsTable :many
-- RoutesOpsTable 线路运维主表（分页）：静态配置 + 绑定/池/可达模型数；请求指标在详情页聚合。
SELECT
    rt.id,
    rt.name,
    rt.mode,
    rt.status,
    rt.description,
    rt.price_ratio,
    rt.rpm_limit,
    rt.tpm_limit,
    rt.rpd_limit,
    rt.created_at,
    (SELECT COUNT(*) FROM api_keys kk WHERE kk.route_id = rt.id) AS bound_keys,
    (SELECT COUNT(*) FROM route_channels rc WHERE rc.route_id = rt.id) AS pool_channels,
    -- models_count（DEC-031）：池内可达且可解析成本的 distinct 模型（绝对覆盖 OR 基准价+价格倍率）。
    (
        SELECT COUNT(DISTINCT m.id)
        FROM models m
        JOIN channel_models cm ON cm.model_id = m.id AND cm.status = 'enabled'
        JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled'
        WHERE (
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
        AND cm.channel_id IN (SELECT channel_id FROM route_channels WHERE route_id = rt.id)
    ) AS models_count
FROM routes rt
WHERE (sqlc.narg('status')::text IS NULL OR rt.status = sqlc.narg('status')::text)
  AND (sqlc.narg('search')::text IS NULL OR rt.name ILIKE '%' || sqlc.narg('search')::text || '%')
ORDER BY
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'name') IN ('', 'name') AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN rt.name END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'name') IN ('', 'name') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN rt.name END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN rt.created_at END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN rt.created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'bindings' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(*) FROM api_keys kk WHERE kk.route_id = rt.id
    ) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'bindings' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(*) FROM api_keys kk WHERE kk.route_id = rt.id
    ) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'pool_channels' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(*) FROM route_channels rc WHERE rc.route_id = rt.id
    ) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'pool_channels' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(*) FROM route_channels rc WHERE rc.route_id = rt.id
    ) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'models' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(DISTINCT m.id)
        FROM models m
        JOIN channel_models cm ON cm.model_id = m.id AND cm.status = 'enabled'
        JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled'
        WHERE (
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
        AND cm.channel_id IN (SELECT channel_id FROM route_channels WHERE route_id = rt.id)
    ) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'models' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(DISTINCT m.id)
        FROM models m
        JOIN channel_models cm ON cm.model_id = m.id AND cm.status = 'enabled'
        JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled'
        WHERE (
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
        AND cm.channel_id IN (SELECT channel_id FROM route_channels WHERE route_id = rt.id)
    ) END ASC NULLS LAST,
  rt.name
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: RoutesOpsTableCount :one
SELECT COUNT(*) AS total
FROM routes rt
WHERE (sqlc.narg('status')::text IS NULL OR rt.status = sqlc.narg('status')::text)
  AND (sqlc.narg('search')::text IS NULL OR rt.name ILIKE '%' || sqlc.narg('search')::text || '%');

-- name: RouteOpsDetail :one
-- RouteOpsDetail 单线路抽屉概览：归因请求指标 + fallback + 无可用渠道。
WITH attributed AS (
    SELECT
        r.status,
        r.error_code,
        r.started_at,
        r.completed_at,
        (SELECT COUNT(*) FROM request_attempts a WHERE a.request_record_id = r.id) AS attempt_count
    FROM request_records r
    JOIN api_keys ak ON ak.id = r.api_key_id
    WHERE ak.route_id = sqlc.arg('route_id')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
)
SELECT
    COUNT(*) FILTER (WHERE status IN ('succeeded', 'failed')) AS request_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS request_succeeded,
    COUNT(*) FILTER (WHERE status = 'succeeded' AND attempt_count > 1) AS fallback_total,
    COUNT(*) FILTER (WHERE error_code IN ('no_available_channel', 'routing_no_available_channel')) AS no_channel_total,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p50,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    (SELECT rt.status FROM routes rt WHERE rt.id = sqlc.arg('route_id')) AS route_status
FROM attributed;

-- name: RouteOpsReachableModels :many
-- RouteOpsReachableModels 线路可达模型（有启用绑定且可解析成本的 distinct 模型；DEC-031：绝对覆盖 OR 基准价+价格倍率）。
SELECT
    m.model_id,
    m.display_name
FROM models m
JOIN channel_models cm ON cm.model_id = m.id AND cm.status = 'enabled'
JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled'
JOIN routes rt ON rt.id = sqlc.arg('route_id')
WHERE (
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
AND cm.channel_id IN (SELECT channel_id FROM route_channels WHERE route_id = rt.id)
GROUP BY m.id, m.model_id, m.display_name
ORDER BY m.model_id
LIMIT 500;

-- name: RouteOpsChannelPool :many
-- RouteOpsChannelPool 线路渠道池成员 + 渠道健康（抽屉渠道池 Tab）。
SELECT
    c.id AS channel_id,
    c.name AS channel_name,
    c.status AS channel_status,
    c.priority,
    p.name AS provider_name
FROM route_channels rc
JOIN channels c ON c.id = rc.channel_id
JOIN providers p ON p.id = c.provider_id
WHERE rc.route_id = sqlc.arg('route_id')
ORDER BY c.priority, c.id;

-- name: RouteOpsBoundUsers :many
-- RouteOpsBoundUsers 拥有「绑定本线路的 API Key」的用户（去重，抽屉绑定 Tab，P0）。
SELECT DISTINCT u.id, u.email, u.display_name
FROM users u
JOIN api_keys k ON k.user_id = u.id
WHERE k.route_id = sqlc.arg('route_id')
ORDER BY u.id
LIMIT 200;

-- name: RouteRuntimePool :many
-- RouteRuntimePool returns every explicitly bound channel plus DB hard-filter facts.
SELECT
    rt.id AS route_id,
    rt.mode,
    rt.status AS route_status,
    rt.price_ratio,
    c.id AS channel_id,
    c.name AS channel_name,
    c.status AS channel_status,
    c.credential_valid,
    (c.credential <> '')::boolean AS has_credential,
    (pe.base_url <> '')::boolean AS has_base_url,
    c.protocol,
    c.adapter_key,
    c.priority,
    c.tpm_limit,
    c.concurrency_limit,
    c.config_revision AS channel_config_revision,
    c.admission_limits_revision AS channel_admission_limits_revision,
    pe.id AS provider_origin_id,
    pe.name AS provider_origin_name,
    pe.status AS provider_origin_status,
    pe.base_url AS provider_origin_base_url,
    pe.base_url_revision AS provider_origin_base_url_revision,
    pe.status_revision AS provider_origin_status_revision,
    p.id AS provider_id,
    p.name AS provider_name,
    p.status AS provider_status,
    COALESCE(m.id, 0)::bigint AS model_db_id,
    (m.id IS NOT NULL)::boolean AS model_exists,
    COALESCE(m.status, '')::text AS model_status,
    COALESCE(cm.status, '')::text AS binding_status,
    (base.id IS NOT NULL)::boolean AS has_model_price,
    COALESCE((cost.id IS NOT NULL OR mult.id IS NOT NULL), false)::boolean AS has_channel_cost,
    COALESCE(base.id, 0)::bigint AS model_price_id,
    COALESCE(base.currency, '')::text AS base_currency,
    COALESCE(base.pricing_unit, '')::text AS base_pricing_unit,
    base.uncached_input_price,
    base.cache_read_input_price,
    base.cache_write_5m_input_price,
    base.cache_write_1h_input_price,
    base.cache_write_30m_input_price,
    base.output_price,
    base.reasoning_output_price,
    COALESCE(cost.id, 0)::bigint AS channel_price_id,
    COALESCE(cost.currency, '')::text AS cost_currency,
    COALESCE(cost.pricing_unit, '')::text AS cost_pricing_unit,
    cost.uncached_input_cost,
    cost.cache_read_input_cost,
    cost.cache_write_5m_input_cost,
    cost.cache_write_1h_input_cost,
    cost.cache_write_30m_input_cost,
    cost.output_cost,
    cost.reasoning_output_cost,
    COALESCE(mult.id, 0)::bigint AS channel_cost_multiplier_id,
    mult.multiplier AS cost_multiplier,
    COALESCE(recharge.id, 0)::bigint AS channel_recharge_factor_id,
    recharge.factor AS recharge_factor
FROM routes rt
JOIN route_channels rc ON rc.route_id = rt.id
JOIN channels c ON c.id = rc.channel_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_origins pe ON pe.id = c.provider_origin_id
LEFT JOIN models m
  ON NULLIF(sqlc.arg(model_id)::text, '') IS NOT NULL
 AND m.model_id = sqlc.arg(model_id)::text
LEFT JOIN channel_models cm ON cm.channel_id = c.id AND cm.model_id = m.id
LEFT JOIN LATERAL (
    SELECT mp.id, mp.currency, mp.pricing_unit,
           mp.uncached_input_price, mp.cache_read_input_price,
           mp.cache_write_5m_input_price, mp.cache_write_1h_input_price,
           mp.cache_write_30m_input_price, mp.output_price, mp.reasoning_output_price
    FROM model_prices mp
    WHERE mp.model_id = m.id
      AND mp.status = 'enabled'
      AND mp.effective_from <= sqlc.arg(at_time)
      AND (mp.effective_to IS NULL OR mp.effective_to > sqlc.arg(at_time))
    ORDER BY mp.effective_from DESC, mp.id DESC
    LIMIT 1
) base ON TRUE
LEFT JOIN LATERAL (
    SELECT cp.id, cp.currency, cp.pricing_unit,
           cp.uncached_input_cost, cp.cache_read_input_cost,
           cp.cache_write_5m_input_cost, cp.cache_write_1h_input_cost,
           cp.cache_write_30m_input_cost, cp.output_cost, cp.reasoning_output_cost
    FROM channel_prices cp
    WHERE cp.channel_id = c.id
      AND cp.model_id = m.id
      AND cp.status = 'enabled'
      AND cp.effective_from <= sqlc.arg(at_time)
      AND (cp.effective_to IS NULL OR cp.effective_to > sqlc.arg(at_time))
    ORDER BY cp.effective_from DESC, cp.id DESC
    LIMIT 1
) cost ON TRUE
LEFT JOIN LATERAL (
    SELECT ccm.id, ccm.multiplier
    FROM channel_cost_multipliers ccm
    WHERE ccm.channel_id = c.id
      AND (ccm.model_id = m.id OR ccm.model_id IS NULL)
      AND ccm.status = 'enabled'
      AND ccm.effective_from <= sqlc.arg(at_time)
      AND (ccm.effective_to IS NULL OR ccm.effective_to > sqlc.arg(at_time))
    ORDER BY (ccm.model_id IS NULL) ASC, ccm.effective_from DESC, ccm.id DESC
    LIMIT 1
) mult ON TRUE
LEFT JOIN LATERAL (
    SELECT crf.id, crf.factor
    FROM channel_recharge_factors crf
    WHERE crf.channel_id = c.id
      AND crf.status = 'enabled'
      AND crf.effective_from <= sqlc.arg(at_time)
      AND (crf.effective_to IS NULL OR crf.effective_to > sqlc.arg(at_time))
    ORDER BY crf.effective_from DESC, crf.id DESC
    LIMIT 1
) recharge ON TRUE
WHERE rt.id = sqlc.arg(route_id)
ORDER BY c.priority, c.id;

-- name: RouteRuntimeChannelStats :many
-- Recent final selections and fallback attempts for channels in one explicit route pool.
SELECT
    rc.channel_id,
    COUNT(DISTINCT rr.id) FILTER (
        WHERE rr.created_at >= sqlc.arg(observed_at)::timestamptz - interval '1 minute'
          AND rr.final_channel_id = rc.channel_id
    )::bigint AS selected_1m,
    COUNT(DISTINCT rr.id) FILTER (
        WHERE rr.created_at >= sqlc.arg(observed_at)::timestamptz - interval '5 minutes'
          AND rr.final_channel_id = rc.channel_id
    )::bigint AS selected_5m,
    COUNT(a.id) FILTER (
        WHERE a.created_at >= sqlc.arg(observed_at)::timestamptz - interval '1 minute'
          AND a.channel_id = rc.channel_id
          AND a.attempt_index > 0
    )::bigint AS fallback_1m
FROM route_channels rc
LEFT JOIN request_records rr
  ON rr.route_id = rc.route_id
 AND rr.created_at >= sqlc.arg(observed_at)::timestamptz - interval '5 minutes'
LEFT JOIN request_attempts a
  ON a.request_record_id = rr.id
 AND a.channel_id = rc.channel_id
WHERE rc.route_id = sqlc.arg(route_id)
GROUP BY rc.channel_id
ORDER BY rc.channel_id;

-- name: RouteOpsBoundKeys :many
-- RouteOpsBoundKeys 绑定本线路的 API Key（抽屉绑定 Tab，P0）。状态由时间戳派生。
SELECT k.id, k.name, k.user_id, k.disabled_at, k.revoked_at, k.expires_at
FROM api_keys k
WHERE k.route_id = sqlc.arg('route_id')
ORDER BY k.id
LIMIT 200;

-- name: RouteOpsPerformanceTimeseries :many
WITH attributed AS (
    SELECT r.created_at, r.status, r.started_at, r.completed_at
    FROM request_records r
    JOIN api_keys ak ON ak.id = r.api_key_id
    WHERE ak.route_id = sqlc.arg('route_id')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
)
SELECT
    date_trunc(sqlc.arg('unit')::text, created_at)::timestamptz AS bucket,
    COUNT(*) FILTER (WHERE status IN ('succeeded', 'failed')) AS request_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS request_succeeded,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95
FROM attributed
GROUP BY bucket
ORDER BY bucket;

-- name: RouteOpsModels :many
-- RouteOpsModels 本线路下各模型表现（抽屉模型 Tab，精简 §1.8）。
WITH attributed AS (
    SELECT r.requested_model_id, r.status
    FROM request_records r
    JOIN api_keys ak ON ak.id = r.api_key_id
    WHERE ak.route_id = sqlc.arg('route_id')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
)
SELECT
    requested_model_id AS model_id,
    COUNT(*) FILTER (WHERE status IN ('succeeded', 'failed')) AS request_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS request_succeeded
FROM attributed
GROUP BY requested_model_id
ORDER BY request_total DESC
LIMIT 50;

-- name: RouteOpsRequests :many
-- RouteOpsRequests 本线路最近请求（抽屉请求 Tab，分页）。
SELECT
    r.request_id,
    r.created_at,
    r.status,
    r.requested_model_id,
    r.final_channel_id,
    CASE WHEN r.completed_at IS NOT NULL THEN (EXTRACT(EPOCH FROM (r.completed_at - r.started_at)) * 1000)::float8 END AS latency_ms
FROM request_records r
JOIN api_keys ak ON ak.id = r.api_key_id
WHERE ak.route_id = sqlc.arg('route_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY r.created_at DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: RouteOpsRequestsCount :one
SELECT COUNT(*) AS total
FROM request_records r
JOIN api_keys ak ON ak.id = r.api_key_id
WHERE ak.route_id = sqlc.arg('route_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz);
