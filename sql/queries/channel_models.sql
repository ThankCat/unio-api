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
-- DeleteChannelModel 删除绑定，并在同一条语句内级联清理该 (channel_id, model_id) 边自身的
-- channel_prices（渠道-模型成本价，追加式配置、无删除接口，只能停用）——否则只要该边配过成本价，
-- 就会被自身配置行永久挡住解绑。channel_prices 仅被账务历史（cost_snapshots/price_snapshots/
-- settlement_recovery_jobs）以 NO ACTION 外键引用：若该边确有计费历史，删 channel_prices 触发
-- 23503 使整条语句回滚，上层降级为 conflict，提示改用停用——保住计费/审计链路。
-- 无历史时（仅配置行）则干净解绑。返回值为 channel_models 行的受影响数（0 表示绑定不存在）。
WITH deleted_channel_prices AS (
    DELETE FROM channel_prices
    WHERE channel_prices.channel_id = sqlc.arg(channel_id)
      AND channel_prices.model_id = sqlc.arg(model_id)
)
DELETE FROM channel_models
WHERE channel_models.channel_id = sqlc.arg(channel_id)
  AND channel_models.model_id = sqlc.arg(model_id);

-- name: FindRouteCandidates :many
-- FindRouteCandidates 按请求模型、用户策略与线路查找可用 channel 路由候选（DEC-026 倍率定价）。
-- 在既有过滤（model/channel/provider/cm enabled + 协议 + 用户 allow/deny）之上叠加：
--   1. 线路候选池：pool_kind='explicit' 时候选 ∩ route_channels（fixed 即只剩一条）；
--   2. 已定价过滤：候选必须同时满足「模型有当前生效 model_prices 基准售价」+「渠道有当前生效 channel_prices 成本行」（两者皆缺则排除，不参与计费）；
--   3. 带回模型基准售价（base，供 Go 侧 ScaleCustomerPrice = 基准 × 线路倍率 算客户售价）与命中渠道成本（cost，供 cheapest 按成本排序与毛利结算）。
-- 客户售价 = base × routes.price_ratio，在 Go 侧算（同一请求所有候选共享同一售价）；
-- 策略排序（cheapest 按成本 / stable 按健康 / random 洗牌 / fixed 单条）在 Go 侧完成；此处仅给稳定的 priority 基序。
WITH user_scope AS (
    SELECT sqlc.arg(user_id)::BIGINT AS user_id
),
user_policy_mode AS (
    SELECT EXISTS (
        SELECT 1
        FROM user_model_policies ump
        JOIN user_scope us ON us.user_id = ump.user_id
        WHERE ump.visibility = 'allowed'
    ) AS has_allow_list
)
SELECT
    m.id AS model_db_id,
    m.model_id AS requested_model_id,
    m.max_output_tokens AS model_max_output_tokens,
    p.id AS provider_id,
    p.slug AS provider_slug,
    c.adapter_key AS adapter_key,
    c.protocol AS protocol,
    c.id AS channel_id,
    c.base_url,
    c.credential,
    c.timeout_ms,
    c.priority,
    c.rpm_limit AS channel_rpm_limit,
    c.tpm_limit AS channel_tpm_limit,
    c.rpd_limit AS channel_rpd_limit,
    cm.upstream_model,
    base.id AS model_price_id,
    base.currency AS base_currency,
    base.pricing_unit AS base_pricing_unit,
    base.uncached_input_price,
    base.cache_read_input_price,
    base.cache_write_5m_input_price,
    base.cache_write_1h_input_price,
    base.output_price,
    base.reasoning_output_price,
    cost.id AS channel_price_id,
    cost.currency AS cost_currency,
    cost.pricing_unit AS cost_pricing_unit,
    cost.uncached_input_cost,
    cost.cache_read_input_cost,
    cost.cache_write_5m_input_cost,
    cost.cache_write_1h_input_cost,
    cost.output_cost,
    cost.reasoning_output_cost
FROM channel_models cm
JOIN models m ON m.id = cm.model_id
JOIN channels c ON c.id = cm.channel_id
JOIN providers p ON p.id = c.provider_id
JOIN user_scope us ON us.user_id > 0
JOIN LATERAL (
    -- base: 模型当前生效的基准售价（DEC-026），客户售价 = base × 线路倍率。
    SELECT mp.id, mp.currency, mp.pricing_unit,
        mp.uncached_input_price, mp.cache_read_input_price,
        mp.cache_write_5m_input_price, mp.cache_write_1h_input_price,
        mp.output_price, mp.reasoning_output_price
    FROM model_prices mp
    WHERE mp.model_id = m.id
      AND mp.status = 'enabled'
      AND mp.effective_from <= sqlc.arg(at_time)
      AND (mp.effective_to IS NULL OR mp.effective_to > sqlc.arg(at_time))
    ORDER BY mp.effective_from DESC, mp.id DESC
    LIMIT 1
) base ON TRUE
JOIN LATERAL (
    -- cost: 命中渠道当前生效的上游成本（毛利结算与 cheapest 按成本排序用）。
    SELECT cp.id, cp.currency, cp.pricing_unit,
        cp.uncached_input_cost, cp.cache_read_input_cost,
        cp.cache_write_5m_input_cost, cp.cache_write_1h_input_cost,
        cp.output_cost, cp.reasoning_output_cost
    FROM channel_prices cp
    WHERE cp.channel_id = c.id
      AND cp.model_id = m.id
      AND cp.status = 'enabled'
      AND cp.effective_from <= sqlc.arg(at_time)
      AND (cp.effective_to IS NULL OR cp.effective_to > sqlc.arg(at_time))
    ORDER BY cp.effective_from DESC, cp.id DESC
    LIMIT 1
) cost ON TRUE
WHERE m.model_id = sqlc.arg(requested_model_id)
  AND c.protocol = sqlc.arg(ingress_protocol)
  AND m.status = 'enabled'
  AND cm.status = 'enabled'
  AND c.status = 'enabled'
  AND c.credential_valid
  AND p.status = 'enabled'
  AND (
    sqlc.arg(pool_kind)::TEXT = 'all'
        OR EXISTS (
        SELECT 1
        FROM route_channels rc
        WHERE rc.route_id = sqlc.arg(route_id)
          AND rc.channel_id = c.id
    )
  )
  AND NOT EXISTS (
    SELECT 1
    FROM user_model_policies denied
    JOIN user_scope us ON us.user_id = denied.user_id
    WHERE denied.model_id = m.id
      AND denied.visibility = 'denied'
)
  AND (
    NOT (SELECT has_allow_list FROM user_policy_mode)
        OR EXISTS (
        SELECT 1
        FROM user_model_policies allowed
        JOIN user_scope us ON us.user_id = allowed.user_id
        WHERE allowed.model_id = m.id
          AND allowed.visibility = 'allowed'
    )
    )
ORDER BY
    c.priority ASC,
    c.id ASC;
