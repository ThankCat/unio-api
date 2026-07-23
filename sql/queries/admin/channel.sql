-- name: CreateChannelCostMultiplier :one
-- CreateChannelCostMultiplier 创建一条渠道价格倍率（DEC-027）。model_id 为空=渠道默认；非空=对该模型的覆盖。
-- 启用窗口重叠（同 channel + 同 model_key）由 ex_channel_cost_multipliers_enabled_window 保证，违反报 23P01。
INSERT INTO channel_cost_multipliers (
    channel_id,
    model_id,
    multiplier,
    status,
    effective_from,
    effective_to
)
VALUES (
    sqlc.arg(channel_id),
    sqlc.narg(model_id),
    sqlc.arg(multiplier),
    sqlc.arg(status),
    sqlc.arg(effective_from),
    sqlc.arg(effective_to)
)
RETURNING *;

-- name: ListChannelCostMultipliersByChannel :many
-- ListChannelCostMultipliersByChannel 列出某 channel 的全部价格倍率（默认 + 逐模型覆盖，含历史与停用），
-- 逐模型覆盖连带模型对外 ID/展示名（默认行 model 相关列为 NULL），供 admin 管理台展示。
SELECT
    ccm.id,
    ccm.channel_id,
    ccm.model_id,
    ccm.multiplier,
    ccm.status,
    ccm.effective_from,
    ccm.effective_to,
    ccm.created_at,
    ccm.updated_at,
    m.model_id AS model_external_id,
    m.display_name AS model_display_name
FROM channel_cost_multipliers ccm
LEFT JOIN models m ON m.id = ccm.model_id
WHERE ccm.channel_id = sqlc.arg(channel_id)
ORDER BY (ccm.model_id IS NULL) DESC, ccm.model_id, ccm.effective_from DESC, ccm.id DESC;

-- name: ListEnabledChannelCostMultiplierWindows :many
-- ListEnabledChannelCostMultiplierWindows 取某 channel + 同一 model_key（默认或某模型覆盖）全部启用中的生效窗口，
-- 供「窗口不重叠」校验；exclude_id 用于更新时排除自身（创建时传 0）。model_id 用 IS NOT DISTINCT FROM 匹配 NULL 默认。
SELECT id, effective_from, effective_to
FROM channel_cost_multipliers
WHERE channel_id = sqlc.arg(channel_id)
    AND model_id IS NOT DISTINCT FROM sqlc.narg(model_id)
    AND status = 'enabled'
    AND id <> sqlc.arg(exclude_id);

-- name: UpdateChannelCostMultiplierWindow :one
-- UpdateChannelCostMultiplierWindow 调整生效结束时间与启停状态；倍率数值不可改（改倍率请新建一条），账务可复算。
UPDATE channel_cost_multipliers
SET effective_to = sqlc.arg(effective_to),
    status = sqlc.arg(status),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

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

-- name: CreateChannelPrice :one
-- CreateChannelPrice 创建一条渠道-模型成本价（DEC-026：渠道只录成本，售价取 model_prices × 线路倍率）。
-- 启用窗口重叠由 ex_channel_prices_enabled_window 保证，违反报 23P01。
INSERT INTO channel_prices (
    channel_id,
    model_id,
    currency,
    pricing_unit,
    uncached_input_cost,
    cache_read_input_cost,
    cache_write_5m_input_cost,
    cache_write_1h_input_cost,
    cache_write_30m_input_cost,
    output_cost,
    reasoning_output_cost,
    status,
    effective_from,
    effective_to
)
VALUES (
    sqlc.arg(channel_id),
    sqlc.arg(model_id),
    sqlc.arg(currency),
    sqlc.arg(pricing_unit),
    sqlc.arg(uncached_input_cost),
    sqlc.arg(cache_read_input_cost),
    sqlc.arg(cache_write_5m_input_cost),
    sqlc.arg(cache_write_1h_input_cost),
    sqlc.arg(cache_write_30m_input_cost),
    sqlc.arg(output_cost),
    sqlc.arg(reasoning_output_cost),
    sqlc.arg(status),
    sqlc.arg(effective_from),
    sqlc.arg(effective_to)
)
RETURNING *;

-- name: ListChannelPricesByChannel :many
-- ListChannelPricesByChannel 列出某 channel 下全部渠道-模型成本价（含历史与停用），连带模型对外 ID/展示名，供 admin 管理台展示成本。
SELECT
    cp.id,
    cp.channel_id,
    cp.model_id,
    cp.currency,
    cp.pricing_unit,
    cp.uncached_input_cost,
    cp.cache_read_input_cost,
    cp.cache_write_5m_input_cost,
    cp.cache_write_1h_input_cost,
    cp.cache_write_30m_input_cost,
    cp.output_cost,
    cp.reasoning_output_cost,
    cp.status,
    cp.effective_from,
    cp.effective_to,
    cp.created_at,
    cp.updated_at,
    m.model_id AS model_external_id,
    m.display_name AS model_display_name
FROM channel_prices cp
JOIN models m ON m.id = cp.model_id
WHERE cp.channel_id = sqlc.arg(channel_id)
ORDER BY m.model_id, cp.effective_from DESC, cp.id DESC;

-- name: ListEnabledChannelPriceWindows :many
-- ListEnabledChannelPriceWindows 取某 channel/model 全部启用中的价格生效窗口，供「窗口不重叠」校验；exclude_id 用于更新时排除自身（创建时传 0）。
SELECT id, effective_from, effective_to
FROM channel_prices
WHERE channel_id = sqlc.arg(channel_id)
    AND model_id = sqlc.arg(model_id)
    AND status = 'enabled'
    AND id <> sqlc.arg(exclude_id);

