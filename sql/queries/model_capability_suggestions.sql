-- name: UpsertModelCapabilitySuggestion :one
-- UpsertModelCapabilitySuggestion 写入或刷新一条能力补全建议（同 (模型, 能力) 更新而非堆叠）。
-- 调用方需先确认非 dismissed/accepted（避免复活已决策项）。
INSERT INTO model_capability_suggestions (
    model_id,
    capability_key,
    suggested_level,
    evidence_kind,
    rationale,
    status,
    decided_at,
    decided_by
)
VALUES (
    sqlc.arg(model_id),
    sqlc.arg(capability_key),
    sqlc.arg(suggested_level),
    sqlc.arg(evidence_kind),
    sqlc.arg(rationale),
    sqlc.arg(status),
    sqlc.narg(decided_at),
    sqlc.narg(decided_by)
)
ON CONFLICT (model_id, capability_key) DO UPDATE
SET suggested_level = excluded.suggested_level,
    evidence_kind = excluded.evidence_kind,
    rationale = excluded.rationale,
    status = excluded.status,
    decided_at = excluded.decided_at,
    decided_by = excluded.decided_by
RETURNING *;

-- name: GetModelCapabilitySuggestion :one
-- GetModelCapabilitySuggestion 按 (模型, 能力) 取建议（worker 判定是否已决策）。
SELECT *
FROM model_capability_suggestions
WHERE model_id = sqlc.arg(model_id)
    AND capability_key = sqlc.arg(capability_key);

-- name: ListModelCapabilitySuggestionsByStatus :many
-- ListModelCapabilitySuggestionsByStatus 按状态列建议（admin 列待采纳）。
SELECT *
FROM model_capability_suggestions
WHERE status = sqlc.arg(status)
ORDER BY model_id ASC, capability_key ASC;

-- name: ListModelCapabilitySuggestionsByModel :many
-- ListModelCapabilitySuggestionsByModel 列出某模型全部建议（admin 模型详情）。
SELECT *
FROM model_capability_suggestions
WHERE model_id = sqlc.arg(model_id)
ORDER BY capability_key ASC;

-- name: MarkModelCapabilitySuggestionDecided :one
-- MarkModelCapabilitySuggestionDecided 标记某条建议为已采纳/已忽略（admin 或 auto_calibrate 决策）。
UPDATE model_capability_suggestions
SET status = sqlc.arg(status),
    decided_at = now(),
    decided_by = sqlc.arg(decided_by)
WHERE id = sqlc.arg(id)
RETURNING *;
