-- name: CreateLedgerWriteOffException :one
-- CreateLedgerWriteOffException 记录实际费用超过授权金额时的平台核销事实。
INSERT INTO ledger_billing_exceptions (
    user_id, request_record_id, reservation_id, event_type,
    actual_amount, captured_amount, platform_amount,
    currency, reason_code, reason
)
VALUES (
   sqlc.arg(user_id),
   sqlc.arg(request_record_id),
   sqlc.arg(reservation_id),
   'write_off',
   sqlc.arg(actual_amount)::numeric,
   sqlc.arg(captured_amount)::numeric,
   sqlc.arg(actual_amount)::numeric - sqlc.arg(captured_amount)::numeric,
   sqlc.arg(currency),
   sqlc.arg(reason_code),
   sqlc.arg(reason)
       )
RETURNING *;

-- name: CreateLedgerRiskExposureException :one
-- CreateLedgerRiskExposureException 记录无可靠 usage 但可能产生上游成本的风险敞口事实。
INSERT INTO ledger_billing_exceptions (
    user_id, request_record_id, reservation_id, event_type,
    actual_amount, captured_amount, platform_amount,
    currency, reason_code, reason
)
VALUES (
   sqlc.arg(user_id),
   sqlc.arg(request_record_id),
   sqlc.arg(reservation_id),
   'risk_exposure',
   NULL,
   0,
   sqlc.arg(platform_amount)::numeric,
   sqlc.arg(currency),
   sqlc.arg(reason_code),
   sqlc.arg(reason)
       )
ON CONFLICT (reservation_id) DO UPDATE
    SET reason_code = ledger_billing_exceptions.reason_code
RETURNING *;

-- name: GetLedgerBillingExceptionByReservationID :one
-- GetLedgerBillingExceptionByReservationID 按 reservation ID 读取 billing exception。
SELECT *
FROM ledger_billing_exceptions
WHERE reservation_id = sqlc.arg(reservation_id);

-- name: GetLedgerBillingExceptionByRequest :one
-- GetLedgerBillingExceptionByRequest 按请求记录 ID 读取该请求的 billing exception（每请求至多一条）。
SELECT *
FROM ledger_billing_exceptions
WHERE request_record_id = sqlc.arg(request_record_id);

-- name: ListLedgerBillingExceptionsPage :many
-- ListLedgerBillingExceptionsPage 供 admin 只读查询台（M6）按用户/事件类型/时间过滤分页倒序列出核销/风险敞口事实。
-- 所有过滤项为 NULL 时不过滤。
SELECT
    id,
    user_id,
    request_record_id,
    reservation_id,
    event_type,
    actual_amount,
    captured_amount,
    platform_amount,
    currency,
    reason_code,
    reason,
    created_at
FROM ledger_billing_exceptions
WHERE (sqlc.narg('user_id')::bigint IS NULL OR user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('event_type')::text IS NULL OR event_type = sqlc.narg('event_type')::text)
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountLedgerBillingExceptions :one
-- CountLedgerBillingExceptions 返回与 ListLedgerBillingExceptionsPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM ledger_billing_exceptions
WHERE (sqlc.narg('user_id')::bigint IS NULL OR user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('event_type')::text IS NULL OR event_type = sqlc.narg('event_type')::text)
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz);
