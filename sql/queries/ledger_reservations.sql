-- name: CreateLedgerReservation :one
-- CreateLedgerReservation 创建一次请求的余额预授权记录。
INSERT INTO ledger_reservations (
    user_id,
    request_record_id,
    currency,
    status,
    estimated_amount,
    authorized_amount,
    idempotency_key,
    reason
)
VALUES (
           sqlc.arg(user_id),
           sqlc.arg(request_record_id),
           sqlc.arg(currency),
           'authorized',
           sqlc.arg(estimated_amount),
           sqlc.arg(authorized_amount),
           sqlc.arg(idempotency_key),
           sqlc.arg(reason)
       )
RETURNING
    id,
    user_id,
    request_record_id,
    currency,
    status,
    authorized_amount,
    captured_amount,
    released_amount,
    estimated_amount,
    capture_ledger_entry_id,
    idempotency_key,
    reason,
    created_at,
    updated_at,
    captured_at,
    released_at;

-- name: GetLedgerReservationByIdempotencyKey :one
-- GetLedgerReservationByIdempotencyKey 按幂等键读取余额预授权记录。
SELECT
    id,
    user_id,
    request_record_id,
    currency,
    status,
    authorized_amount,
    captured_amount,
    released_amount,
    estimated_amount,
    capture_ledger_entry_id,
    idempotency_key,
    reason,
    created_at,
    updated_at,
    captured_at,
    released_at
FROM ledger_reservations
WHERE idempotency_key = sqlc.arg(idempotency_key);

-- name: GetLedgerReservationByRequestRecordID :one
-- GetLedgerReservationByRequestRecordID 按请求 ID 读取余额预授权记录。
SELECT
    id,
    user_id,
    request_record_id,
    currency,
    status,
    authorized_amount,
    captured_amount,
    released_amount,
    estimated_amount,
    capture_ledger_entry_id,
    idempotency_key,
    reason,
    created_at,
    updated_at,
    captured_at,
    released_at
FROM ledger_reservations
WHERE request_record_id = sqlc.arg(request_record_id);

-- name: GetLedgerReservationByRequestRecordIDForUpdate :one
-- GetLedgerReservationByRequestRecordIDForUpdate 按请求 ID 锁定余额预授权记录。
SELECT
    id,
    user_id,
    request_record_id,
    currency,
    status,
    authorized_amount,
    captured_amount,
    released_amount,
    estimated_amount,
    capture_ledger_entry_id,
    idempotency_key,
    reason,
    created_at,
    updated_at,
    captured_at,
    released_at
FROM ledger_reservations
WHERE request_record_id = sqlc.arg(request_record_id)
    FOR UPDATE;

-- name: CaptureLedgerReservation :one
-- CaptureLedgerReservation 将 authorized 预授权确认扣费，并记录 capture 流水。
UPDATE ledger_reservations
SET
    status = 'captured',
    captured_amount = sqlc.arg(captured_amount),
    released_amount = authorized_amount - sqlc.arg(captured_amount),
    capture_ledger_entry_id = sqlc.arg(capture_ledger_entry_id),
    captured_at = now(),
    released_at = CASE
                      WHEN authorized_amount > sqlc.arg(captured_amount) THEN now()
                      ELSE NULL
        END,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'authorized'
  AND authorized_amount >= sqlc.arg(captured_amount)
RETURNING
    id,
    user_id,
    request_record_id,
    currency,
    status,
    authorized_amount,
    captured_amount,
    released_amount,
    estimated_amount,
    capture_ledger_entry_id,
    idempotency_key,
    reason,
    created_at,
    updated_at,
    captured_at,
    released_at;

-- name: ReleaseLedgerReservation :one
-- ReleaseLedgerReservation 将 authorized 预授权全部释放。
UPDATE ledger_reservations
SET
    status = 'released',
    released_amount = authorized_amount,
    released_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'authorized'
RETURNING
    id,
    user_id,
    request_record_id,
    currency,
    status,
    authorized_amount,
    captured_amount,
    released_amount,
    estimated_amount,
    capture_ledger_entry_id,
    idempotency_key,
    reason,
    created_at,
    updated_at,
    captured_at,
    released_at;

-- name: ListOrphanAuthorizedReservations :many
-- ListOrphanAuthorizedReservations 扫描「孤儿」预授权：进程崩溃后请求永久停留 running、冻结余额永不释放。
-- 仅命中 status='authorized' 且超过阈值、且其请求仍 running、且没有任何 settlement 补偿任务的预授权，
-- 与 settlement_recovery worker 严格互补（有补偿任务的预授权由该 worker 负责 capture/finalize，绝不在此释放，
-- 避免上游已成功却被误释放导致白嫖）。走部分索引 idx_ledger_reservations_authorized_created_at。
SELECT
    lr.id,
    lr.user_id,
    lr.request_record_id,
    lr.currency,
    lr.status,
    lr.authorized_amount,
    lr.captured_amount,
    lr.released_amount,
    lr.estimated_amount,
    lr.capture_ledger_entry_id,
    lr.idempotency_key,
    lr.reason,
    lr.created_at,
    lr.updated_at,
    lr.captured_at,
    lr.released_at
FROM ledger_reservations lr
JOIN request_records r ON r.id = lr.request_record_id
WHERE lr.status = 'authorized'
  AND lr.created_at < sqlc.arg(created_before)
  AND r.status = 'running'
  AND NOT EXISTS (
        SELECT 1 FROM settlement_recovery_jobs j
        WHERE j.request_record_id = lr.request_record_id
    )
ORDER BY lr.created_at, lr.id
LIMIT sqlc.arg(batch_limit);