-- name: UpdateChannelPriceWindow :one
-- UpdateChannelPriceWindow 调整生效结束时间与启停状态；金额不可改（改价请新建一条），账务可复算。
UPDATE channel_prices
SET effective_to = sqlc.arg(effective_to),
    status = sqlc.arg(status),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: CreateChannelRechargeFactor :one
-- CreateChannelRechargeFactor 创建一条渠道充值倍率（DEC-027）。渠道真实成本 = 上游名义成本 × 本充值倍率。
-- 启用窗口重叠由 ex_channel_recharge_factors_enabled_window 保证，违反报 23P01。
INSERT INTO channel_recharge_factors (
    channel_id,
    factor,
    status,
    effective_from,
    effective_to
)
VALUES (
    sqlc.arg(channel_id),
    sqlc.arg(factor),
    sqlc.arg(status),
    sqlc.arg(effective_from),
    sqlc.arg(effective_to)
)
RETURNING *;

-- name: ListChannelRechargeFactorsByChannel :many
-- ListChannelRechargeFactorsByChannel 列出某 channel 的全部充值倍率（含历史与停用），供 admin 管理台展示。
SELECT
    id,
    channel_id,
    factor,
    status,
    effective_from,
    effective_to,
    created_at,
    updated_at
FROM channel_recharge_factors
WHERE channel_id = sqlc.arg(channel_id)
ORDER BY effective_from DESC, id DESC;

-- name: ListEnabledChannelRechargeFactorWindows :many
-- ListEnabledChannelRechargeFactorWindows 取某 channel 全部启用中的充值倍率生效窗口，供「窗口不重叠」校验；exclude_id 用于更新时排除自身（创建时传 0）。
SELECT id, effective_from, effective_to
FROM channel_recharge_factors
WHERE channel_id = sqlc.arg(channel_id)
    AND status = 'enabled'
    AND id <> sqlc.arg(exclude_id);

-- name: UpdateChannelRechargeFactorWindow :one
-- UpdateChannelRechargeFactorWindow 调整生效结束时间与启停状态；倍率数值不可改（改倍率请新建一条），账务可复算。
UPDATE channel_recharge_factors
SET effective_to = sqlc.arg(effective_to),
    status = sqlc.arg(status),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ListChannelTestLogsByChannel :many
-- ListChannelTestLogsByChannel 按渠道倒序分页返回检测日志（详情页「检测日志」区块）。
SELECT id, channel_id, created_at, source, success, error_code, http_status, latency_ms, tested_model, credential_valid_after, message, upstream_error, tested_endpoint_base_url_revision, tested_endpoint_status_revision, tested_config_revision, state_change_applied
FROM channel_test_logs
WHERE channel_id = sqlc.arg(channel_id)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountChannelTestLogsByChannel :one
-- CountChannelTestLogsByChannel 返回某渠道检测日志总数（分页用）。
SELECT COUNT(*) AS total
FROM channel_test_logs
WHERE channel_id = sqlc.arg(channel_id);

-- name: ListChannelsByProvider :many
-- ListChannelsByProvider 列出指定 provider 下的 channel，按 priority、id 升序。
SELECT id, provider_id, provider_endpoint_id, name, protocol, adapter_key, credential, config_revision, admission_limits_revision, status, priority, timeout_ms, created_at, updated_at, rpm_limit, tpm_limit, rpd_limit, last_tested_at, last_test_ok, last_test_latency_ms, last_test_error, credential_valid, archived_at, concurrency_limit, upstream_bills_on_disconnect
FROM channels
WHERE provider_id = $1
ORDER BY priority, id;

-- name: ListChannelsForRuntimeControlRestore :many
-- ListChannelsForRuntimeControlRestore 返回启动期恢复 Channel admission control 所需的 PostgreSQL 权威事实。
SELECT id, provider_id, provider_endpoint_id, name, protocol, adapter_key, credential, config_revision, admission_limits_revision, status, priority, timeout_ms, created_at, updated_at, rpm_limit, tpm_limit, rpd_limit, last_tested_at, last_test_ok, last_test_latency_ms, last_test_error, credential_valid, archived_at, concurrency_limit, upstream_bills_on_disconnect
FROM channels
ORDER BY id;

-- name: ListChannelsPage :many
-- ListChannelsPage 按 provider/状态/关键字过滤后分页列出 channel，连带 provider 名称；过滤项为 NULL 时不过滤。
SELECT
    c.id, c.provider_id, c.provider_endpoint_id, c.name, c.protocol, c.adapter_key, pe.base_url,
    c.credential, c.status, c.priority, c.timeout_ms, c.created_at, c.updated_at,
    c.rpm_limit, c.tpm_limit, c.rpd_limit, c.concurrency_limit, c.upstream_bills_on_disconnect,
    c.last_tested_at, c.last_test_ok, c.last_test_latency_ms, c.last_test_error, c.credential_valid,
    c.config_revision, c.admission_limits_revision,
    pe.name AS provider_endpoint_name, pe.status AS provider_endpoint_status,
    p.name AS provider_name
