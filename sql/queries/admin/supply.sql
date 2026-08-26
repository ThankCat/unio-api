-- 模型供给状态归属。
--
-- 供给的根是 Model：模型 enabled 即对外可售，不再有「线路售卖意图」这一层。
-- 由此产生一条不变量：**enabled 的模型必定至少有一条可用渠道能供**。
-- 它需要双向守住：
--   减法侧——停用/解绑渠道前，检查哪些模型会失去全部供给（本文件的 Losing* 查询）；
--   加法侧——启用模型前，检查是否已有供给（ModelHasConfiguredSupply / ModelHasRuntimeSupply）。
--
-- 「配置支撑」只看 enabled 的 Channel-Model Binding，不读 Channel/Provider 实体状态；
-- 「运行候选」额外要求 Channel、Provider enabled 且凭据有效。前者管配置意图，后者管此刻能不能打通。
-- 影响计算查询必须在 LockModelsForSupplyChange 取得的 Model 行锁内执行。

-- name: LockModelsForSupplyChange :many
-- LockModelsForSupplyChange 按 model_id 升序锁定 Model 行，作为供给变更的串行化点。
-- 收缩侧（停用/解除 Binding、停用 Channel、停用 Model）与扩张侧（启用 Binding、启用 Model）
-- 都必须先取得该锁再计算或校验。
SELECT id FROM models
WHERE id = ANY(sqlc.arg(model_ids)::bigint[])
ORDER BY id
FOR UPDATE;

-- name: ListEnabledBindingModelIDsForChannel :many
-- ListEnabledBindingModelIDsForChannel 列出某 Channel 全部 enabled Binding 的模型（升序），
-- 供 Channel 停用/归档前聚合锁定与影响计算。
SELECT DISTINCT cm.model_id
FROM channel_models cm
WHERE cm.channel_id = sqlc.arg(channel_id) AND cm.status = 'enabled'
ORDER BY cm.model_id;

-- name: ListModelsLosingConfiguredSupply :many
-- ListModelsLosingConfiguredSupply 返回「排除目标 Channel 上将失效的 enabled Binding 后，
-- 失去最后一条配置支撑」的 enabled 模型。model_id 为空表示该 Channel 全部 Binding 同时失效，
-- 否则只针对单条 Binding（停用/解除）。不读 Channel/Provider 实体状态。
SELECT m.id AS model_id,
       m.model_id AS public_model_id,
       m.display_name AS model_display_name
FROM models m
WHERE m.status = 'enabled'
  AND (sqlc.narg(model_id)::bigint IS NULL OR m.id = sqlc.narg(model_id)::bigint)
  AND EXISTS (
      SELECT 1
      FROM channel_models cm
      WHERE cm.channel_id = sqlc.arg(channel_id)
        AND cm.model_id = m.id
        AND cm.status = 'enabled'
  )
  AND NOT EXISTS (
      SELECT 1
      FROM channel_models cm2
      WHERE cm2.model_id = m.id
        AND cm2.channel_id <> sqlc.arg(channel_id)
        AND cm2.status = 'enabled'
  )
ORDER BY m.id;

-- name: ListModelsLosingRuntimeSupply :many
-- ListModelsLosingRuntimeSupply 返回暂停目标 Channel 后，按 Channel/Provider 当前启用状态
-- 已无其他运行候选的 enabled 模型。只用于结果预览，不改变配置支撑定义。
SELECT m.id AS model_id,
       m.model_id AS public_model_id,
       m.display_name AS model_display_name
FROM models m
WHERE m.status = 'enabled'
  AND EXISTS (
      SELECT 1
      FROM channel_models cm
      JOIN channels c ON c.id = cm.channel_id
      JOIN providers p ON p.id = c.provider_id
      WHERE cm.model_id = m.id
        AND c.id = sqlc.arg(channel_id)
        AND cm.status = 'enabled'
        AND c.status = 'enabled'
        AND p.status = 'enabled'
  )
  AND NOT EXISTS (
      SELECT 1
      FROM channel_models cm2
      JOIN channels c2 ON c2.id = cm2.channel_id
      JOIN providers p2 ON p2.id = c2.provider_id
      WHERE cm2.model_id = m.id
        AND c2.id <> sqlc.arg(channel_id)
        AND cm2.status = 'enabled'
        AND c2.status = 'enabled'
        AND p2.status = 'enabled'
        AND c2.credential_valid
  )
