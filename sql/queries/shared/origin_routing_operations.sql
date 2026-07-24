-- origin_routing_operations 的可恢复围栏状态机查询（P4 §4.3）。
-- 状态机：preparing -> prepared -> db_committed -> committed；仅 preparing|prepared -> aborted。

-- name: CreateOriginRoutingOperation :one
-- 以 preparing 开一条 Origin/Provider 围栏操作（Redis Prepare 前）。
INSERT INTO origin_routing_operations (
    token, kind, provider_id, origin_id, transitions, payload_hash, state
) VALUES (
    sqlc.arg(token), sqlc.arg(kind), sqlc.narg(provider_id), sqlc.narg(origin_id),
    sqlc.arg(transitions), sqlc.arg(payload_hash), 'preparing'
)
RETURNING id, token, kind, provider_id, origin_id, transitions, payload_hash, state, created_at, updated_at, completed_at;

-- name: GetOriginRoutingOperationByToken :one
SELECT id, token, kind, provider_id, origin_id, transitions, payload_hash, state, created_at, updated_at, completed_at
FROM origin_routing_operations
WHERE token = sqlc.arg(token);

-- name: MarkOriginRoutingOperationPrepared :execrows
UPDATE origin_routing_operations
SET state = 'prepared', updated_at = now()
WHERE token = sqlc.arg(token) AND payload_hash = sqlc.arg(payload_hash) AND state = 'preparing';

-- name: MarkOriginRoutingOperationDBCommitted :execrows
UPDATE origin_routing_operations
SET state = 'db_committed', updated_at = now()
WHERE token = sqlc.arg(token) AND payload_hash = sqlc.arg(payload_hash) AND state = 'prepared';

-- name: MarkOriginRoutingOperationCommitted :execrows
UPDATE origin_routing_operations
SET state = 'committed', completed_at = now(), updated_at = now()
WHERE token = sqlc.arg(token) AND payload_hash = sqlc.arg(payload_hash) AND state = 'db_committed';

-- name: MarkOriginRoutingOperationAborted :execrows
UPDATE origin_routing_operations
SET state = 'aborted', completed_at = now(), updated_at = now()
WHERE token = sqlc.arg(token) AND payload_hash = sqlc.arg(payload_hash)
  AND state IN ('preparing', 'prepared');

-- name: ListNonterminalOriginRoutingOperations :many
SELECT id, token, kind, provider_id, origin_id, transitions, payload_hash, state, created_at, updated_at, completed_at
FROM origin_routing_operations
WHERE state <> ALL (ARRAY['committed'::text, 'aborted'::text])
ORDER BY created_at, id;

-- name: DeleteTerminalOriginRoutingOperationsBefore :execrows
DELETE FROM origin_routing_operations
WHERE state = ANY (ARRAY['committed'::text, 'aborted'::text])
  AND completed_at IS NOT NULL
  AND completed_at < sqlc.arg(cutoff);
