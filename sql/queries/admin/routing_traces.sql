-- 以下三个聚合共用同一个窗口口径：按 requested_model_id（本表冗余列，不必 JOIN
-- request_records）加 created_at 范围，命中 idx_routing_decision_traces_model_created。
-- 只统计 trace_status='complete'：partial 是进行中或进程崩溃遗留，payload 不完整；
-- legacy_sampled 是改造前的采样行，schema 不同。

-- name: ModelRoutingSelectionStats :many
-- ModelRoutingSelectionStats 统计流量最终落在哪条渠道。
-- selected_channel_id 为 NULL 表示这次选路没能选出渠道（无可用候选等），单独归一行。
SELECT
    t.selected_channel_id,
    COUNT(*) AS selections
FROM routing_decision_traces t
WHERE t.requested_model_id = sqlc.arg(requested_model_id)
  AND t.trace_status = 'complete'
  AND t.created_at >= sqlc.arg(from_time)::timestamptz
  AND t.created_at < sqlc.arg(to_time)::timestamptz
GROUP BY 1
ORDER BY selections DESC, t.selected_channel_id;

-- name: ModelRoutingOutcomeStats :many
-- ModelRoutingOutcomeStats 统计选路终态分布（success / no_available_channel / ...）。
SELECT
    COALESCE(t.final_result, 'unknown')::text AS final_result,
    COUNT(*) AS occurrences
FROM routing_decision_traces t
WHERE t.requested_model_id = sqlc.arg(requested_model_id)
  AND t.trace_status = 'complete'
  AND t.created_at >= sqlc.arg(from_time)::timestamptz
  AND t.created_at < sqlc.arg(to_time)::timestamptz
GROUP BY 1
ORDER BY occurrences DESC, 1;

-- name: ModelRoutingExclusionStats :many
-- ModelRoutingExclusionStats 统计候选为什么没被用上。
--
-- 成本提示：trace_payload.candidates 装的是全池非 archived 渠道，不是该模型的绑定渠道，
-- 因此展开后的元素数约等于「选路次数 × 全池渠道数」。调用方必须限制时间窗（当前 24 小时），
-- 否则这条查询会随保留期线性变慢。
--
-- sample_channel_id 取该原因下出现次数最多的渠道，用于在界面上指认「主要是谁」。
WITH exclusions AS (
    SELECT
        candidate.value->>'excluded_reason' AS excluded_reason,
        (candidate.value->>'channel_id')::bigint AS channel_id
    FROM routing_decision_traces t
    CROSS JOIN LATERAL jsonb_array_elements(
        COALESCE(t.trace_payload->'candidates', '[]'::jsonb)
    ) AS candidate(value)
    WHERE t.requested_model_id = sqlc.arg(requested_model_id)
      AND t.trace_status = 'complete'
      AND t.created_at >= sqlc.arg(from_time)::timestamptz
      AND t.created_at < sqlc.arg(to_time)::timestamptz
      AND COALESCE((candidate.value->>'eligible')::boolean, false) = false
      AND COALESCE(candidate.value->>'excluded_reason', '') <> ''
), per_reason_channel AS (
    SELECT excluded_reason, channel_id, COUNT(*) AS hits
    FROM exclusions
    GROUP BY 1, 2
)
SELECT
    e.excluded_reason::text AS excluded_reason,
    SUM(e.hits)::bigint AS occurrences,
    ((array_agg(e.channel_id ORDER BY e.hits DESC, e.channel_id))[1])::bigint AS sample_channel_id,
    COUNT(DISTINCT e.channel_id)::bigint AS channels_touched
FROM per_reason_channel e
GROUP BY 1
ORDER BY occurrences DESC, 1;

-- name: ModelRoutingTraceList :many
-- ModelRoutingTraceList 是最近选路列表（分页），供逐条下钻到完整 trace。
-- candidate_count 用 payload 里的候选数而非 pool_size：后者是全池渠道数，
-- 与界面上「候选 N 条」不是一回事。
SELECT
    t.created_at,
    t.final_result,
    t.eligible_count,
    COALESCE(jsonb_array_length(t.trace_payload->'candidates'), 0)::int AS candidate_count,
    t.selected_channel_id,
    t.fallback_count,
    t.capacity_wait_result,
    r.request_id
