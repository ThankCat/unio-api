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
-- UpsertModelCapability 写入或覆盖模型能力声明（同步与 admin 用）。
INSERT INTO model_capabilities (
    model_id,
    capability_key,
    support_level,
    limits,
    source,
    updated_by
)
VALUES (
    sqlc.arg(model_id),
    sqlc.arg(capability_key),
    sqlc.arg(support_level),
    sqlc.arg(limits),
    sqlc.arg(source),
    sqlc.arg(updated_by)
)
ON CONFLICT (model_id, capability_key) DO UPDATE
SET support_level = excluded.support_level,
    limits = excluded.limits,
    source = excluded.source,
    updated_by = excluded.updated_by,
    updated_at = now()
RETURNING *;

-- name: DeleteModelCapability :exec
-- DeleteModelCapability 删除指定模型对某能力的声明（admin 手工撤销）。
DELETE FROM model_capabilities
WHERE model_id = sqlc.arg(model_id)
    AND capability_key = sqlc.arg(capability_key);