FROM channels c
JOIN providers p ON p.id = c.provider_id
JOIN provider_endpoints pe ON pe.id = c.provider_endpoint_id
WHERE (sqlc.narg('provider_id')::bigint IS NULL OR c.provider_id = sqlc.narg('provider_id')::bigint)
  AND (sqlc.narg('status')::text IS NULL OR c.status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR c.name ILIKE '%' || sqlc.narg('q')::text || '%'
    OR pe.base_url ILIKE '%' || sqlc.narg('q')::text || '%'
  )
ORDER BY c.priority, c.id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountChannels :one
-- CountChannels 返回与 ListChannelsPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM channels c
JOIN provider_endpoints pe ON pe.id = c.provider_endpoint_id
WHERE (sqlc.narg('provider_id')::bigint IS NULL OR c.provider_id = sqlc.narg('provider_id')::bigint)
  AND (sqlc.narg('status')::text IS NULL OR c.status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR c.name ILIKE '%' || sqlc.narg('q')::text || '%'
    OR pe.base_url ILIKE '%' || sqlc.narg('q')::text || '%'
  );

-- name: GetChannel :one
-- GetChannel 按 id 读取单个 channel。
SELECT id, provider_id, provider_endpoint_id, name, protocol, adapter_key, credential, config_revision, admission_limits_revision, status, priority, timeout_ms, created_at, updated_at, rpm_limit, tpm_limit, rpd_limit, last_tested_at, last_test_ok, last_test_latency_ms, last_test_error, credential_valid, archived_at, concurrency_limit, upstream_bills_on_disconnect
FROM channels
WHERE id = $1
LIMIT 1;

-- name: GetChannelProbeSnapshot :one
-- GetChannelProbeSnapshot 一次读取主动检测所需的 Channel、Provider 与 Endpoint 冻结事实；检测结果只允许按三类 revision CAS 回写。
SELECT
    c.id AS channel_id,
    c.provider_id,
    c.provider_endpoint_id,
    c.protocol,
    c.adapter_key,
    c.credential,
    c.credential_valid,
    c.config_revision,
    p.slug AS provider_slug,
    pe.base_url AS endpoint_base_url,
    pe.base_url_revision AS endpoint_base_url_revision,
    pe.status_revision AS endpoint_status_revision
FROM channels c
JOIN providers p ON p.id = c.provider_id
JOIN provider_endpoints pe ON pe.id = c.provider_endpoint_id
WHERE c.id = sqlc.arg(channel_id)
LIMIT 1;

-- name: PrepareChannelCredentialRotation :one
-- PrepareChannelCredentialRotation 原子判定同值/真变化并保存 credential。真变化只推进一次 config_revision，
-- 同时先把 credential_valid 置 false、清空只属于旧 credential 的最近检测摘要；随后即时检测按返回的三类 revision 做 CAS。
WITH current AS MATERIALIZED (
    SELECT c.id, c.credential AS previous_credential, c.credential_valid AS previous_credential_valid
    FROM channels AS c
    WHERE c.id = sqlc.arg(channel_id)
    FOR UPDATE
), updated AS (
    UPDATE channels AS c
    SET credential = sqlc.arg(credential),
        credential_valid = CASE
            WHEN current.previous_credential IS DISTINCT FROM sqlc.arg(credential) THEN FALSE
            ELSE c.credential_valid
        END,
        last_tested_at = CASE
            WHEN current.previous_credential IS DISTINCT FROM sqlc.arg(credential) THEN NULL
            ELSE c.last_tested_at
        END,
        last_test_ok = CASE
            WHEN current.previous_credential IS DISTINCT FROM sqlc.arg(credential) THEN NULL
            ELSE c.last_test_ok
        END,
        last_test_latency_ms = CASE
            WHEN current.previous_credential IS DISTINCT FROM sqlc.arg(credential) THEN NULL
            ELSE c.last_test_latency_ms
        END,
        last_test_error = CASE
            WHEN current.previous_credential IS DISTINCT FROM sqlc.arg(credential) THEN NULL
            ELSE c.last_test_error
        END,
        config_revision = c.config_revision + CASE
            WHEN current.previous_credential IS DISTINCT FROM sqlc.arg(credential) THEN 1
            ELSE 0
        END,
        updated_at = CASE
            WHEN current.previous_credential IS DISTINCT FROM sqlc.arg(credential) THEN now()
            ELSE c.updated_at
        END
    FROM current
    WHERE c.id = current.id
    RETURNING
        c.id AS channel_id,
        c.provider_id,
        c.provider_endpoint_id,
        c.protocol,
        c.adapter_key,
        c.credential,
        c.credential_valid,
        c.config_revision,
        (current.previous_credential IS DISTINCT FROM sqlc.arg(credential))::boolean AS credential_changed
)
SELECT
    updated.channel_id,
    updated.provider_id,
    updated.provider_endpoint_id,
    updated.protocol,
    updated.adapter_key,
    updated.credential,
    updated.credential_valid,
    updated.config_revision,
    updated.credential_changed,
    p.slug AS provider_slug,
    pe.base_url AS endpoint_base_url,
    pe.base_url_revision AS endpoint_base_url_revision,
    pe.status_revision AS endpoint_status_revision
FROM updated
JOIN providers p ON p.id = updated.provider_id
JOIN provider_endpoints pe ON pe.id = updated.provider_endpoint_id;

