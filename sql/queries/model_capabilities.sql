-- name: ListModelCapabilities :many
-- ListModelCapabilities 列出指定模型已声明的全部能力。
SELECT *
FROM model_capabilities
WHERE model_id = sqlc.arg(model_id)
ORDER BY capability_key ASC;

-- name: ListModelsByCapability :many
-- ListModelsByCapability 反查声明了指定能力的模型及其支持级别（cap-tags 与闸门用）。
SELECT *
FROM model_capabilities
WHERE capability_key = sqlc.arg(capability_key)
ORDER BY model_id ASC;

-- name: UpsertModelCapability :one
-- UpsertModelCapability 写入或覆盖模型能力声明（admin 与采纳用）。能力已去 source（阶段 14 Q4）。
INSERT INTO model_capabilities (
    model_id,
    capability_key,
    support_level,
    limits,
    updated_by
)
VALUES (
    sqlc.arg(model_id),
    sqlc.arg(capability_key),
    sqlc.arg(support_level),
    sqlc.arg(limits),
    sqlc.arg(updated_by)
)
ON CONFLICT (model_id, capability_key) DO UPDATE
SET support_level = excluded.support_level,
    limits = excluded.limits,
    updated_by = excluded.updated_by,
    updated_at = now()
RETURNING *;

-- name: DeleteModelCapability :exec
-- DeleteModelCapability 删除指定模型对某能力的声明（admin 手工撤销）。
DELETE FROM model_capabilities
WHERE model_id = sqlc.arg(model_id)
    AND capability_key = sqlc.arg(capability_key);

-- name: DeleteModelCapabilitiesByModel :exec
-- DeleteModelCapabilitiesByModel 清空某模型的全部能力声明（「从目录刷新」整体覆盖前置）。
DELETE FROM model_capabilities
WHERE model_id = sqlc.arg(model_id);
