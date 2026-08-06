-- runtime_control_operations 的可恢复发布状态机查询。
-- Admin 与 Worker 共用同一套生成的 sqlc 查询，不各写一套状态机。
-- 普通状态机：preparing -> prepared -> db_committed -> committed；普通 control 允许 preparing|prepared -> aborted；
-- runtime_state_epoch 同样自动推进到 committed，任何阶段不允许 Abort；旧 awaiting_release 记录只做兼容收口。

-- name: CreateBootstrapRuntimeStateEpoch :one
-- CreateBootstrapRuntimeStateEpoch 在同一 PostgreSQL statement/事务中建立 revision=1/recovering
-- 保留行与 preparing durable operation。已有保留行时返回 no rows，绝不覆盖。
WITH inserted_epoch AS (
    INSERT INTO app_settings (key, value, description, revision, updated_at)
    VALUES (
        'gateway.runtime_state_epoch',
        sqlc.arg(state_epoch_value)::jsonb,
        '网关 Redis 运行态完整性 epoch（维护专用，不对外编辑）',
        1,
        now()
    )
    ON CONFLICT (key) DO NOTHING
    RETURNING key
)
INSERT INTO runtime_control_operations (
    token, kind, channel_id, setting_key,
    current_revision, next_revision, payload_hash,
    epoch_transition, expected_marker_hash, recovery_evidence, release_evidence,
    state
)
SELECT
    sqlc.arg(token), 'runtime_state_epoch', NULL, inserted_epoch.key,
    0, 1, sqlc.arg(payload_hash),
    sqlc.arg(epoch_transition)::jsonb, NULL, NULL, NULL,
    'preparing'
FROM inserted_epoch
RETURNING id, token, kind, channel_id, setting_key, current_revision, next_revision, payload_hash,
    epoch_transition, expected_marker_hash, recovery_evidence, release_evidence, state, created_at, updated_at, completed_at;

-- name: CreateRuntimeControlOperation :one
-- CreateRuntimeControlOperation 以 preparing 状态开一条发布操作（Redis Prepare 前）。
INSERT INTO runtime_control_operations (
    token, kind, channel_id, setting_key,
    current_revision, next_revision, payload_hash,
    epoch_transition, expected_marker_hash, recovery_evidence,
    state
) VALUES (
    sqlc.arg(token), sqlc.arg(kind), sqlc.narg(channel_id), sqlc.narg(setting_key),
    sqlc.arg(current_revision), sqlc.arg(next_revision), sqlc.arg(payload_hash),
    sqlc.narg(epoch_transition), sqlc.narg(expected_marker_hash), sqlc.narg(recovery_evidence),
    'preparing'
)
RETURNING id, token, kind, channel_id, setting_key, current_revision, next_revision, payload_hash,
    epoch_transition, expected_marker_hash, recovery_evidence, release_evidence, state, created_at, updated_at, completed_at;

-- name: GetRuntimeControlOperationByToken :one
-- GetRuntimeControlOperationByToken 按 token 读取操作（幂等/恢复对账）。
SELECT id, token, kind, channel_id, setting_key, current_revision, next_revision, payload_hash,
    epoch_transition, expected_marker_hash, recovery_evidence, release_evidence, state, created_at, updated_at, completed_at
FROM runtime_control_operations
WHERE token = sqlc.arg(token);

-- name: GetRuntimeControlOperationByTokenForUpdate :one
-- GetRuntimeControlOperationByTokenForUpdate 锁定 operation，供 epoch 的 PostgreSQL 原子推进/终结。
SELECT id, token, kind, channel_id, setting_key, current_revision, next_revision, payload_hash,
    epoch_transition, expected_marker_hash, recovery_evidence, release_evidence, state, created_at, updated_at, completed_at
FROM runtime_control_operations
WHERE token = sqlc.arg(token)
FOR UPDATE;

-- name: GetNonterminalRuntimeStateEpochOperation :one
-- 完整性 epoch 同时最多一条非终态 operation（由 partial UNIQUE 保证）。
SELECT id, token, kind, channel_id, setting_key, current_revision, next_revision, payload_hash,
    epoch_transition, expected_marker_hash, recovery_evidence, release_evidence, state, created_at, updated_at, completed_at
