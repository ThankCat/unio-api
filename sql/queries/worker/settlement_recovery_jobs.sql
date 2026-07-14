-- name: GetDeadSettlementRecoveryJobWithRunningRequest :one
-- GetDeadSettlementRecoveryJobWithRunningRequest 取一条已 dead、但其请求仍停留在 running 的补偿任务，
-- 供 worker 收口：释放冻结余额（记风险敞口）并把请求原子推进到 failed，避免请求永远停在「进行中」。
-- 以「请求仍为 running」为闸门，幂等且可重放（已收口的请求不会再被选中）。
SELECT j.*
FROM settlement_recovery_jobs j
JOIN request_records r ON r.id = j.request_record_id
WHERE j.status = 'dead'
  AND r.status = 'running'
ORDER BY j.id ASC
LIMIT 1;

-- name: ClaimNextSettlementRecoveryJob :one
-- ClaimNextSettlementRecoveryJob claim 一条到期 pending 或锁过期 running 的 recovery job。
WITH candidate AS (
    SELECT id
    FROM settlement_recovery_jobs
    WHERE settlement_recovery_jobs.attempt_count < settlement_recovery_jobs.max_attempts
      AND (
          (
              settlement_recovery_jobs.status = 'pending'
                  AND settlement_recovery_jobs.next_run_at <= sqlc.arg(now_at)
              )
              OR
          (
              settlement_recovery_jobs.status = 'running'
                  AND settlement_recovery_jobs.locked_until <= sqlc.arg(now_at)
              )
      )
    ORDER BY settlement_recovery_jobs.next_run_at ASC, settlement_recovery_jobs.id ASC
        FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE settlement_recovery_jobs
SET
    status = 'running',
    attempt_count = attempt_count + 1,
    locked_by = sqlc.arg(locked_by),
    locked_until = sqlc.arg(locked_until),
    last_attempted_at = sqlc.arg(now_at),
    updated_at = sqlc.arg(now_at)
WHERE settlement_recovery_jobs.id = (SELECT candidate.id FROM candidate)
RETURNING *;

-- name: MarkSettlementRecoveryJobRetry :one
-- MarkSettlementRecoveryJobRetry 将 running recovery job 退回 pending 并设置下次重试时间。
UPDATE settlement_recovery_jobs
SET
    status = 'pending',
    locked_by = NULL,
    locked_until = NULL,
    next_run_at = sqlc.arg(next_run_at),
    last_error_code = sqlc.arg(last_error_code),
    last_error_message = sqlc.arg(last_error_message),
    last_internal_error_detail = sqlc.arg(last_internal_error_detail),
    updated_at = sqlc.arg(updated_at)
WHERE settlement_recovery_jobs.id = sqlc.arg(id)
  AND settlement_recovery_jobs.status = 'running'
  AND settlement_recovery_jobs.locked_by = sqlc.arg(locked_by)
  AND settlement_recovery_jobs.locked_until = sqlc.arg(locked_until)
  AND settlement_recovery_jobs.attempt_count = sqlc.arg(attempt_count)
  AND settlement_recovery_jobs.attempt_count < settlement_recovery_jobs.max_attempts
RETURNING *;

-- name: MarkSettlementRecoveryJobDead :one
-- MarkSettlementRecoveryJobDead 将 running recovery job 标记为 dead，等待后台人工处理。
UPDATE settlement_recovery_jobs
SET
    status = 'dead',
    locked_by = NULL,
    locked_until = NULL,
    last_error_code = sqlc.arg(last_error_code),
    last_error_message = sqlc.arg(last_error_message),
    last_internal_error_detail = sqlc.arg(last_internal_error_detail),
    completed_at = sqlc.arg(completed_at),
    updated_at = sqlc.arg(completed_at)
WHERE settlement_recovery_jobs.id = sqlc.arg(id)
  AND settlement_recovery_jobs.status = 'running'
  AND settlement_recovery_jobs.locked_by = sqlc.arg(locked_by)
  AND settlement_recovery_jobs.locked_until = sqlc.arg(locked_until)
  AND settlement_recovery_jobs.attempt_count = sqlc.arg(attempt_count)
RETURNING *;

-- name: MarkExhaustedSettlementRecoveryJobDead :one
-- MarkExhaustedSettlementRecoveryJobDead 将到期且已耗尽自动尝试次数的 recovery job 标记为 dead。
WITH candidate AS (
    SELECT id
    FROM settlement_recovery_jobs
    WHERE settlement_recovery_jobs.attempt_count >= settlement_recovery_jobs.max_attempts
      AND (
          (
              settlement_recovery_jobs.status = 'pending'
                  AND settlement_recovery_jobs.next_run_at <= sqlc.arg(now_at)
              )
              OR
          (
              settlement_recovery_jobs.status = 'running'
                  AND settlement_recovery_jobs.locked_until <= sqlc.arg(now_at)
              )
      )
    ORDER BY settlement_recovery_jobs.next_run_at ASC, settlement_recovery_jobs.id ASC
        FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE settlement_recovery_jobs
SET
    status = 'dead',
    locked_by = NULL,
    locked_until = NULL,
    last_error_code = sqlc.arg(last_error_code),
    last_error_message = sqlc.arg(last_error_message),
    last_internal_error_detail = sqlc.arg(last_internal_error_detail),
    completed_at = sqlc.arg(completed_at),
    updated_at = sqlc.arg(completed_at)
WHERE settlement_recovery_jobs.id = (SELECT candidate.id FROM candidate)
RETURNING *;
