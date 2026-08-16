-- name: ListRequestRecordsPage :many
-- ListRequestRecordsPage 供 admin 请求记录列表（富化版）按过滤条件分页倒序列出。
-- 关联（均 1:1 或标量子查询，不放大行数）：usage_records（token）、cost_snapshots（平台成本 + 分项）、
-- ledger_entries 净扣费（用户实际扣费）、api_keys→routes（线路名，当前绑定，快照见批二）、
-- final channel 名、经过的渠道链（attempts 按序 string_agg）、routing_decision_traces（sticky 摘要）。
-- 列表故意不 SELECT internal_error_detail（SQL 层脱敏，详情上游源站按 ?include_internal 返回）。
-- latency/ttft/tps 由 Go 侧用时间戳 + output_tokens 计算，不在此列。
-- 线路名优先用请求级快照 route_id（Key 换绑不影响历史）；历史行 route_id 为 NULL 时回落到 Key 当前绑定。
-- 模型元信息（显示名 / owned_by）按请求模型 id 关联；请求模型不在库时为 NULL。
-- 先在 request_records 上完成过滤、精确计数与分页，再只对当前页执行富字段 JOIN/子查询；
-- 避免 COUNT(*) OVER() 迫使数据库为全部匹配请求构造完整富化结果。
WITH filtered_page AS (
    SELECT
        r.id,
        COUNT(*) OVER()::bigint AS total_count
    FROM request_records r
    WHERE (sqlc.narg('user_id')::bigint IS NULL OR r.user_id = sqlc.narg('user_id')::bigint)
      AND (sqlc.narg('api_key_id')::bigint IS NULL OR r.api_key_id = sqlc.narg('api_key_id')::bigint)
      AND (sqlc.narg('request_id')::text IS NULL OR r.request_id = sqlc.narg('request_id')::text)
      AND (sqlc.narg('status')::text IS NULL OR r.status = sqlc.narg('status')::text)
      AND (sqlc.narg('model')::text IS NULL OR r.requested_model_id ILIKE '%' || sqlc.narg('model')::text || '%')
      AND (sqlc.narg('route_id')::bigint IS NULL OR r.route_id = sqlc.narg('route_id')::bigint)
      AND (
          (sqlc.narg('channel_id')::bigint IS NULL AND sqlc.narg('attempt_id')::bigint IS NULL AND sqlc.narg('scoring_sample')::text IS NULL)
          OR EXISTS (
              SELECT 1
              FROM request_attempts a
              WHERE a.request_record_id = r.id
                AND (sqlc.narg('channel_id')::bigint IS NULL OR a.channel_id = sqlc.narg('channel_id')::bigint)
                AND (sqlc.narg('attempt_id')::bigint IS NULL OR a.id = sqlc.narg('attempt_id')::bigint)
                AND (
                    sqlc.narg('scoring_sample')::text IS NULL
                    OR (sqlc.narg('scoring_sample')::text = 'ttft' AND a.ttft_scoring_sample)
                    OR (sqlc.narg('scoring_sample')::text = 'error' AND a.error_scoring_sample)
                    OR (sqlc.narg('scoring_sample')::text = 'any' AND (a.ttft_scoring_sample OR a.error_scoring_sample))
                )
          )
      )
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
    ORDER BY
      CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'created_at') IN ('', 'created_at') AND COALESCE(sqlc.narg('sort_desc')::bool, true) THEN r.created_at END DESC NULLS LAST,
      CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'created_at') IN ('', 'created_at') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, true) THEN r.created_at END ASC NULLS LAST,
      CASE WHEN sqlc.narg('sort_field')::text = 'status' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.status END DESC NULLS LAST,
      CASE WHEN sqlc.narg('sort_field')::text = 'status' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.status END ASC NULLS LAST,
      CASE WHEN sqlc.narg('sort_field')::text = 'user_id' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.user_id END DESC NULLS LAST,
      CASE WHEN sqlc.narg('sort_field')::text = 'user_id' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.user_id END ASC NULLS LAST,
      CASE WHEN sqlc.narg('sort_field')::text = 'model' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.requested_model_id END DESC NULLS LAST,
      CASE WHEN sqlc.narg('sort_field')::text = 'model' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.requested_model_id END ASC NULLS LAST,
      CASE WHEN sqlc.narg('sort_field')::text = 'stream' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.stream END DESC NULLS LAST,
      CASE WHEN sqlc.narg('sort_field')::text = 'stream' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.stream END ASC NULLS LAST,
      r.id DESC
    LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset')
)
SELECT
    fp.total_count,
    r.id,
    r.request_id,
    r.user_id,
    r.api_key_id,
    r.requested_model_id,
    r.ingress_protocol,
    r.endpoint,
    r.response_model_id,
    r.response_protocol,
    r.response_id,
    r.stream,
    r.status,
    r.final_provider_id,
    r.final_channel_id,
    r.error_code,
    r.error_message,
    r.delivery_status,
    r.gateway_first_token_at,
    r.response_completed_at,
    r.started_at,
    r.completed_at,
    r.created_at,
    r.updated_at,
    r.requested_service_tier,
    r.actual_service_tier,
    r.settled_service_tier,
    r.service_tier_resolution,
    ak.name AS api_key_name,
    ak.key_prefix AS api_key_prefix,
    ak.key_plaintext AS api_key_plaintext,
    COALESCE(ur.uncached_input_tokens, 0)::bigint AS uncached_input_tokens,
    COALESCE(ur.cache_read_input_tokens, 0)::bigint AS cache_read_input_tokens,
    COALESCE(ur.cache_write_5m_input_tokens, 0)::bigint AS cache_write_5m_input_tokens,
    COALESCE(ur.cache_write_1h_input_tokens, 0)::bigint AS cache_write_1h_input_tokens,
    COALESCE(ur.cache_write_30m_input_tokens, 0)::bigint AS cache_write_30m_input_tokens,
    COALESCE(ur.output_tokens_total, 0)::bigint AS output_tokens_total,
    COALESCE(ur.reasoning_output_tokens, 0)::bigint AS reasoning_output_tokens,
    cs.total_cost_amount,
    cs.uncached_input_cost_amount,
    cs.cache_read_input_cost_amount,
    cs.cache_write_5m_input_cost_amount,
    cs.cache_write_1h_input_cost_amount,
    cs.cache_write_30m_input_cost_amount,
    cs.output_cost_amount,
    cs.reasoning_output_cost_amount,
    cs.uncached_input_cost,
    cs.cache_read_input_cost,
    cs.cache_write_5m_input_cost,
    cs.cache_write_1h_input_cost,
    cs.cache_write_30m_input_cost,
    cs.output_cost,
    cs.reasoning_output_cost,
    cs.cost_multiplier AS channel_cost_multiplier,
    cs.recharge_factor,
    ps.uncached_input_price,
    ps.cache_read_input_price,
    ps.cache_write_5m_input_price,
    ps.cache_write_1h_input_price,
    ps.cache_write_30m_input_price,
    ps.output_price,
    ps.reasoning_output_price,
    ps.service_tier AS price_service_tier,
    cs.service_tier AS cost_service_tier,
    ps.model_price_service_tier_id,
    cs.channel_price_service_tier_id,
    cs.tier_cost_source,
    (
        SELECT COALESCE(SUM(
            CASE
                WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
                WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
                ELSE 0
            END
        ), 0)
        FROM ledger_entries le
        WHERE le.request_record_id = r.id AND le.currency = 'USD'
    )::numeric AS user_charge_amount,
    r.reasoning_effort,
    r.reasoning_budget_tokens,
    r.client_ip,
    rt.name AS route_name,
    -- 倍率取结算当时的快照（price_snapshots.price_ratio），历史无快照行为 NULL，展示端回落「—」；
    -- 不再实时读 rt.price_ratio，避免管理员改倍率污染历史请求的倍率与倒推基准价展示。
    ps.price_ratio AS route_price_ratio,
    -- 售价侧长上下文是否已应用（费用列标识）；无 price 快照时回落成本侧标记。
    COALESCE(ps.long_context_applied, cs.long_context_applied, false) AS long_context_applied,
    rt.mode AS route_mode,
    m.display_name AS model_display_name,
    m.owned_by AS model_owned_by,
    fc.name AS final_channel_name,
    COALESCE((
        SELECT string_agg(ch.name, ' → ' ORDER BY a.attempt_index)
        FROM request_attempts a
        JOIN channels ch ON ch.id = a.channel_id
        WHERE a.request_record_id = r.id
    ), '')::text AS channel_chain,
    r.route_id,
    COALESCE(scoring_attempt.id, 0)::bigint AS scoring_attempt_id,
    COALESCE(scoring_attempt.dimensions, ARRAY[]::text[])::text[] AS scoring_dimensions,
    COALESCE(scoring_attempt.error_scoring_failure, false)::boolean AS scoring_error_failure,
    -- Sticky 摘要：无 routing_decision_traces 时整组为 NULL（列表显示「—」）。
    -- pinned / pinned_non_preferred 用 text 编码，避免 sqlc 对 JSON 布尔产出 interface{}。
    rdt.sticky_key_present,
    rdt.sticky_action,
    rdt.sticky_reason,
    rdt.sticky_before_channel_id,
    rdt.sticky_after_channel_id,
    CASE
        WHEN rdt.id IS NULL THEN ''
        WHEN (rdt.trace_payload->'sticky'->>'pinned') = 'true' THEN 'true'
        ELSE 'false'
    END AS sticky_pinned,
    CASE
        WHEN rdt.id IS NULL THEN ''
        WHEN (rdt.trace_payload->'sticky'->>'pinned_non_preferred') = 'true' THEN 'true'
        ELSE 'false'
    END AS sticky_pinned_non_preferred,
    sbc.name AS sticky_before_channel_name,
    sac.name AS sticky_after_channel_name