FROM runtime_control_operations
WHERE kind = 'runtime_state_epoch'
  AND setting_key = 'gateway.runtime_state_epoch'
  AND state <> ALL (ARRAY['committed'::text, 'aborted'::text])
ORDER BY created_at, id
LIMIT 1;

-- name: CompareAndSetRuntimeStateEpochExpectedMarkerHash :execrows
-- 只有 application 严格分类为 absent 或 durable old ready 后才可 CAS 记录 observed marker。
-- 冲突 marker 不得调用本查询；并发变化也因 current hash 不匹配而零更新。
UPDATE runtime_control_operations
SET expected_marker_hash = sqlc.arg(next_expected_marker_hash), updated_at = now()
WHERE token = sqlc.arg(token)
  AND payload_hash = sqlc.arg(payload_hash)
  AND kind = 'runtime_state_epoch'
  AND state IN ('preparing', 'prepared', 'db_committed')
  AND expected_marker_hash IS NOT DISTINCT FROM sqlc.narg(current_expected_marker_hash)::text;

-- name: MarkRuntimeControlOperationPrepared :execrows
-- preparing -> prepared（Redis Prepare 成功后，token/payload_hash CAS）。
UPDATE runtime_control_operations
SET state = 'prepared', updated_at = now()
WHERE token = sqlc.arg(token) AND payload_hash = sqlc.arg(payload_hash) AND state = 'preparing';

-- name: MarkRuntimeControlOperationDBCommitted :execrows
-- prepared -> db_committed（同一业务事务内提交业务行后）。
UPDATE runtime_control_operations
SET state = 'db_committed', updated_at = now()
WHERE token = sqlc.arg(token) AND payload_hash = sqlc.arg(payload_hash) AND state = 'prepared';

-- name: AdvanceRuntimeStateEpochRecovering :execrows
-- 非 bootstrap transition 在 operation=prepared 后把保留行从 durable old ready CAS 到 new recovering，
-- revision 仅在创建新 epoch 时 +1。必须与 MarkRuntimeControlOperationDBCommitted 同事务。
UPDATE app_settings
SET value = sqlc.arg(next_value)::jsonb,
    revision = sqlc.arg(next_revision),
    updated_at = now()
WHERE key = 'gateway.runtime_state_epoch'
  AND revision = sqlc.arg(current_revision)
  AND sqlc.arg(next_revision) = sqlc.arg(current_revision) + 1
  AND value = sqlc.arg(current_value)::jsonb;

-- name: MarkRuntimeStateEpochReady :execrows
-- Redis Commit 确认后，在不推进 revision 的前提下将 new epoch recovering CAS 为 ready。
-- 必须与 operation db_committed->committed 同事务。
UPDATE app_settings
SET value = sqlc.arg(ready_value)::jsonb,
    updated_at = now()
WHERE key = 'gateway.runtime_state_epoch'
  AND revision = sqlc.arg(revision)
  AND value = sqlc.arg(recovering_value)::jsonb;

-- name: MarkRuntimeControlOperationCommitted :execrows
-- db_committed/legacy awaiting_release -> committed（Redis Commit 成功后终结）。
UPDATE runtime_control_operations
SET state = 'committed', completed_at = now(), updated_at = now()
WHERE token = sqlc.arg(token) AND payload_hash = sqlc.arg(payload_hash)
  AND state IN ('db_committed', 'awaiting_release');

-- name: MarkRuntimeControlOperationAborted :execrows
-- preparing|prepared -> aborted（仅业务 revision 未提交；epoch kind 不允许 abort，由应用层拦截）。
UPDATE runtime_control_operations
SET state = 'aborted', completed_at = now(), updated_at = now()
WHERE token = sqlc.arg(token) AND payload_hash = sqlc.arg(payload_hash)
  AND state IN ('preparing', 'prepared')
  AND kind <> 'runtime_state_epoch';

-- name: ListNonterminalRuntimeControlOperations :many
-- 供 reconciler 扫描未终结操作（preparing|prepared|db_committed），按创建时间升序。
SELECT id, token, kind, channel_id, setting_key, current_revision, next_revision, payload_hash,
    epoch_transition, expected_marker_hash, recovery_evidence, release_evidence, state, created_at, updated_at, completed_at
FROM runtime_control_operations
WHERE state <> ALL (ARRAY['committed'::text, 'aborted'::text])
ORDER BY created_at, id;