-- name: ApplyChannelProbeResult :one
-- ApplyChannelProbeResult 按 Channel config + Endpoint BaseURL/status 三类 expected revision 原子应用检测摘要与 credential_valid，
-- 并无论 current/stale 都写一条带 tested revisions 的历史日志；stale 结果不覆盖当前摘要或凭据状态。
WITH matching AS MATERIALIZED (
    SELECT c.id, c.credential_valid
    FROM channels c
    JOIN provider_endpoints pe ON pe.id = c.provider_endpoint_id
    WHERE c.id = sqlc.arg(channel_id)
      AND c.config_revision = sqlc.arg(expected_config_revision)
      AND pe.base_url_revision = sqlc.arg(expected_endpoint_base_url_revision)
      AND pe.status_revision = sqlc.arg(expected_endpoint_status_revision)
    FOR UPDATE OF c
), applied AS (
    UPDATE channels AS c
    SET last_tested_at = now(),
        last_test_ok = sqlc.arg(success),
        last_test_latency_ms = sqlc.narg(last_test_latency_ms),
        last_test_error = sqlc.narg(last_test_error),
        credential_valid = COALESCE(sqlc.narg(next_credential_valid)::boolean, c.credential_valid),
        config_revision = c.config_revision + CASE
            WHEN sqlc.narg(next_credential_valid)::boolean IS NOT NULL
             AND c.credential_valid IS DISTINCT FROM sqlc.narg(next_credential_valid)::boolean
            THEN 1 ELSE 0
        END,
        updated_at = CASE
            WHEN sqlc.narg(next_credential_valid)::boolean IS NOT NULL
             AND c.credential_valid IS DISTINCT FROM sqlc.narg(next_credential_valid)::boolean
            THEN now() ELSE c.updated_at
        END
    FROM matching
    WHERE c.id = matching.id
    RETURNING
        c.id,
        c.credential_valid,
        c.config_revision,
        (matching.credential_valid IS DISTINCT FROM c.credential_valid)::boolean AS state_change_applied
), current_state AS (
    SELECT
        applied.id,
        applied.credential_valid,
        applied.config_revision,
        TRUE AS result_applied,
        applied.state_change_applied
    FROM applied
    UNION ALL
    SELECT
        c.id,
        c.credential_valid,
        c.config_revision,
        FALSE AS result_applied,
        FALSE AS state_change_applied
    FROM channels c
    WHERE c.id = sqlc.arg(channel_id)
      AND NOT EXISTS (SELECT 1 FROM applied)
    LIMIT 1
), logged AS (
    INSERT INTO channel_test_logs (
        channel_id,
        source,
        success,
        error_code,
        http_status,
        latency_ms,
        tested_model,
        credential_valid_after,
        message,
        upstream_error,
        tested_endpoint_base_url_revision,
        tested_endpoint_status_revision,
        tested_config_revision,
        state_change_applied
    )
    SELECT
        current_state.id,
        sqlc.arg(source),
        sqlc.arg(success),
        sqlc.narg(error_code),
        sqlc.narg(http_status),
        sqlc.narg(last_test_latency_ms),
        sqlc.narg(tested_model),
        current_state.credential_valid,
        sqlc.narg(last_test_error),
        sqlc.narg(upstream_error),
        sqlc.arg(expected_endpoint_base_url_revision),
        sqlc.arg(expected_endpoint_status_revision),
        sqlc.arg(expected_config_revision),
        current_state.state_change_applied
    FROM current_state
    RETURNING channel_id, credential_valid_after, state_change_applied
)
SELECT
    current_state.result_applied,
    logged.state_change_applied,
    logged.credential_valid_after,
    current_state.config_revision AS current_config_revision
FROM current_state
JOIN logged ON logged.channel_id = current_state.id;

-- name: CreateChannel :one
-- CreateChannel 创建 channel；credential 为明文上游凭据，protocol+adapter_key 复合键须先在 adapter registry 校验存在。
-- 四维限额随业务行一次写入，初始 admission_limits_revision 固定使用表默认值 1；随后同步安装 revision=1 Redis control。
INSERT INTO channels (
    provider_id, provider_endpoint_id, name, protocol, adapter_key, credential, status, priority, timeout_ms,
    rpm_limit, tpm_limit, rpd_limit, concurrency_limit, upstream_bills_on_disconnect
)
VALUES (
    sqlc.arg(provider_id), sqlc.arg(provider_endpoint_id), sqlc.arg(name), sqlc.arg(protocol), sqlc.arg(adapter_key),
    sqlc.arg(credential), sqlc.arg(status), sqlc.arg(priority), sqlc.arg(timeout_ms),
    sqlc.narg(rpm_limit), sqlc.narg(tpm_limit), sqlc.narg(rpd_limit), sqlc.narg(concurrency_limit),
    sqlc.arg(upstream_bills_on_disconnect)
)
RETURNING id, provider_id, provider_endpoint_id, name, protocol, adapter_key, credential, config_revision, admission_limits_revision, status, priority, timeout_ms, created_at, updated_at, rpm_limit, tpm_limit, rpd_limit, last_tested_at, last_test_ok, last_test_latency_ms, last_test_error, credential_valid, archived_at, concurrency_limit, upstream_bills_on_disconnect;

