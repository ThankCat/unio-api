-- name: ListChannelModelsByChannel :many
-- ListChannelModelsByChannel 列出某 channel 的全部模型绑定，连带 Unio 侧模型的对外 ID 与展示名，供 admin 管理台展示。
SELECT
    cm.id,
    cm.channel_id,
    cm.model_id,
    cm.upstream_model,
    cm.status,
    cm.created_at,
    cm.updated_at,
    m.model_id AS model_external_id,
    m.display_name AS model_display_name
FROM channel_models cm
JOIN models m ON m.id = cm.model_id
WHERE cm.channel_id = sqlc.arg(channel_id)
ORDER BY m.model_id;

-- name: GetChannelModel :one
-- GetChannelModel 按 (channel_id, model_id) 读取单条绑定。
SELECT id, channel_id, model_id, upstream_model, status, created_at, updated_at
FROM channel_models
WHERE channel_id = sqlc.arg(channel_id) AND model_id = sqlc.arg(model_id)
LIMIT 1;

-- name: CreateChannelModel :one
-- CreateChannelModel 创建 channel↔model 绑定；同一 channel 对同一 model 只能绑定一次（唯一约束）。
INSERT INTO channel_models (channel_id, model_id, upstream_model, status)
VALUES (sqlc.arg(channel_id), sqlc.arg(model_id), sqlc.arg(upstream_model), sqlc.arg(status))
RETURNING id, channel_id, model_id, upstream_model, status, created_at, updated_at;

-- name: UpdateChannelModel :one
-- UpdateChannelModel 更新绑定的上游模型名与启停状态；按 (channel_id, model_id) 定位。
UPDATE channel_models
SET upstream_model = sqlc.arg(upstream_model), status = sqlc.arg(status), updated_at = now()
WHERE channel_id = sqlc.arg(channel_id) AND model_id = sqlc.arg(model_id)
RETURNING id, channel_id, model_id, upstream_model, status, created_at, updated_at;

-- name: DeleteChannelModel :execrows
-- DeleteChannelModel 删除绑定；被 cost_snapshots/channel_cost_prices 外键引用时由 DB 拒绝（23503），上层降级为 conflict。
DELETE FROM channel_models
WHERE channel_id = sqlc.arg(channel_id) AND model_id = sqlc.arg(model_id);

-- name: FindRouteCandidates :many
-- FindRouteCandidates 按请求模型和项目策略查找可用 channel 路由候选。
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
SELECT
    m.id AS model_db_id,
    m.model_id AS requested_model_id,
    p.id AS provider_id,
    p.slug AS provider_slug,
    c.adapter_key AS adapter_key,
    c.protocol AS protocol,
    c.id AS channel_id,
    c.base_url,
    c.credential_encrypted,
    c.timeout_ms,
    c.priority,
    cm.upstream_model
FROM channel_models cm
JOIN models m ON m.id = cm.model_id
JOIN channels c ON c.id = cm.channel_id
JOIN providers p ON p.id = c.provider_id
JOIN project_scope ps ON ps.project_id > 0
WHERE m.model_id = sqlc.arg(requested_model_id)
  AND c.protocol = sqlc.arg(ingress_protocol)
  AND m.status = 'enabled'
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
ORDER BY
    c.priority ASC,
    c.id ASC;
