-- name: ListChannelOverrides :many
-- ListChannelOverrides 列出指定 channel 的全部能力收紧策略。
SELECT *
FROM channel_capability_overrides
WHERE channel_id = sqlc.arg(channel_id)
ORDER BY capability_key ASC;

-- name: UpsertChannelOverride :one
-- UpsertChannelOverride 写入或覆盖 channel 能力收紧策略（admin 用）。
INSERT INTO channel_capability_overrides (
    channel_id,
    capability_key,
    support_level,
    limits,
    reason,
    updated_by
)
VALUES (
    sqlc.arg(channel_id),
    sqlc.arg(capability_key),
    sqlc.arg(support_level),
    sqlc.arg(limits),
    sqlc.arg(reason),
    sqlc.arg(updated_by)
)
ON CONFLICT (channel_id, capability_key) DO UPDATE
SET support_level = excluded.support_level,
    limits = excluded.limits,
    reason = excluded.reason,
    updated_by = excluded.updated_by,
    updated_at = now()
RETURNING *;

-- name: DeleteChannelOverride :exec
-- DeleteChannelOverride 删除指定 channel 对某能力的收紧策略。
DELETE FROM channel_capability_overrides
WHERE channel_id = sqlc.arg(channel_id)
    AND capability_key = sqlc.arg(capability_key);
