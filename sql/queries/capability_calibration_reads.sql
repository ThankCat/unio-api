-- name: ScanSucceededAttemptsForCalibration :many
-- ScanSucceededAttemptsForCalibration 增量拉取成功尝试的能力使用与强证据线索（worker 聚合用）。
-- 经 request_records.requested_model_id 反查 Unio 模型，消解 (channel, upstream_model) 多模型歧义；
-- LEFT JOIN usage_records 取 cache/reasoning 证据（无 usage 行按 0）。
SELECT
    a.id AS attempt_id,
    m.id AS model_id,
    a.channel_id AS channel_id,
    a.finish_class AS finish_class,
    a.required_capabilities AS required_capabilities,
    COALESCE(u.cache_read_input_tokens, 0)::BIGINT AS cache_read_input_tokens,
    COALESCE(u.reasoning_output_tokens, 0)::BIGINT AS reasoning_output_tokens
FROM request_attempts a
JOIN request_records r ON r.id = a.request_record_id
JOIN models m ON m.model_id = r.requested_model_id
LEFT JOIN usage_records u ON u.request_record_id = a.request_record_id
WHERE a.id > sqlc.arg(after_attempt_id)
    AND a.status = 'succeeded'
    AND a.created_at >= sqlc.arg(since)
ORDER BY a.id ASC
LIMIT sqlc.arg(max_rows);

-- name: ListModelsForCalibration :many
-- ListModelsForCalibration 列出启用模型及其自动校正档位（决策输入）。
SELECT id, model_id, capability_autocalibrate
FROM models
WHERE status = 'enabled'
ORDER BY id ASC;

-- name: ListEnabledChannelModelCounts :many
-- ListEnabledChannelModelCounts 统计每个模型当前启用且渠道启用的可服务渠道数（多渠道安全判定用）。
SELECT cm.model_id AS model_id, COUNT(DISTINCT cm.channel_id) AS channel_count
FROM channel_models cm
JOIN channels c ON c.id = cm.channel_id
WHERE cm.status = 'enabled'
    AND c.status = 'enabled'
GROUP BY cm.model_id;
