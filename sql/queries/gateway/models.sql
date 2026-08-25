-- name: ModelExistsByID :one
-- ModelExistsByID 判断指定对外模型 ID 是否存在且启用。
SELECT EXISTS (
    SELECT 1
    FROM models m
    WHERE m.model_id = sqlc.arg(requested_model_id)
    AND m.status = 'enabled'
) AS exists;

-- name: ListAvailableModels :many
-- ListAvailableModels 列出所有启用模型，并附带该模型已声明的 cap-tags
-- （能力架构 Layer 2，support_level<>'unsupported' 的 capability_key 去重升序）。
-- cap-tags 取模型级声明，不下钻到 channel override（不向客户暴露 channel 维度收紧）。
-- 未声明任何能力的模型 capability_keys 为空数组（unprovisioned）。
--
-- 不做用户级过滤：模型 enabled 的前置条件是「至少有一条可用渠道能供」（供给不变量），
-- 因此列出即可调用。协议维度由 ListModelProtocols 单独提供，不在此收窄。
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
LEFT JOIN model_capabilities mc ON mc.model_id = m.id
WHERE m.status = 'enabled'
GROUP BY m.id, m.model_id, m.display_name, m.owned_by
ORDER BY m.model_id ASC;

-- name: ListModelProtocols :many
-- ListModelProtocols 汇总每个启用模型当前实际可用的入口协议：
-- 取其所有可用渠道 protocols 的并集。协议信息不落库，恒等于实际供给能力，
-- 不存在「声明支持但调不通」的状态。
SELECT
    m.model_id,
    array_agg(DISTINCT proto ORDER BY proto)::text[] AS protocols
FROM models m
JOIN channel_models cm ON cm.model_id = m.id AND cm.status = 'enabled'
JOIN channels c ON c.id = cm.channel_id AND c.status = 'enabled' AND c.credential_valid
JOIN providers p ON p.id = c.provider_id AND p.status = 'enabled'
CROSS JOIN LATERAL unnest(c.protocols) AS proto
WHERE m.status = 'enabled'
GROUP BY m.model_id
ORDER BY m.model_id ASC;
