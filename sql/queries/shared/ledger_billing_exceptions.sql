-- name: CreateLedgerWriteOffException :one
-- CreateLedgerWriteOffException 记录实际费用超过「授权金额 + 二次补扣金额」后仍由平台核销的残差事实。
-- captured_amount 存「冻结内确认扣费 + 超额二次补扣」的合计（用户真实承担总额）；
-- platform_amount = 真实费用 - 合计实扣，即平台最终核销的不可回收残差。
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
   sqlc.arg(captured_amount)::numeric + sqlc.arg(overage_amount)::numeric,
   sqlc.arg(actual_amount)::numeric - sqlc.arg(captured_amount)::numeric - sqlc.arg(overage_amount)::numeric,
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
