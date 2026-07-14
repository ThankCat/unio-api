-- name: GetSettlementRecoveryJobByID :one
-- GetSettlementRecoveryJobByID 按主键读取单条 recovery job 完整事实（含 last_internal_error_detail）。
-- 不加锁，仅供 admin 只读详情端点使用；是否回显内部详情由 service/handler 控制（M8）。
-- 关联预授权行与超额补扣流水,还原资金闭环:冻结(authorized)→ 实扣(captured+overage)→ 释放(released)。
-- overage 是独立 debit 流水(幂等键 = capture 键 + ':overage',见 ledger/reservation.go),每请求至多一条。
SELECT
    j.*,
    rr.request_id AS request_public_id,
    res.status AS reservation_status,
    res.captured_amount AS reservation_captured_amount,
    res.released_amount AS reservation_released_amount,
    COALESCE(oe.amount, 0)::numeric(20, 10) AS overage_amount
FROM settlement_recovery_jobs j
LEFT JOIN request_records rr ON rr.id = j.request_record_id
LEFT JOIN ledger_reservations res ON res.id = j.reservation_id
LEFT JOIN ledger_entries oe
    ON oe.request_record_id = j.request_record_id
   AND oe.entry_type = 'debit'
   AND oe.idempotency_key LIKE '%:overage'
WHERE j.id = sqlc.arg(id);

-- name: ListSettlementRecoveryJobsPage :many
-- ListSettlementRecoveryJobsPage 按可选过滤分页倒序列出 recovery job（M8 运营任务台，只读）。
-- 安全红线：列表绝不 SELECT last_internal_error_detail（从存储层就脱敏）；金额走十进制字符串。
-- 关联预授权行与超额补扣流水,还原资金闭环:冻结(authorized)→ 实扣(captured+overage)→ 释放(released)。
-- reservation_status: authorized=未结算 / captured=已实扣 / released=已全额释放(dead 收口)。
SELECT
    j.id,
    j.user_id,
    j.request_record_id,
    j.attempt_id,
    j.reservation_id,
    j.response_protocol,
    j.response_id,
    j.response_model_id,
    j.model_id,
    j.provider_id,
    j.channel_id,
    j.upstream_protocol,
    j.upstream_model,
    j.finish_class,
    j.upstream_status_code,
    j.currency,
    j.estimated_amount,
    j.authorized_amount,
    j.status,
    j.attempt_count,
    j.max_attempts,
    j.next_run_at,
    j.locked_by,
    j.locked_until,
    j.last_error_code,
    j.last_error_message,
    j.last_attempted_at,
    j.completed_at,
    j.created_at,
    j.updated_at,
    rr.request_id AS request_public_id,
    res.status AS reservation_status,
    res.captured_amount AS reservation_captured_amount,
    res.released_amount AS reservation_released_amount,
    COALESCE(oe.amount, 0)::numeric(20, 10) AS overage_amount
FROM settlement_recovery_jobs j
LEFT JOIN request_records rr ON rr.id = j.request_record_id
LEFT JOIN ledger_reservations res ON res.id = j.reservation_id
LEFT JOIN ledger_entries oe
    ON oe.request_record_id = j.request_record_id
   AND oe.entry_type = 'debit'
   AND oe.idempotency_key LIKE '%:overage'
WHERE (sqlc.narg('status')::text IS NULL OR j.status = sqlc.narg('status')::text)
  AND (sqlc.narg('user_id')::bigint IS NULL OR j.user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR j.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR j.created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'created_at') IN ('', 'created_at') AND COALESCE(sqlc.narg('sort_desc')::bool, true) THEN j.created_at END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'created_at') IN ('', 'created_at') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, true) THEN j.created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN j.status END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN j.status END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'user_id' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN j.user_id END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'user_id' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN j.user_id END ASC NULLS LAST,
  j.id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountSettlementRecoveryJobs :one
-- CountSettlementRecoveryJobs 返回与 ListSettlementRecoveryJobsPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM settlement_recovery_jobs
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('user_id')::bigint IS NULL OR user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz);