ORDER BY m.id;

-- name: CountOtherEnabledBindingsForModel :one
-- CountOtherEnabledBindingsForModel 全局判断：排除目标 Channel 后，该 Model 是否仍有任意
-- enabled Binding。只读 Binding 行状态，不读 Channel/Provider 实体状态。
SELECT COUNT(*) AS remaining
FROM channel_models cm
WHERE cm.model_id = sqlc.arg(model_id)
  AND cm.status = 'enabled'
  AND cm.channel_id <> sqlc.arg(exclude_channel_id);

-- name: ModelHasConfiguredSupply :one
-- ModelHasConfiguredSupply 判断该 Model 是否存在任意 enabled Binding（配置意图层面的供给）。
SELECT EXISTS (
    SELECT 1
    FROM channel_models cm
    WHERE cm.model_id = sqlc.arg(model_id)
      AND cm.status = 'enabled'
) AS supported;

-- name: ModelHasRuntimeSupply :one
-- ModelHasRuntimeSupply 判断该 Model 此刻是否真的能打通：需要 Binding、Channel、Provider
-- 三级 enabled、凭据有效，且渠道成本可解析（有绝对覆盖或价格倍率，否则不参与计费也就进不了候选）。
-- 这是启用模型的前置条件，用于守住「enabled 即可调用」的不变量。
--
-- 基准价条件必须与 FindModelCandidates 取 base 的那个 INNER JOIN LATERAL 对齐：
-- 那边没有生效基准价就没有候选。少了这一条，一个没配价的模型能被成功 enable、
-- 出现在 /v1/models 里，却对每次调用都返回 404。
-- 售价本身不用单独判：ck_model_prices_sale_configured 保证存在价格行即可解析出售价。
SELECT EXISTS (
    SELECT 1
    FROM channel_models cm
    JOIN channels c ON c.id = cm.channel_id
    JOIN providers p ON p.id = c.provider_id
    WHERE cm.model_id = sqlc.arg(model_id)
      AND cm.status = 'enabled'
      AND c.status = 'enabled'
      AND c.credential_valid
      AND p.status = 'enabled'
      AND EXISTS (
          SELECT 1 FROM model_prices mp
          WHERE mp.model_id = cm.model_id
            AND mp.status = 'enabled'
            AND mp.effective_from <= now()
            AND (mp.effective_to IS NULL OR mp.effective_to > now())
      )
      AND (
          EXISTS (
              SELECT 1 FROM channel_prices cp
              WHERE cp.channel_id = c.id AND cp.model_id = cm.model_id AND cp.status = 'enabled'
          )
          OR EXISTS (
              SELECT 1 FROM channel_cost_multipliers ccm
              WHERE ccm.channel_id = c.id
                AND (ccm.model_id = cm.model_id OR ccm.model_id IS NULL)
                AND ccm.status = 'enabled'
          )
      )
) AS supported;

-- name: ListModelRuntimeProtocols :many
-- ListModelRuntimeProtocols 返回该 Model 当前可用渠道覆盖的入口协议集合。
-- 协议不落库，恒等于实际供给能力。
SELECT DISTINCT proto AS ingress_protocol
FROM channel_models cm
JOIN channels c ON c.id = cm.channel_id
JOIN providers p ON p.id = c.provider_id
CROSS JOIN LATERAL unnest(c.protocols) AS proto
WHERE cm.model_id = sqlc.arg(model_id)
  AND cm.status = 'enabled'
  AND c.status = 'enabled'
  AND c.credential_valid
  AND p.status = 'enabled'
ORDER BY proto;

