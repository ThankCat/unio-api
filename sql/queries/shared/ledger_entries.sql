-- name: CreateLedgerEntry :one
-- CreateLedgerEntry 创建一条用户余额变化账本流水。
INSERT INTO
    ledger_entries (
    user_id,
    request_record_id,
    entry_type,
    amount,
    currency,
    balance_before,
    balance_after,
    idempotency_key,
    reason
)
VALUES
    (
        sqlc.arg (user_id),
        sqlc.arg (request_record_id),
        sqlc.arg (entry_type),
        sqlc.arg (amount),
        sqlc.arg (currency),
        sqlc.arg (balance_before),
        sqlc.arg (balance_after),
        sqlc.arg (idempotency_key),
        sqlc.arg (reason)
    )
RETURNING
    id,
    user_id,
    request_record_id,
    entry_type,
    amount,
    currency,
    balance_before,
    balance_after,
    idempotency_key,
    reason,
    created_at;

-- name: GetLedgerEntryByIdempotencyKey :one
-- GetLedgerEntryByIdempotencyKey 按幂等键读取账本流水。
SELECT
    id,
    user_id,
    request_record_id,
    entry_type,
    amount,
    currency,
    balance_before,
    balance_after,
    idempotency_key,
    reason,
    created_at
FROM
    ledger_entries
WHERE
    idempotency_key = sqlc.arg (idempotency_key);

-- name: LockLedgerEntryIdempotencyKey :exec
-- LockLedgerEntryIdempotencyKey 在事务内按 ledger entry 幂等键加 advisory lock。
-- 该锁避免外部事务并发重复扣款时先改余额、后撞 ledger_entries 唯一约束。
SELECT pg_advisory_xact_lock(
               hashtext('ledger_entries'),
               hashtext(sqlc.arg(idempotency_key)::text)
       );

-- name: ListLedgerEntriesByRequest :many
-- ListLedgerEntriesByRequest 按请求 ID 列出相关账本流水。
SELECT
    id,
    user_id,
    request_record_id,
    entry_type,
    amount,
    currency,
    balance_before,
    balance_after,
    idempotency_key,
    reason,
    created_at
FROM
    ledger_entries
WHERE
    request_record_id = sqlc.arg (request_record_id)
ORDER BY
    id;
