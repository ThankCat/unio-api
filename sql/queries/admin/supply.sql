-- 模型供给与线路售卖联动（ADR-0018）。
-- 结构支撑定义：Route 池内、同 ingress protocol、enabled Channel 承载的 enabled Channel-Model Binding。
-- 本文件的影响计算查询必须在「结构支撑串行化」的 Model 行锁内执行（LockModelsForSupplyChange 先行）。

-- name: LockModelsForSupplyChange :many
-- LockModelsForSupplyChange 按 model_id 升序锁定 Model 行，作为结构支撑图的串行化点。
-- 收缩侧（停用/解除 Binding、停用 Channel、删除 Route Channel、停用 Model）与扩张侧
-- （启用 Binding、Route 保存创建/启用 Offering、批量恢复）都必须先取得该锁再计算或校验。
SELECT id FROM models
WHERE id = ANY(sqlc.arg(model_ids)::bigint[])
ORDER BY id
FOR UPDATE;

-- name: ListEnabledBindingModelIDsForChannel :many
-- ListEnabledBindingModelIDsForChannel 列出某 Channel 全部 enabled Binding 的模型（升序），
-- 供 Channel 实体停用/归档前聚合锁定与影响计算。
SELECT DISTINCT cm.model_id
FROM channel_models cm
WHERE cm.channel_id = sqlc.arg(channel_id) AND cm.status = 'enabled'
ORDER BY cm.model_id;

-- name: ListOfferingsLosingSupport :many
-- ListOfferingsLosingSupport 返回「排除目标 Channel 上将失效的 enabled Binding 后，失去最后
-- 结构支撑」的 enabled Offering。model_id 为空表示该 Channel 全部 enabled Binding 同时失效
-- （Channel 实体停用），否则只针对单条 Binding（停用/解除）。
-- 反查覆盖所有未硬删除 Route（含 disabled 与 archived），确认信息按 Route 状态分组展示。
SELECT o.route_id,
       rt.name AS route_name,
       rt.status AS route_status,
       o.model_id,
       m.model_id AS public_model_id,
       m.display_name AS model_display_name,
       o.ingress_protocol
FROM route_model_offerings o
JOIN routes rt ON rt.id = o.route_id
JOIN models m ON m.id = o.model_id
WHERE o.status = 'enabled'
  AND (sqlc.narg(model_id)::bigint IS NULL OR o.model_id = sqlc.narg(model_id)::bigint)
  AND EXISTS (
      SELECT 1
      FROM route_channels rc
      JOIN channels c ON c.id = rc.channel_id
      JOIN channel_models cm ON cm.channel_id = c.id AND cm.model_id = o.model_id
      WHERE rc.route_id = o.route_id
        AND c.id = sqlc.arg(channel_id)
        AND c.status = 'enabled'
        AND c.protocol = o.ingress_protocol
        AND cm.status = 'enabled'
  )
  AND NOT EXISTS (
      SELECT 1
      FROM route_channels rc2
      JOIN channels c2 ON c2.id = rc2.channel_id
      JOIN channel_models cm2 ON cm2.channel_id = c2.id AND cm2.model_id = o.model_id
      WHERE rc2.route_id = o.route_id
        AND c2.id <> sqlc.arg(channel_id)
        AND c2.status = 'enabled'
        AND c2.protocol = o.ingress_protocol
        AND cm2.status = 'enabled'
  )
ORDER BY o.model_id, o.route_id, o.ingress_protocol;

-- name: CountOtherEnabledBindingsForModel :one
-- CountOtherEnabledBindingsForModel 全局判断：排除目标 Channel 后，该 Model 是否仍有任意
-- enabled Binding。只读取 Binding 行状态，不读取 Channel/Provider 实体状态（ADR-0018）。
SELECT COUNT(*) AS remaining
FROM channel_models cm
WHERE cm.model_id = sqlc.arg(model_id)
  AND cm.status = 'enabled'
  AND cm.channel_id <> sqlc.arg(exclude_channel_id);

-- name: ListEnabledOfferingsForModel :many
-- ListEnabledOfferingsForModel 列出该 Model 全部 enabled Offering（Model 手动/自动停用预览）。
SELECT o.route_id,
       rt.name AS route_name,
       rt.status AS route_status,
       o.model_id,
       m.model_id AS public_model_id,
       m.display_name AS model_display_name,
       o.ingress_protocol