FROM filtered_page fp
JOIN request_records r ON r.id = fp.id
LEFT JOIN usage_records ur ON ur.request_record_id = r.id
LEFT JOIN cost_snapshots cs ON cs.request_record_id = r.id
LEFT JOIN price_snapshots ps ON ps.request_record_id = r.id
LEFT JOIN api_keys ak ON ak.id = r.api_key_id
LEFT JOIN routes rt ON rt.id = COALESCE(r.route_id, ak.route_id)
LEFT JOIN models m ON m.model_id = r.requested_model_id
LEFT JOIN channels fc ON fc.id = r.final_channel_id
LEFT JOIN routing_decision_traces rdt ON rdt.request_record_id = r.id
LEFT JOIN channels sbc ON sbc.id = rdt.sticky_before_channel_id
LEFT JOIN channels sac ON sac.id = rdt.sticky_after_channel_id
LEFT JOIN LATERAL (
    SELECT
        a.id,
        ARRAY_REMOVE(ARRAY[
            CASE WHEN a.ttft_scoring_sample THEN 'ttft'::text END,
            CASE WHEN a.error_scoring_sample THEN 'error'::text END
        ], NULL)::text[] AS dimensions,
        a.error_scoring_failure
    FROM request_attempts a
    WHERE a.request_record_id = r.id
      AND (sqlc.narg('channel_id')::bigint IS NULL OR a.channel_id = sqlc.narg('channel_id')::bigint)
      AND (sqlc.narg('attempt_id')::bigint IS NULL OR a.id = sqlc.narg('attempt_id')::bigint)
      AND (
          sqlc.narg('scoring_sample')::text IS NULL
          OR (sqlc.narg('scoring_sample')::text = 'ttft' AND a.ttft_scoring_sample)
          OR (sqlc.narg('scoring_sample')::text = 'error' AND a.error_scoring_sample)
          OR (sqlc.narg('scoring_sample')::text = 'any' AND (a.ttft_scoring_sample OR a.error_scoring_sample))
      )
      AND (
          sqlc.narg('channel_id')::bigint IS NOT NULL
          OR sqlc.narg('attempt_id')::bigint IS NOT NULL
          OR sqlc.narg('scoring_sample')::text IS NOT NULL
          OR a.ttft_scoring_sample
          OR a.error_scoring_sample
      )
    ORDER BY a.attempt_index, a.id
    LIMIT 1
) scoring_attempt ON true
ORDER BY
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'created_at') IN ('', 'created_at') AND COALESCE(sqlc.narg('sort_desc')::bool, true) THEN r.created_at END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'created_at') IN ('', 'created_at') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, true) THEN r.created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.status END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.status END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'user_id' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.user_id END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'user_id' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.user_id END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'model' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.requested_model_id END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'model' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.requested_model_id END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'stream' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.stream END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'stream' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.stream END ASC NULLS LAST,
  r.id DESC;

