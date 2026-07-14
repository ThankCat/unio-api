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
