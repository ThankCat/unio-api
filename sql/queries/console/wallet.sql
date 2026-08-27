-- name: ListConsoleLedgerEntries :many
-- ListConsoleLedgerEntries 按当前用户分页倒序列出钱包流水。
-- user_id 必填（调用方只能传会话主体），entry_type / 时间为可选过滤；
-- total_count 用窗口函数随行带出，省一次独立 COUNT 往返。
SELECT
    id,
    entry_type,
    amount,
    currency,
    balance_after,
    request_record_id,
    reason,
    created_at,
    COUNT(*) OVER () AS total_count
FROM ledger_entries
WHERE user_id = sqlc.arg(user_id)
  AND currency = sqlc.arg(currency)
  AND (cardinality(sqlc.arg(entry_types)::text[]) = 0 OR entry_type = ANY (sqlc.arg(entry_types)::text[]))
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountConsoleLedgerEntries :one
-- CountConsoleLedgerEntries 返回与 ListConsoleLedgerEntries 相同过滤条件下的总条数。
-- 供 offset 超出末页导致列表为空、拿不到窗口计数时兜底。
SELECT COUNT(*) AS total
FROM ledger_entries
WHERE user_id = sqlc.arg(user_id)
  AND currency = sqlc.arg(currency)
  AND (cardinality(sqlc.arg(entry_types)::text[]) = 0 OR entry_type = ANY (sqlc.arg(entry_types)::text[]))
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz);
