-- app_settings 通用 key→JSONB 全局设置存储的读写查询。

-- name: GetAppSetting :one
-- GetAppSetting 按 key 读取设置内容;不存在时返回 pgx.ErrNoRows(由调用方回退默认)。
SELECT value
FROM app_settings
WHERE key = $1;

-- name: GetAppSettingRecord :one
-- GetAppSettingRecord 读取设置业务行及其单调 revision，供 P4 runtime-control 发布和同步状态比较。
SELECT key, value, description, updated_at, revision
FROM app_settings
WHERE key = $1;

-- name: GetAppSettingRecordForUpdate :one
-- GetAppSettingRecordForUpdate 在 epoch 事务中锁定维护保留行，防止并发 coordinator 跨越 PostgreSQL 状态。
SELECT key, value, description, updated_at, revision
FROM app_settings
WHERE key = $1
FOR UPDATE;

-- name: GetGatewayAdmissionControlRevisions :one
-- GetGatewayAdmissionControlRevisions 在同一 PostgreSQL statement snapshot 中读取完整性 epoch、线路限流、渠道限流与全局并发 revision；任一必需行缺失时返回 no rows。
SELECT
    epoch.value AS runtime_state_epoch_value,
    epoch.revision AS runtime_state_epoch_revision,
    route_rate_limit.revision AS route_rate_limit_defaults_revision,
    channel_rate_limit.revision AS channel_rate_limit_defaults_revision,
    concurrency.revision AS concurrency_defaults_revision
FROM app_settings AS epoch
JOIN app_settings AS route_rate_limit
  ON route_rate_limit.key = 'gateway.route_rate_limit_defaults'
JOIN app_settings AS channel_rate_limit
  ON channel_rate_limit.key = 'gateway.channel_rate_limit_defaults'
JOIN app_settings AS concurrency
  ON concurrency.key = 'gateway.concurrency_defaults'
WHERE epoch.key = 'gateway.runtime_state_epoch';

-- name: GetGatewayRuntimeReadinessSnapshot :one
-- GetGatewayRuntimeReadinessSnapshot 在同一 statement snapshot 中读取 readiness 所需的 epoch 与五个关键 control revision，
-- 并确认关键 setting、Channel admission 与 Origin 围栏的持久操作均已终结。
-- 任一必需行缺失时返回 no rows，Gateway 必须 fail-closed。
SELECT
    epoch.value AS runtime_state_epoch_value,
    epoch.revision AS runtime_state_epoch_revision,
    route_rate_limit.revision AS route_rate_limit_defaults_revision,
    channel_rate_limit.revision AS channel_rate_limit_defaults_revision,
    concurrency.revision AS concurrency_defaults_revision,
    circuit_breaker.revision AS circuit_breaker_revision,
    routing_balance.revision AS routing_balance_revision,
    CASE WHEN NOT EXISTS (
        SELECT 1
        FROM runtime_control_operations AS operation
        WHERE operation.state <> ALL (ARRAY['committed'::text, 'aborted'::text])
          AND (
              operation.kind = 'runtime_state_epoch'
              OR operation.kind = 'channel_admission_limits'
              OR (
                  operation.kind = 'app_setting'
                  AND operation.setting_key = ANY (ARRAY[
                      'gateway.route_rate_limit_defaults'::text,
                      'gateway.channel_rate_limit_defaults'::text,
                      'gateway.concurrency_defaults'::text,
                      'gateway.circuit_breaker'::text,
                      'gateway.routing_balance'::text
                  ])
              )
          )
    )
    AND NOT EXISTS (
        SELECT 1
        FROM origin_routing_operations AS operation
        WHERE operation.state <> ALL (ARRAY['committed'::text, 'aborted'::text])
    ) THEN TRUE ELSE FALSE END AS runtime_operations_reconciled,
    CASE WHEN
        epoch.value ->> 'state' = 'ready'
        AND EXISTS (
            SELECT 1
            FROM runtime_control_operations AS operation
            WHERE operation.kind = 'runtime_state_epoch'
              AND operation.state = 'awaiting_release'
              AND operation.next_revision = epoch.revision
              AND operation.epoch_transition ->> 'new_epoch' = epoch.value ->> 'epoch'
              AND operation.epoch_transition ->> 'reason' IN ('state_loss', 'restore')
              AND operation.recovery_evidence ->> 'status' = 'approved'
              AND operation.release_evidence IS NULL
        )
        AND NOT EXISTS (
            SELECT 1
            FROM runtime_control_operations AS operation
            WHERE operation.state <> ALL (ARRAY['committed'::text, 'aborted'::text])
              AND (
                  operation.kind = 'runtime_state_epoch'
                  OR operation.kind = 'channel_admission_limits'
                  OR (
                      operation.kind = 'app_setting'
                      AND operation.setting_key = ANY (ARRAY[
                          'gateway.route_rate_limit_defaults'::text,
                          'gateway.channel_rate_limit_defaults'::text,
                          'gateway.concurrency_defaults'::text,
                          'gateway.circuit_breaker'::text,
                          'gateway.routing_balance'::text
                      ])
                  )
              )
              AND NOT (
                  operation.kind = 'runtime_state_epoch'
                  AND operation.state = 'awaiting_release'
                  AND operation.next_revision = epoch.revision
                  AND operation.epoch_transition ->> 'new_epoch' = epoch.value ->> 'epoch'
                  AND operation.epoch_transition ->> 'reason' IN ('state_loss', 'restore')
                  AND operation.recovery_evidence ->> 'status' = 'approved'
                  AND operation.release_evidence IS NULL
              )
        )
        AND NOT EXISTS (
            SELECT 1
            FROM origin_routing_operations AS operation
            WHERE operation.state <> ALL (ARRAY['committed'::text, 'aborted'::text])
        )
    THEN TRUE ELSE FALSE END AS runtime_maintenance_smoke_allowed