-- name: ModelDisableImpactCounts :one
-- ModelDisableImpactCounts 统计 Model 全局暂停影响范围内的 enabled Binding 及其 Channel/Provider 数。
SELECT COUNT(*) AS enabled_bindings,
       COUNT(DISTINCT c.id) AS channels,
       COUNT(DISTINCT c.provider_id) AS providers
FROM channel_models cm
JOIN channels c ON c.id = cm.channel_id
WHERE cm.model_id = sqlc.arg(model_id) AND cm.status = 'enabled';

-- name: DisableModelSupply :execrows
-- DisableModelSupply 暂停 Model 行并记录直接原因；供给失去最后支撑时由调用方连带执行。
UPDATE models
SET status = 'disabled',
    disabled_reason = sqlc.arg(reason),
    disabled_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'enabled';

-- name: EnableModelSupply :execrows
-- EnableModelSupply 启用 Model 行并清空停用原因。调用方必须先在同一事务、同一 Model 锁内
-- 确认 ModelHasRuntimeSupply 为真，否则会破坏「enabled 即可调用」的不变量。
UPDATE models
SET status = 'enabled',
    disabled_reason = NULL,
    disabled_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'disabled';

-- name: ListDisabledModelsRecoverable :many
-- ListDisabledModelsRecoverable 列出因供给中断而停用、如今供给已恢复的模型（批量恢复入口）。
-- 只列 binding_disabled / channel_disabled：管理员手动下架的不该被「恢复」列表打扰。
SELECT m.id AS model_id,
       m.model_id AS public_model_id,
       m.display_name,
       m.disabled_reason,
       m.disabled_at
FROM models m
WHERE m.status = 'disabled'
  AND m.disabled_reason IN ('binding_disabled', 'channel_disabled')
  AND EXISTS (
      SELECT 1
      FROM channel_models cm
      JOIN channels c ON c.id = cm.channel_id
      JOIN providers p ON p.id = c.provider_id
      WHERE cm.model_id = m.id
        AND cm.status = 'enabled'
        AND c.status = 'enabled'
        AND c.credential_valid
        AND p.status = 'enabled'
  )
ORDER BY m.model_id;

-- name: CountEnabledBindingsByChannel :one
-- CountEnabledBindingsByChannel 归档前置：archived Channel 下不得存在 enabled Binding。
SELECT COUNT(*) AS enabled_bindings
FROM channel_models
WHERE channel_id = sqlc.arg(channel_id) AND status = 'enabled';

-- name: ListSupplyCandidatesForChannels :many
-- ListSupplyCandidatesForChannels 按给定 Channel 集合计算「模型 × 协议」供给覆盖：
-- enabled Binding 去重，协议来自渠道的 protocols 数组展开。不读 Model 当前状态。
SELECT cm.model_id,
       m.model_id AS public_model_id,
       m.display_name,
       m.status AS model_status,
       proto AS ingress_protocol,
       COUNT(DISTINCT c.id) AS supporting_channels
FROM channels c
JOIN channel_models cm ON cm.channel_id = c.id AND cm.status = 'enabled'
JOIN models m ON m.id = cm.model_id
CROSS JOIN LATERAL unnest(c.protocols) AS proto
WHERE c.id = ANY(sqlc.arg(channel_ids)::bigint[])
GROUP BY cm.model_id, m.model_id, m.display_name, m.status, proto
ORDER BY m.model_id, proto;

-- name: ListModelsWithoutRuntimeSupply :many
-- ListModelsWithoutRuntimeSupply 列出 enabled 但当前没有任何运行候选的模型。
-- 这是不变量被打破的信号（例如渠道凭据集体失效），供后台告警，不自动改状态。
SELECT m.id AS model_id,
       m.model_id AS public_model_id,
       m.display_name
FROM models m
WHERE m.status = 'enabled'
  AND NOT EXISTS (
      SELECT 1
      FROM channel_models cm
      JOIN channels c ON c.id = cm.channel_id
      JOIN providers p ON p.id = c.provider_id
      WHERE cm.model_id = m.id
        AND cm.status = 'enabled'
        AND c.status = 'enabled'
        AND c.credential_valid
        AND p.status = 'enabled'
  )
ORDER BY m.model_id;
