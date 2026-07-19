-- name: ModelExistsByID :one
-- ModelExistsByID 判断指定对外模型 ID 是否存在且启用。
SELECT EXISTS (
    SELECT 1
    FROM models m
    WHERE m.model_id = sqlc.arg(requested_model_id)
    AND m.status = 'enabled'
) AS exists;

-- name: ListAvailableModelsForUser :many
-- ListAvailableModelsForUser 列出指定用户在 API Key 当前线路内可见且可路由的模型，并附带该模型已声明的
-- cap-tags（能力架构 Layer 2，support_level<>'unsupported' 的 capability_key 去重升序）。
-- cap-tags 取模型级声明，不下钻到 channel override（不向客户暴露 channel 维度收紧）。
-- 未声明任何能力的模型 capability_keys 为空数组（unprovisioned）。
WITH user_scope AS (
    SELECT sqlc.arg(user_id)::BIGINT AS user_id
),
user_policy_mode AS (
    SELECT EXISTS (
        SELECT 1
        FROM user_model_policies ump
        JOIN user_scope us ON us.user_id = ump.user_id
        WHERE ump.visibility = 'allowed'
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
JOIN route_channels rc ON rc.channel_id = c.id AND rc.route_id = sqlc.arg(route_id)
JOIN routes rt ON rt.id = rc.route_id AND rt.status = 'enabled'
LEFT JOIN model_capabilities mc ON mc.model_id = m.id
JOIN user_scope us ON us.user_id > 0
WHERE m.status = 'enabled'
    AND cm.status = 'enabled'
    AND c.status = 'enabled'
    AND c.credential_valid
    AND p.status = 'enabled'
    AND EXISTS (
        SELECT 1
        FROM model_prices mp
        WHERE mp.model_id = m.id
          AND mp.status = 'enabled'
          AND mp.effective_from <= now()
          AND (mp.effective_to IS NULL OR mp.effective_to > now())
    )
    AND (
        EXISTS (
            SELECT 1
            FROM channel_prices cp
            WHERE cp.channel_id = c.id
              AND cp.model_id = m.id
              AND cp.status = 'enabled'
              AND cp.effective_from <= now()
              AND (cp.effective_to IS NULL OR cp.effective_to > now())
        )
        OR EXISTS (
            SELECT 1
            FROM channel_cost_multipliers ccm
            WHERE ccm.channel_id = c.id
              AND (ccm.model_id = m.id OR ccm.model_id IS NULL)
              AND ccm.status = 'enabled'
              AND ccm.effective_from <= now()
              AND (ccm.effective_to IS NULL OR ccm.effective_to > now())
        )
    )
    AND NOT EXISTS (
        SELECT 1
        FROM user_model_policies denied
        JOIN user_scope us ON us.user_id = denied.user_id
        WHERE denied.model_id = m.id
            AND denied.visibility = 'denied'
    )
    AND (
        NOT (SELECT has_allow_list FROM user_policy_mode)
        OR EXISTS (
            SELECT 1
            FROM user_model_policies allowed
            JOIN user_scope us ON us.user_id = allowed.user_id
            WHERE allowed.model_id = m.id
                AND allowed.visibility = 'allowed'
        )
    )
GROUP BY m.id, m.model_id, m.display_name, m.owned_by
ORDER BY m.model_id ASC;