FROM route_model_offerings o
JOIN routes rt ON rt.id = o.route_id
JOIN models m ON m.id = o.model_id
WHERE o.model_id = sqlc.arg(model_id) AND o.status = 'enabled'
ORDER BY o.route_id, o.ingress_protocol;

-- name: ModelDisableImpactCounts :one
-- ModelDisableImpactCounts 统计 Model 手动停用将级联停用的 enabled Binding 及其 Channel/Provider 数。
SELECT COUNT(*) AS enabled_bindings,
       COUNT(DISTINCT c.id) AS channels,
       COUNT(DISTINCT c.provider_id) AS providers
FROM channel_models cm
JOIN channels c ON c.id = cm.channel_id
WHERE cm.model_id = sqlc.arg(model_id) AND cm.status = 'enabled';

-- name: DisableRouteModelOffering :execrows
-- DisableRouteModelOffering 把一条 enabled Offering 置为 disabled 并记录直接原因。
UPDATE route_model_offerings
SET status = 'disabled', disabled_reason = sqlc.arg(reason), disabled_at = now(), updated_at = now()
WHERE route_id = sqlc.arg(route_id)
  AND model_id = sqlc.arg(model_id)
  AND ingress_protocol = sqlc.arg(ingress_protocol)
  AND status = 'enabled';

-- name: EnableRouteModelOffering :exec
-- EnableRouteModelOffering 创建或重新启用一条 Offering；重新启用时清空停用原因和时间（ADR-0018）。
-- 结构支撑必须由调用方在同一事务、同一 Model 锁内先行校验。
INSERT INTO route_model_offerings (route_id, model_id, ingress_protocol)
VALUES (sqlc.arg(route_id), sqlc.arg(model_id), sqlc.arg(ingress_protocol))
ON CONFLICT (route_id, model_id, ingress_protocol)
DO UPDATE SET status = 'enabled', disabled_reason = NULL, disabled_at = NULL, updated_at = now();

-- name: DisableEnabledOfferingsForModel :execrows
-- DisableEnabledOfferingsForModel 把该 Model 全部 enabled Offering 置 disabled（Model 停用级联）。
UPDATE route_model_offerings
SET status = 'disabled', disabled_reason = 'model_disabled', disabled_at = now(), updated_at = now()
WHERE model_id = sqlc.arg(model_id) AND status = 'enabled';

-- name: DisableEnabledBindingsForModel :execrows
-- DisableEnabledBindingsForModel 把该 Model 全部 enabled Binding 置 disabled（Model 停用级联，
-- 维持「Model disabled 不得存在 enabled Binding」不变量）。
UPDATE channel_models
SET status = 'disabled', updated_at = now()
WHERE model_id = sqlc.arg(model_id) AND status = 'enabled';

-- name: DisableModelSupply :execrows
-- DisableModelSupply 停用 Model 行（手动停用或全局最后供给自动停用），只改状态不动其他资料。
UPDATE models
SET status = 'disabled', updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'enabled';

-- name: CountEnabledBindingsByChannel :one
-- CountEnabledBindingsByChannel 归档前置：archived Channel 下不得存在 enabled Binding。
SELECT COUNT(*) AS enabled_bindings
FROM channel_models
WHERE channel_id = sqlc.arg(channel_id) AND status = 'enabled';

-- name: ListOfferingCandidatesForChannels :many
-- ListOfferingCandidatesForChannels 按给定 Channel 池计算可售卖的 Model+协议组合候选：
-- enabled Channel 承载的 enabled Binding 且 Model enabled；不读取 breaker、容量、凭据或价格。
SELECT cm.model_id,
       m.model_id AS public_model_id,
       m.display_name,
       c.protocol AS ingress_protocol,
       COUNT(DISTINCT c.id) AS supporting_channels
FROM channels c
JOIN channel_models cm ON cm.channel_id = c.id AND cm.status = 'enabled'
JOIN models m ON m.id = cm.model_id AND m.status = 'enabled'
WHERE c.id = ANY(sqlc.arg(channel_ids)::bigint[])
  AND c.status = 'enabled'
  AND c.protocol IN ('openai', 'anthropic')
GROUP BY cm.model_id, m.model_id, m.display_name, c.protocol
ORDER BY m.model_id, c.protocol;