FROM app_settings AS epoch
JOIN app_settings AS route_rate_limit
  ON route_rate_limit.key = 'gateway.route_rate_limit_defaults'
JOIN app_settings AS channel_rate_limit
  ON channel_rate_limit.key = 'gateway.channel_rate_limit_defaults'
JOIN app_settings AS concurrency
  ON concurrency.key = 'gateway.concurrency_defaults'
JOIN app_settings AS circuit_breaker
  ON circuit_breaker.key = 'gateway.circuit_breaker'
JOIN app_settings AS routing_balance
  ON routing_balance.key = 'gateway.routing_balance'
WHERE epoch.key = 'gateway.runtime_state_epoch';

-- name: GetGatewayRoutingControlRevisions :one
-- GetGatewayRoutingControlRevisions 在同一 PostgreSQL statement snapshot 中读取完整性 epoch 与候选路由所需 breaker/balance revision；任一必需行缺失时返回 no rows。
SELECT
    epoch.value AS runtime_state_epoch_value,
    epoch.revision AS runtime_state_epoch_revision,
    circuit_breaker.revision AS circuit_breaker_revision,
    routing_balance.revision AS routing_balance_revision
FROM app_settings AS epoch
JOIN app_settings AS circuit_breaker
  ON circuit_breaker.key = 'gateway.circuit_breaker'
JOIN app_settings AS routing_balance
  ON routing_balance.key = 'gateway.routing_balance'
WHERE epoch.key = 'gateway.runtime_state_epoch';

-- name: SeedRuntimeStateEpoch :execrows
-- SeedRuntimeStateEpoch 仅供受信任的 bootstrap/恢复 use-case 创建非普通设置行；普通 settings registry/API 不得调用。
INSERT INTO app_settings (key, value, description, revision, updated_at)
VALUES (
    'gateway.runtime_state_epoch',
    sqlc.arg(value)::jsonb,
    '网关 Redis 运行态完整性 epoch（维护专用，不对外编辑）',
    1,
    now()
)
ON CONFLICT (key) DO NOTHING;

-- name: ListAppSettings :many
-- ListAppSettings 列出全部已持久化设置(供 admin 面板对照展示)。
SELECT key, value, description, updated_at
FROM app_settings
ORDER BY key;

-- name: UpsertAppSetting :exec
-- UpsertAppSetting 写入普通设置；只有 JSONB 语义值真变化时递增 revision，重复写同值不推进。
INSERT INTO app_settings (key, value, description, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    description = EXCLUDED.description,
    revision = app_settings.revision + CASE
        WHEN app_settings.value IS DISTINCT FROM EXCLUDED.value THEN 1
        ELSE 0
    END,
    updated_at = CASE
        WHEN app_settings.value IS DISTINCT FROM EXCLUDED.value
          OR app_settings.description IS DISTINCT FROM EXCLUDED.description
        THEN now()
        ELSE app_settings.updated_at
    END;

-- name: UpdateAppSettingAtRevision :one
-- UpdateAppSettingAtRevision 在 runtime-control 的业务事务中 CAS 提交关键设置及 next revision。
UPDATE app_settings
SET value = sqlc.arg(value)::jsonb,
    description = sqlc.arg(description),
    revision = sqlc.arg(next_revision),
    updated_at = now()
WHERE key = sqlc.arg(key)
  AND revision = sqlc.arg(current_revision)
  AND sqlc.arg(next_revision) = sqlc.arg(current_revision) + 1
  AND value IS DISTINCT FROM sqlc.arg(value)::jsonb
RETURNING key, value, description, updated_at, revision;

-- name: SeedAppSetting :exec
-- SeedAppSetting 仅在 key 缺行时写入注册表默认值(启动 seed 用)。
-- 与 UpsertAppSetting 的关键区别:DO NOTHING——绝不覆盖运维已改过的值;幂等且并发安全,
-- gateway/admin 启动都会调用。
INSERT INTO app_settings (key, value, description, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (key) DO NOTHING;