FROM routing_decision_traces t
JOIN request_records r ON r.id = t.request_record_id
WHERE t.requested_model_id = sqlc.arg(requested_model_id)
  AND t.created_at >= sqlc.arg(from_time)::timestamptz
  AND t.created_at < sqlc.arg(to_time)::timestamptz
ORDER BY t.created_at DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: LiveTrafficChannels :many
-- LiveTrafficChannels 列出参与流量的渠道及其静态事实，运行态由 Redis 补齐。
-- 归档渠道不出现；停用渠道保留，因为「配了却不在跑」本身是要看的信息。
SELECT
    c.id AS channel_id,
    c.name AS channel_name,
    c.status AS channel_status,
    c.credential_valid,
    c.priority,
    c.concurrency_limit,
    p.id AS provider_id,
    p.name AS provider_name,
    p.status AS provider_status,
    (
        SELECT COUNT(*) FROM channel_models cm
        WHERE cm.channel_id = c.id AND cm.status = 'enabled'
    ) AS bound_models
FROM channels c
JOIN providers p ON p.id = c.provider_id
WHERE c.status <> 'archived'
ORDER BY c.id;

-- name: LiveTrafficModels :many
-- LiveTrafficModels 统计本分钟各模型的请求量。
-- 与渠道侧的 Redis 观测不同源：模型维度的 in-flight 需要改准入热路径才能统计，
-- 不值得为一个展示字段承担那个风险，所以这里只给已落库的请求数。
SELECT
    r.requested_model_id,
    COUNT(*) FILTER (WHERE r.status IN ('succeeded', 'failed')) AS request_total,
    COUNT(*) FILTER (WHERE r.status = 'succeeded') AS request_succeeded
FROM request_records r
WHERE r.created_at >= sqlc.arg(from_time)::timestamptz
GROUP BY 1
HAVING COUNT(*) > 0
ORDER BY request_total DESC
LIMIT sqlc.arg(row_limit);

-- name: ListChannelNames :many
-- ListChannelNames 按 id 批量取渠道名，供选路统计把 channel_id 翻译成可读名称。
SELECT c.id, c.name, c.status
FROM channels c
WHERE c.id = ANY(sqlc.arg(channel_ids)::bigint[])
ORDER BY c.id;

-- name: ModelRoutingTraceCount :one
SELECT COUNT(*) AS total
FROM routing_decision_traces t
WHERE t.requested_model_id = sqlc.arg(requested_model_id)
  AND t.created_at >= sqlc.arg(from_time)::timestamptz
  AND t.created_at < sqlc.arg(to_time)::timestamptz;

-- name: GetRoutingDecisionTraceByRequestID :one
SELECT
    t.id,
    t.request_record_id,
    t.mode,
    t.requested_model_id,
    t.protocol,
    t.endpoint,
    t.trace_status,
    t.schema_version,
    t.algorithm_version,
    t.pool_size,
    t.eligible_count,
    t.baseline_order,
    t.actual_scan_order,
    t.attempted_channel_ids,
    t.selected_channel_id,
    t.fallback_count,
    t.final_result,
    t.sticky_key_present,
    t.sticky_before_channel_id,
    t.sticky_before_version,
    t.sticky_action,
    t.sticky_reason,
    t.sticky_after_channel_id,
    t.sticky_after_version,
    t.capacity_wait_ms,
    t.capacity_wait_result,
    t.trace_payload,
    t.created_at,
    t.updated_at,
    r.request_id,
    r.status AS request_status,
    r.final_channel_id
FROM routing_decision_traces t
JOIN request_records r ON r.id = t.request_record_id
WHERE r.request_id = sqlc.arg(request_id)
LIMIT 1;