-- name: ListRouteOfferingDetails :many
-- ListRouteOfferingDetails 返回线路全部 Offering（逐 Model+协议组合），含状态、原因与当前结构支撑。
SELECT o.model_id,
       m.model_id AS public_model_id,
       m.display_name,
       m.status AS model_status,
       o.ingress_protocol,
       o.status,
       o.disabled_reason,
       o.disabled_at,
       o.updated_at,
       EXISTS (
           SELECT 1
           FROM route_channels rc
           JOIN channels c ON c.id = rc.channel_id
           JOIN channel_models cm ON cm.channel_id = c.id AND cm.model_id = o.model_id
           WHERE rc.route_id = o.route_id
             AND c.status = 'enabled'
             AND c.protocol = o.ingress_protocol
             AND cm.status = 'enabled'
       ) AS support_available
FROM route_model_offerings o
JOIN models m ON m.id = o.model_id
WHERE o.route_id = sqlc.arg(route_id)
ORDER BY m.model_id, o.ingress_protocol;

-- name: ListRouteEnabledOfferingsUnsupportedByPool :many
-- ListRouteEnabledOfferingsUnsupportedByPool 返回该 Route 当前 enabled、但按给定（保存后最终）
-- Channel 池已无结构支撑的 Offering；Route 保存按最终池聚合评估（ADR-0018）。
SELECT o.model_id,
       m.model_id AS public_model_id,
       m.display_name,
       o.ingress_protocol
FROM route_model_offerings o
JOIN models m ON m.id = o.model_id
WHERE o.route_id = sqlc.arg(route_id)
  AND o.status = 'enabled'
  AND NOT EXISTS (
      SELECT 1
      FROM channels c
      JOIN channel_models cm ON cm.channel_id = c.id AND cm.model_id = o.model_id
      WHERE c.id = ANY(sqlc.arg(channel_ids)::bigint[])
        AND c.status = 'enabled'
        AND c.protocol = o.ingress_protocol
        AND cm.status = 'enabled'
  )
ORDER BY o.model_id, o.ingress_protocol;

-- name: OfferingComboSupportedByPool :one
-- OfferingComboSupportedByPool 校验一个 Model+协议组合能否被给定 Channel 池结构支撑，
-- 且 Model 全局 enabled（勾选/重新勾选售卖组合的保存校验）。
SELECT EXISTS (
    SELECT 1
    FROM channels c
    JOIN channel_models cm ON cm.channel_id = c.id
    JOIN models m ON m.id = cm.model_id AND m.status = 'enabled'
    WHERE c.id = ANY(sqlc.arg(channel_ids)::bigint[])
      AND c.status = 'enabled'
      AND c.protocol = sqlc.arg(ingress_protocol)
      AND cm.model_id = sqlc.arg(model_id)
      AND cm.status = 'enabled'
) AS supported;

-- name: ListDisabledOfferingsForModel :many
-- ListDisabledOfferingsForModel 按 Model 聚合列出 disabled Offering（批量恢复入口），
-- 附当前结构支撑是否已恢复；可按协议过滤。
SELECT o.route_id,
       rt.name AS route_name,
       rt.status AS route_status,
       o.ingress_protocol,
       o.disabled_reason,
       o.disabled_at,
       EXISTS (
           SELECT 1
           FROM route_channels rc
           JOIN channels c ON c.id = rc.channel_id
           JOIN channel_models cm ON cm.channel_id = c.id AND cm.model_id = o.model_id
           WHERE rc.route_id = o.route_id
             AND c.status = 'enabled'
             AND c.protocol = o.ingress_protocol
             AND cm.status = 'enabled'
       ) AS support_available
FROM route_model_offerings o
JOIN routes rt ON rt.id = o.route_id
WHERE o.model_id = sqlc.arg(model_id)
  AND o.status = 'disabled'
  AND (sqlc.narg(ingress_protocol)::text IS NULL OR o.ingress_protocol = sqlc.narg(ingress_protocol)::text)
ORDER BY rt.name, o.ingress_protocol;

-- name: OfferingSupportExists :one
-- OfferingSupportExists 校验某条 Offering 在其 Route 当前池内是否有结构支撑（批量恢复逐条重校验）。
SELECT EXISTS (
    SELECT 1
    FROM route_channels rc
    JOIN channels c ON c.id = rc.channel_id
    JOIN channel_models cm ON cm.channel_id = c.id
    WHERE rc.route_id = sqlc.arg(route_id)
      AND c.status = 'enabled'
      AND c.protocol = sqlc.arg(ingress_protocol)
      AND cm.model_id = sqlc.arg(model_id)
      AND cm.status = 'enabled'
) AS supported;