-- name: UpdateChannel :one
-- UpdateChannel 更新 channel 的展示名、绑定 Endpoint、启停状态、优先级与超时；protocol、adapter_key 与凭据不在此更新。
-- [P4 §4.4] base_url 已移除（地址归 provider_endpoints）；config_revision 递增由服务层在真变化时于同事务处理。
UPDATE channels
SET name = sqlc.arg(name),
    provider_endpoint_id = sqlc.arg(provider_endpoint_id),
    status = sqlc.arg(status),
    priority = sqlc.arg(priority),
    timeout_ms = sqlc.arg(timeout_ms),
    config_revision = config_revision + (
        CASE WHEN (
            provider_endpoint_id IS DISTINCT FROM sqlc.arg(provider_endpoint_id)
            OR status IS DISTINCT FROM sqlc.arg(status)
            OR timeout_ms IS DISTINCT FROM sqlc.arg(timeout_ms)
        ) THEN 1 ELSE 0 END
    ),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, provider_id, provider_endpoint_id, name, protocol, adapter_key, credential, config_revision, admission_limits_revision, status, priority, timeout_ms, created_at, updated_at, rpm_limit, tpm_limit, rpd_limit, last_tested_at, last_test_ok, last_test_latency_ms, last_test_error, credential_valid, archived_at, concurrency_limit, upstream_bills_on_disconnect;

-- name: CommitChannelAdmissionLimitsAtRevision :one
-- CommitChannelAdmissionLimitsAtRevision 只供 runtimecontrol.Publisher 的 BusinessCommit 事务调用：
-- 在 expected revision 仍匹配且四维语义真变化时一次写值并推进 admission_limits_revision。
-- 普通 Channel 更新不得调用无 revision 的直接覆盖查询。
UPDATE channels
SET rpm_limit = sqlc.narg(rpm_limit),
    tpm_limit = sqlc.narg(tpm_limit),
    rpd_limit = sqlc.narg(rpd_limit),
    concurrency_limit = sqlc.narg(concurrency_limit),
    admission_limits_revision = sqlc.arg(next_revision),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND admission_limits_revision = sqlc.arg(current_revision)
  AND sqlc.arg(next_revision) = sqlc.arg(current_revision) + 1
  AND ROW(rpm_limit, tpm_limit, rpd_limit, concurrency_limit) IS DISTINCT FROM ROW(
      sqlc.narg(rpm_limit), sqlc.narg(tpm_limit), sqlc.narg(rpd_limit), sqlc.narg(concurrency_limit)
  )
RETURNING id, provider_id, provider_endpoint_id, name, protocol, adapter_key, credential, config_revision, admission_limits_revision, status, priority, timeout_ms, created_at, updated_at, rpm_limit, tpm_limit, rpd_limit, last_tested_at, last_test_ok, last_test_latency_ms, last_test_error, credential_valid, archived_at, concurrency_limit, upstream_bills_on_disconnect;

-- name: SetChannelBillingBehavior :one
-- SetChannelBillingBehavior 设置渠道「断开仍计费」标记（DESIGN-bill-on-cancel 阶段一）。
-- true 表示上游在连接断开后仍会完成生成并计费（sub2api 类中转）；打开后失败/取消路径会记成本敞口。
UPDATE channels
SET upstream_bills_on_disconnect = sqlc.arg(upstream_bills_on_disconnect), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, provider_id, provider_endpoint_id, name, protocol, adapter_key, credential, config_revision, admission_limits_revision, status, priority, timeout_ms, created_at, updated_at, rpm_limit, tpm_limit, rpd_limit, last_tested_at, last_test_ok, last_test_latency_ms, last_test_error, credential_valid, archived_at, concurrency_limit, upstream_bills_on_disconnect;

-- name: SetChannelTestResult :execrows
-- SetChannelTestResult 写入渠道「最近一次主动检测结果」（渠道检测，阶段一）。
-- last_tested_at 用 DB now() 保证服务器时钟；latency 恒有值（连接失败也测到耗时）；
-- error 成功时为 NULL、失败时为可读原因。不改 updated_at（检测是运营遥测，非配置变更），
-- 也不改 status（阶段一只报告不摘除）。返回受影响行数用于判定渠道是否存在。
UPDATE channels
SET last_tested_at = now(),
    last_test_ok = sqlc.arg(last_test_ok),
    last_test_latency_ms = sqlc.arg(last_test_latency_ms),
    last_test_error = sqlc.narg(last_test_error)
WHERE id = sqlc.arg(id);

-- name: UpdateChannelCredential :execrows
-- UpdateChannelCredential 更新 channel 的明文上游凭据；返回受影响行数用于判定 channel 是否存在。
UPDATE channels
SET credential = sqlc.arg(credential), updated_at = now()
WHERE id = sqlc.arg(id);

-- name: SetChannelCredentialValid :execrows
-- SetChannelCredentialValid 将渠道恢复为「凭据有效」。幂等：仅在 false→true 跳变时写入并返回受影响行数=1。
UPDATE channels
SET credential_valid = TRUE
WHERE id = sqlc.arg(id) AND credential_valid = FALSE;

-- name: ArchiveChannelCascade :execrows
-- ArchiveChannelCascade 归档单个渠道：从所有线路候选池移除（删 route_channels 行）、置 archived、
-- 释放渠道名（追加 __archived_<id> 后缀释放 (provider_id, name) 槽位供复用）。不动 provider。
-- 返回 channels 受影响行数（0 = 渠道不存在或已归档）。恢复保持后缀名、不自动重加线路池。
WITH cleared_pools AS (
    DELETE FROM route_channels WHERE channel_id = sqlc.arg(id)
)
UPDATE channels
SET status = 'archived', archived_at = now(), name = name || '__archived_' || id::text,
    config_revision = config_revision + 1, updated_at = now()
WHERE channels.id = sqlc.arg(id) AND channels.status <> 'archived';