-- name: CountRequestRecords :one
-- CountRequestRecords 返回与 ListRequestRecordsPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM request_records
WHERE (sqlc.narg('user_id')::bigint IS NULL OR user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('api_key_id')::bigint IS NULL OR api_key_id = sqlc.narg('api_key_id')::bigint)
  AND (sqlc.narg('request_id')::text IS NULL OR request_id = sqlc.narg('request_id')::text)
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('model')::text IS NULL OR requested_model_id ILIKE '%' || sqlc.narg('model')::text || '%')
  AND (sqlc.narg('route_id')::bigint IS NULL OR route_id = sqlc.narg('route_id')::bigint)
  AND (
      (sqlc.narg('channel_id')::bigint IS NULL AND sqlc.narg('attempt_id')::bigint IS NULL AND sqlc.narg('scoring_sample')::text IS NULL)
      OR EXISTS (
          SELECT 1
          FROM request_attempts a
          WHERE a.request_record_id = request_records.id
            AND (sqlc.narg('channel_id')::bigint IS NULL OR a.channel_id = sqlc.narg('channel_id')::bigint)
            AND (sqlc.narg('attempt_id')::bigint IS NULL OR a.id = sqlc.narg('attempt_id')::bigint)
            AND (
                sqlc.narg('scoring_sample')::text IS NULL
                OR (sqlc.narg('scoring_sample')::text = 'ttft' AND a.ttft_scoring_sample)
                OR (sqlc.narg('scoring_sample')::text = 'error' AND a.error_scoring_sample)
                OR (sqlc.narg('scoring_sample')::text = 'any' AND (a.ttft_scoring_sample OR a.error_scoring_sample))
            )
      )
  )
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz);

