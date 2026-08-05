-- name: ListOrphanAuthorizedReservations :many
-- ListOrphanAuthorizedReservations 扫描「孤儿」预授权候选：进程崩溃后请求永久停留 running、冻结余额永不释放。
-- 仅命中尚未向客户交付内容、超过阈值、没有 settlement 补偿任务的 authorized 请求。
-- running attempt 由 worker 结合 Redis permit 存活状态判断，不能仅按 reservation 年龄判为孤儿。
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
  AND r.delivery_status = 'not_started'
  AND NOT EXISTS (
        SELECT 1 FROM settlement_recovery_jobs j
        WHERE j.request_record_id = lr.request_record_id
    )
ORDER BY lr.created_at, lr.id
LIMIT sqlc.arg(batch_limit);

-- name: ListRunningRequestAttemptPermits :many
-- ListRunningRequestAttemptPermits 读取孤儿候选当前全部 running attempt 的 permit 与交付边界。
SELECT id, permit_id, upstream_started_at, gateway_first_token_at
FROM request_attempts
WHERE request_record_id = sqlc.arg(request_record_id)
  AND status = 'running'
ORDER BY id;

-- name: ListRunningRequestAttemptPermitsForUpdate :many
-- ListRunningRequestAttemptPermitsForUpdate 在收口事务中锁定并重查 running attempt 集合。
SELECT id, permit_id, upstream_started_at, gateway_first_token_at
FROM request_attempts
WHERE request_record_id = sqlc.arg(request_record_id)
  AND status = 'running'
FOR UPDATE;

-- name: MarkRunningRequestAttemptsOrphaned :execrows
-- MarkRunningRequestAttemptsOrphaned 将进程退出遗留的 running attempt 收口为平台失败，不进入渠道错误样本。
UPDATE request_attempts
SET status = 'failed',
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    internal_error_detail = sqlc.arg(internal_error_detail),
    completed_at = sqlc.arg(completed_at)
WHERE request_record_id = sqlc.arg(request_record_id)
  AND status = 'running';

-- name: ListStrandedAuthorizedReservations :many
-- ListStrandedAuthorizedReservations 扫描「搁浅」预授权：请求已进入终态但冻结余额仍停留在 authorized。
-- 成因是网关失败路径「先 release 再写终态」两步非原子——release 自身失败（5s 超时 / reservation 行锁竞争 /
-- 瞬时抖动）而随后的审计写入成功。这类行落在孤儿清扫（只捞 r.status='running'）与 settlement recovery
-- 之间，既无自动回收路径也无 TTL，reserved_balance 被永久占用。
--
-- 自动释放的安全性依据：全部释放路径都是「release 在前、终态写在后」或与终态写同事务，因此 authorized
-- 配终态请求不存在合法瞬时态，命中即为已确定失败的 release。仅取 failed/canceled——authorized 配
-- succeeded 属另一类更严重的异常（capture 未发生却已告知客户成功），自动释放会抹掉现场，留给不变量巡检。
-- NOT EXISTS 与 settlement recovery worker 严格互补，绝不释放「上游可能已成功、等待 capture」的冻结。
-- 走部分索引 idx_ledger_reservations_authorized_created_at。
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
  AND r.status IN ('failed', 'canceled')
  AND NOT EXISTS (
        SELECT 1 FROM settlement_recovery_jobs j
        WHERE j.request_record_id = lr.request_record_id
    )
ORDER BY lr.created_at, lr.id
LIMIT sqlc.arg(batch_limit);
