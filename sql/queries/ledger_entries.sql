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

-- name: ListLedgerEntriesByUser :many
-- ListLedgerEntriesByUser 按用户和币种倒序列出账本流水。
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
    user_id = sqlc.arg (user_id)
  AND currency = sqlc.arg (currency)
ORDER BY
    created_at DESC,
    id DESC
LIMIT
    sqlc.arg (limit_rows)
    OFFSET
    sqlc.arg (offset_rows);

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

-- name: ListLedgerEntriesPage :many
-- ListLedgerEntriesPage 供 admin 只读查询台（M6）按用户/类型/币种/时间过滤分页倒序列出账本流水。
-- 所有过滤项为 NULL 时不过滤。
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
FROM ledger_entries
WHERE (sqlc.narg('user_id')::bigint IS NULL OR user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('entry_type')::text IS NULL OR entry_type = sqlc.narg('entry_type')::text)
  AND (sqlc.narg('currency')::text IS NULL OR currency = sqlc.narg('currency')::text)
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountLedgerEntries :one
-- CountLedgerEntries 返回与 ListLedgerEntriesPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM ledger_entries
WHERE (sqlc.narg('user_id')::bigint IS NULL OR user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('entry_type')::text IS NULL OR entry_type = sqlc.narg('entry_type')::text)
  AND (sqlc.narg('currency')::text IS NULL OR currency = sqlc.narg('currency')::text)
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz);
