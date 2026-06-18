-- name: IncrementModelCapabilityObservation :exec
-- IncrementModelCapabilityObservation 增量累加一条 (模型, 渠道, 能力) 的成功/证据观测计数（worker 聚合用）。
INSERT INTO model_capability_observations (
    model_id,
    channel_id,
    capability_key,
    success_count,
    evidence_count,
    first_seen_at,
    last_seen_at,
    updated_at
)
VALUES (
    sqlc.arg(model_id),
    sqlc.arg(channel_id),
    sqlc.arg(capability_key),
    sqlc.arg(success_delta),
    sqlc.arg(evidence_delta),
    now(),
    now(),
    now()
)
ON CONFLICT (model_id, channel_id, capability_key) DO UPDATE
SET success_count = model_capability_observations.success_count + excluded.success_count,
    evidence_count = model_capability_observations.evidence_count + excluded.evidence_count,
    last_seen_at = now(),
    updated_at = now();

-- name: ListModelCapabilityObservations :many
-- ListModelCapabilityObservations 列出全部能力观测 rollup（决策输入；表小，按模型/渠道/能力有序）。
SELECT *
FROM model_capability_observations
ORDER BY model_id ASC, channel_id ASC, capability_key ASC;

-- name: ListModelCapabilityObservationsByModel :many
-- ListModelCapabilityObservationsByModel 列出某模型全部渠道的能力观测（admin/排障用）。
SELECT *
FROM model_capability_observations
WHERE model_id = sqlc.arg(model_id)
ORDER BY channel_id ASC, capability_key ASC;
