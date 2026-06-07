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

-- name: LookupModelByModelID :one
-- LookupModelByModelID 按对外模型 ID 读取模型完整元数据（含能力架构 Layer 1 列）。
SELECT *
FROM models
WHERE model_id = sqlc.arg(model_id);

-- name: ListAvailableModelsForProject :many
-- ListAvailableModelsForProject 列出指定项目当前可见且可路由的模型。
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
SELECT DISTINCT
    m.id,
    m.model_id,
    m.display_name,
    m.owned_by
FROM models m
JOIN channel_models cm ON cm.model_id = m.id
JOIN channels c ON c.id = cm.channel_id
JOIN providers p ON p.id = c.provider_id
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
ORDER BY m.model_id ASC;
