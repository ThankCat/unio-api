-- name: CreateModelCatalogLink :one
-- CreateModelCatalogLink 建立「模型 ← 目录条目」采纳关联（采纳事务的一部分）。
INSERT INTO model_catalog_links (
    model_id,
    canonical_id,
    adopted_fingerprint
)
VALUES (
    sqlc.arg(model_id),
    sqlc.arg(canonical_id),
    sqlc.arg(adopted_fingerprint)
)
RETURNING *;

-- name: GetModelCatalogLink :one
-- GetModelCatalogLink 读取某模型的采纳关联（刷新/提醒前置查询）。
SELECT *
FROM model_catalog_links
WHERE model_id = sqlc.arg(model_id);

-- name: UpdateModelCatalogLinkBaseline :exec
-- UpdateModelCatalogLinkBaseline 「从目录刷新」后把采纳基线指纹更新为最新，并清空忽略/稍后提醒状态。
UPDATE model_catalog_links
SET adopted_fingerprint = sqlc.arg(adopted_fingerprint),
    dismissed_fingerprint = NULL,
    reminder_snooze_until = NULL,
    updated_at = now()
WHERE model_id = sqlc.arg(model_id);

-- name: SetModelCatalogLinkDismissed :exec
-- SetModelCatalogLinkDismissed 忽略本次更新：记下被忽略的目录指纹；目录再变到新指纹会重新提醒。
UPDATE model_catalog_links
SET dismissed_fingerprint = sqlc.arg(dismissed_fingerprint),
    updated_at = now()
WHERE model_id = sqlc.arg(model_id);

-- name: SetModelCatalogLinkMuted :exec
-- SetModelCatalogLinkMuted 永久忽略更新（true）/ 取消静音（false）。
UPDATE model_catalog_links
SET reminder_muted = sqlc.arg(reminder_muted),
    updated_at = now()
WHERE model_id = sqlc.arg(model_id);

-- name: SetModelCatalogLinkSnooze :exec
-- SetModelCatalogLinkSnooze 稍后提醒：此时间之前不提醒。
UPDATE model_catalog_links
SET reminder_snooze_until = sqlc.narg(reminder_snooze_until),
    updated_at = now()
WHERE model_id = sqlc.arg(model_id);