-- name: ArchiveChannelWithReplacement :one
-- Atomically add one healthy replacement to every affected route, then archive/remove the target.
WITH replacement AS (
    SELECT c.id
    FROM channels c
    JOIN providers p ON p.id = c.provider_id
    JOIN provider_endpoints pe ON pe.id = c.provider_endpoint_id
    WHERE c.id = sqlc.arg(replacement_channel_id)
      AND c.id <> sqlc.arg(id)
      AND c.status = 'enabled'
      AND c.credential_valid
      AND c.credential <> ''
      AND pe.base_url <> ''
      AND pe.status = 'enabled'
      AND p.status = 'enabled'
),
affected_routes AS (
    SELECT route_id FROM route_channels WHERE channel_id = sqlc.arg(id)
),
added AS (
    INSERT INTO route_channels (route_id, channel_id)
    SELECT ar.route_id, replacement.id
    FROM affected_routes ar CROSS JOIN replacement
    ON CONFLICT (route_id, channel_id) DO NOTHING
    RETURNING route_id
),
archived AS (
    UPDATE channels
    SET status = 'archived', archived_at = now(), name = name || '__archived_' || id::text,
        config_revision = config_revision + 1, updated_at = now()
    WHERE channels.id = sqlc.arg(id)
      AND channels.status <> 'archived'
      AND EXISTS (SELECT 1 FROM replacement)
      AND (SELECT COUNT(*) FROM added) >= 0
    RETURNING id
),
cleared AS (
    DELETE FROM route_channels WHERE channel_id IN (SELECT id FROM archived)
    RETURNING route_id
)
SELECT COUNT(*)::bigint FROM archived WHERE (SELECT COUNT(*) FROM cleared) >= 0;

-- name: ListEnabledRoutesEmptiedByChannel :many
-- 归档目标渠道后将失去最后一个显式池成员的启用线路；归档前必须先替换渠道或停用线路。
SELECT rt.id, rt.name
FROM routes rt
JOIN route_channels target ON target.route_id = rt.id AND target.channel_id = sqlc.arg(channel_id)
WHERE rt.status = 'enabled'
  AND NOT EXISTS (
      SELECT 1 FROM route_channels other
      WHERE other.route_id = rt.id AND other.channel_id <> sqlc.arg(channel_id)
  )
ORDER BY rt.id;

-- name: RestoreChannel :execrows
-- RestoreChannel 取消归档渠道：archived → disabled（archived_at 清空）。名字保持归档时的后缀名
-- （如需干净名由管理员手动改）。调用方需先保证所属 provider 非 archived（服务层护栏）。
UPDATE channels
SET status = 'disabled', archived_at = NULL,
    config_revision = config_revision + 1, updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'archived';

-- name: DeleteChannelCascade :execrows
-- DeleteChannelCascade 物理删除 channel，用于清理录错且从未使用的脏数据，并在同一条语句内
-- 级联清理 channel 自身的全部配置子表：channel_models（模型绑定）、channel_prices（渠道-模型价，
-- 绝对成本覆盖）、channel_cost_multipliers（价格倍率，DEC-027）、channel_recharge_factors（充值倍率，DEC-027）。
-- 这四张都是「渠道自身配置」（无请求/账务事实），随渠道硬删一并清理；channel_test_logs 走 ON DELETE CASCADE 自动清。
-- 外键均为默认 NO ACTION（约束在语句末校验），故 CTE 删子表 + 删主体在单条语句内原子完成：
-- 子配置先删除，语句末 channels 的删除不会留下悬挂引用。若 channel 仍被请求/账务历史
-- （request_attempts/request_records/cost_snapshots/settlement_recovery_jobs/channel_cost_exposures）引用，
-- 整条语句报 23503 全部回滚，上层降级为 conflict，提示改用停用/保持归档。返回值为 channels 行的受影响数（0 表示 channel 不存在）。
-- 注：归档时已从 route_channels 线路池移除（ArchiveChannelCascade），故此处无需再清线路池。
WITH deleted_channel_prices AS (
    DELETE FROM channel_prices WHERE channel_prices.channel_id = sqlc.arg(id)
),
deleted_channel_models AS (
    DELETE FROM channel_models WHERE channel_models.channel_id = sqlc.arg(id)
),
deleted_channel_cost_multipliers AS (
    DELETE FROM channel_cost_multipliers WHERE channel_cost_multipliers.channel_id = sqlc.arg(id)
),
deleted_channel_recharge_factors AS (
    DELETE FROM channel_recharge_factors WHERE channel_recharge_factors.channel_id = sqlc.arg(id)
)
DELETE FROM channels WHERE channels.id = sqlc.arg(id);

-- §3.3 渠道作战台只读运维聚合。全部只读。
-- 口径：渠道性能/成功率/错误以 request_attempts（attempt 粒度，每次尝试命中一条渠道）为准；
-- TPS/token 因无 per-attempt usage，按 request_records.final_channel_id 归因（最终成功渠道）。
-- 区间 [from,to) 半开；narg 可空（NULL 不过滤）。延迟由 completed_at-started_at 推导（毫秒）。