-- name: GetRequestRecordByRequestID :one
-- GetRequestRecordByRequestID 按对外 request_id 读取单条请求记录完整事实（含 internal_error_detail）。
-- 不加锁，仅供 admin 只读详情上游源站使用；是否回显内部详情由 service/handler 控制。
SELECT
    id,
    request_id,
    user_id,
    api_key_id,
    requested_model_id,
    ingress_protocol,
    endpoint,
    response_model_id,
    response_protocol,
    response_id,
    stream,
    status,
    final_provider_id,
    final_channel_id,
    error_code,
    error_message,
    internal_error_detail,
    delivery_status,
    gateway_first_token_at,
    response_completed_at,
    started_at,
    completed_at,
    created_at,
    updated_at,
    route_id,
    reasoning_effort,
    reasoning_budget_tokens,
    client_ip,
    requested_service_tier,
    actual_service_tier,
    settled_service_tier,
    service_tier_resolution
FROM request_records
WHERE request_id = sqlc.arg(request_id);

-- name: ListAdminRequestAttemptsByRequest :many
-- ListAdminRequestAttemptsByRequest 为 Admin 请求详情关联当前渠道名称，并把本请求冻结的成本倍率
-- 只归到最终一次真实尝试。未形成成本快照的尝试保持倍率为空，禁止用当前配置冒充历史结算事实。
SELECT
    a.*,
    c.name AS channel_name,
    cs.cost_multiplier AS channel_cost_multiplier,
    cs.recharge_factor
FROM request_attempts a
JOIN channels c ON c.id = a.channel_id
LEFT JOIN cost_snapshots cs
  ON cs.request_record_id = a.request_record_id
 AND cs.channel_id = a.channel_id
 AND a.id = (
      SELECT final_attempt.id
      FROM request_attempts final_attempt
      WHERE final_attempt.request_record_id = a.request_record_id
      ORDER BY final_attempt.attempt_index DESC, final_attempt.id DESC
      LIMIT 1
 )
WHERE a.request_record_id = sqlc.arg(request_record_id)
ORDER BY a.attempt_index;