-- name: ChannelsOpsTable :many
-- ChannelsOpsTable 渠道运维主表（分页）：每渠道 attempt 指标 + 绑定模型数 + 最近错误，默认最需处理优先。
SELECT
    c.id,
    c.name,
    c.status,
    c.protocol,
    c.adapter_key,
    pe.base_url,
    c.priority,
    c.timeout_ms,
    c.credential,
    c.rpm_limit,
    c.tpm_limit,
    c.rpd_limit,
    c.created_at,
    c.last_tested_at,
    c.last_test_ok,
    c.last_test_latency_ms,
    c.last_test_error,
    c.credential_valid,
    (
        SELECT ccm.multiplier
        FROM channel_cost_multipliers ccm
        WHERE ccm.channel_id = c.id
          AND ccm.model_id IS NULL
          AND ccm.status = 'enabled'
          AND ccm.effective_from <= now()
          AND (ccm.effective_to IS NULL OR ccm.effective_to > now())
        ORDER BY ccm.effective_from DESC, ccm.id DESC
        LIMIT 1
    ) AS cost_multiplier,
    (
        SELECT COUNT(*)::bigint
        FROM channel_cost_multipliers ccm
        WHERE ccm.channel_id = c.id
          AND ccm.model_id IS NOT NULL
          AND ccm.status = 'enabled'
          AND ccm.effective_from <= now()
          AND (ccm.effective_to IS NULL OR ccm.effective_to > now())
    ) AS cost_multiplier_overrides,
    (
        SELECT crf.factor
        FROM channel_recharge_factors crf
        WHERE crf.channel_id = c.id
          AND crf.status = 'enabled'
          AND crf.effective_from <= now()
          AND (crf.effective_to IS NULL OR crf.effective_to > now())
        ORDER BY crf.effective_from DESC, crf.id DESC
        LIMIT 1
    ) AS recharge_factor,
    pr.name AS provider_name,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream') AS attempt_total,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS attempt_succeeded,
    COUNT(a.id) FILTER (WHERE a.status = 'failed' AND (a.error_code ILIKE '%timeout%' OR a.error_code = 'context_deadline_exceeded')) AS timeout_total,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded' AND a.completed_at IS NOT NULL) AS latency_sample,
    COALESCE(AVG(CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p50,
    COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p90,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p99,
    (SELECT COUNT(*) FROM channel_models cm WHERE cm.channel_id = c.id AND cm.status = 'enabled') AS bound_models,
    (SELECT COUNT(*) FROM route_channels rc WHERE rc.channel_id = c.id) AS bound_routes,
    (
        SELECT a2.error_code FROM request_attempts a2
        WHERE a2.channel_id = c.id AND a2.status = 'failed' AND a2.fault_party = 'upstream' AND a2.error_code IS NOT NULL
          AND (sqlc.narg('from_time')::timestamptz IS NULL OR a2.created_at >= sqlc.narg('from_time')::timestamptz)
          AND (sqlc.narg('to_time')::timestamptz IS NULL OR a2.created_at < sqlc.narg('to_time')::timestamptz)
        ORDER BY a2.created_at DESC LIMIT 1
    ) AS recent_error_code
FROM channels c
JOIN providers pr ON pr.id = c.provider_id
JOIN provider_endpoints pe ON pe.id = c.provider_endpoint_id
LEFT JOIN request_attempts a
    ON a.channel_id = c.id
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
WHERE (sqlc.narg('status')::text IS NULL OR c.status = sqlc.narg('status')::text)
  AND (sqlc.narg('provider_id')::bigint IS NULL OR c.provider_id = sqlc.narg('provider_id')::bigint)
  AND (sqlc.narg('search')::text IS NULL OR c.name ILIKE '%' || sqlc.narg('search')::text || '%')
GROUP BY c.id, c.name, c.status, c.protocol, c.adapter_key, pe.base_url, c.priority, c.timeout_ms, c.credential, c.rpm_limit, c.tpm_limit, c.rpd_limit, c.created_at, c.last_tested_at, c.last_test_ok, c.last_test_latency_ms, c.last_test_error, c.credential_valid, pr.name
ORDER BY
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'success_rate') IN ('', 'success_rate') AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (COUNT(a.id) FILTER (WHERE a.status = 'succeeded')::float8 / NULLIF(COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream'), 0)) END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'success_rate') IN ('', 'success_rate') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (COUNT(a.id) FILTER (WHERE a.status = 'succeeded')::float8 / NULLIF(COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream'), 0)) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'name' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN c.name END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'name' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN c.name END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'requests' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream') END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'requests' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream') END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN c.status END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN c.status END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'credential_valid' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN c.credential_valid END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'credential_valid' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN c.credential_valid END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN c.created_at END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN c.created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'latency' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COALESCE(AVG(CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'latency' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COALESCE(AVG(CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'timeout' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COUNT(a.id) FILTER (WHERE a.status = 'failed' AND (a.error_code ILIKE '%timeout%' OR a.error_code = 'context_deadline_exceeded')) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'timeout' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN COUNT(a.id) FILTER (WHERE a.status = 'failed' AND (a.error_code ILIKE '%timeout%' OR a.error_code = 'context_deadline_exceeded')) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'bound_models' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (SELECT COUNT(*) FROM channel_models cm WHERE cm.channel_id = c.id AND cm.status = 'enabled') END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'bound_models' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (SELECT COUNT(*) FROM channel_models cm WHERE cm.channel_id = c.id AND cm.status = 'enabled') END ASC NULLS LAST,
  c.id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ChannelsOpsTableCount :one
-- ChannelsOpsTableCount 与 ChannelsOpsTable 同过滤条件下的渠道总数。
SELECT COUNT(*) AS total
FROM channels c
WHERE (sqlc.narg('status')::text IS NULL OR c.status = sqlc.narg('status')::text)
  AND (sqlc.narg('provider_id')::bigint IS NULL OR c.provider_id = sqlc.narg('provider_id')::bigint)
  AND (sqlc.narg('search')::text IS NULL OR c.name ILIKE '%' || sqlc.narg('search')::text || '%');

-- name: ChannelOpsDetail :one
-- ChannelOpsDetail 单渠道（抽屉概览）attempt 指标。attempt_total 口径同上：合格 attempt（succeeded+failed）。
SELECT
    COUNT(*) FILTER (WHERE status = 'succeeded' OR fault_party = 'upstream') AS attempt_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS attempt_succeeded,
    COUNT(*) FILTER (WHERE status = 'failed' AND (error_code ILIKE '%timeout%' OR error_code = 'context_deadline_exceeded')) AS timeout_total,
    COUNT(*) FILTER (WHERE status = 'succeeded' AND completed_at IS NOT NULL) AS latency_sample,
    COALESCE(AVG(CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p50,
    COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p90,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_p99,
    (MAX(completed_at) FILTER (WHERE status = 'succeeded'))::timestamptz AS last_success_at,
    (MAX(completed_at) FILTER (WHERE status = 'failed' AND fault_party = 'upstream'))::timestamptz AS last_failure_at
FROM request_attempts
WHERE channel_id = sqlc.arg('channel_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz);

-- name: ChannelOpsPerformanceTimeseries :many
-- ChannelOpsPerformanceTimeseries 单渠道按时间桶的 attempt 量/成功/平均延迟（抽屉性能 Tab）。
SELECT
    date_trunc(sqlc.arg('unit')::text, created_at)::timestamptz AS bucket,
    COUNT(*) FILTER (WHERE status = 'succeeded' OR fault_party = 'upstream') AS attempt_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS attempt_succeeded,
    COALESCE(AVG(CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg
FROM request_attempts
WHERE channel_id = sqlc.arg('channel_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY bucket
ORDER BY bucket;

-- name: ChannelOpsErrors :many
-- ChannelOpsErrors 单渠道错误明细（抽屉错误 Tab，分页）。携带 request_id 便于跳证据中心。
SELECT
    a.created_at,
    a.upstream_model,
    a.error_code,
    a.upstream_status_code,
    a.error_message,
    r.request_id
FROM request_attempts a
JOIN request_records r ON r.id = a.request_record_id
WHERE a.channel_id = sqlc.arg('channel_id')
  AND a.status = 'failed'
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY a.created_at DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ChannelOpsErrorsCount :one
SELECT COUNT(*) AS total
FROM request_attempts a
WHERE a.channel_id = sqlc.arg('channel_id')
  AND a.status = 'failed'
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz);

-- name: ChannelOpsModels :many
-- ChannelOpsModels 单渠道绑定模型 + attempt 指标（抽屉模型 Tab，完整列 §1.8）。
-- attempt 无 model_id，按 upstream_model 关联绑定。
SELECT
    cm.model_id,
    m.model_id AS model_ref,
    m.display_name,
    cm.upstream_model,
    cm.status,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream') AS attempt_total,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS attempt_succeeded,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded' AND a.completed_at IS NOT NULL) AS latency_sample,
    COALESCE(AVG(CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p50,
    COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p90,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p99,
    -- has_price（DEC-031）：该 (渠道,模型) 可解析成本——绝对覆盖 OR （模型有生效基准价 AND 渠道对本模型有生效价格倍率，含默认）；与路由「可卖」对齐。
    -- 用单一顶层 EXISTS (SELECT 1 WHERE <条件>) 让 sqlc 推断为非空 bool（复合布尔/COALESCE 会被推断为可空或 interface{}）。
    EXISTS (
        SELECT 1 WHERE
            EXISTS (
                SELECT 1 FROM channel_prices p
                WHERE p.channel_id = sqlc.arg('channel_id') AND p.model_id = cm.model_id AND p.status = 'enabled'
                  AND p.effective_from <= now() AND (p.effective_to IS NULL OR p.effective_to > now())
            )
            OR (
                EXISTS (
                    SELECT 1 FROM model_prices mp
                    WHERE mp.model_id = cm.model_id AND mp.status = 'enabled'
                      AND mp.effective_from <= now() AND (mp.effective_to IS NULL OR mp.effective_to > now())
                )
                AND EXISTS (
                    SELECT 1 FROM channel_cost_multipliers ccm
                    WHERE ccm.channel_id = sqlc.arg('channel_id')
                      AND (ccm.model_id = cm.model_id OR ccm.model_id IS NULL)
                      AND ccm.status = 'enabled'
                      AND ccm.effective_from <= now() AND (ccm.effective_to IS NULL OR ccm.effective_to > now())
                )
            )
    ) AS has_price
FROM channel_models cm
JOIN models m ON m.id = cm.model_id
LEFT JOIN request_attempts a
    ON a.channel_id = cm.channel_id
    AND a.upstream_model = cm.upstream_model
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
WHERE cm.channel_id = sqlc.arg('channel_id')
GROUP BY cm.model_id, m.model_id, m.display_name, cm.upstream_model, cm.status
ORDER BY attempt_total DESC, m.model_id;

-- name: ChannelOpsRoutes :many
-- ChannelOpsRoutes 引用该渠道的线路池（抽屉线路 Tab）。
SELECT rt.id, rt.name, rt.mode, rt.status, rt.price_ratio
FROM route_channels rc
JOIN routes rt ON rt.id = rc.route_id
WHERE rc.channel_id = sqlc.arg('channel_id')
ORDER BY rt.id;
