-- name: GetSettlementRecoveryJobByRequest :one
-- GetSettlementRecoveryJobByRequest 按请求 ID 读取 settlement recovery job。
SELECT *
FROM settlement_recovery_jobs
WHERE request_record_id = sqlc.arg(request_record_id);

-- name: MarkSettlementRecoveryJobSucceeded :one
-- MarkSettlementRecoveryJobSucceeded 将 pending/running recovery job 标记为 succeeded。
WITH updated AS (
    UPDATE settlement_recovery_jobs
    SET
        status = 'succeeded',
        locked_by = NULL,
        locked_until = NULL,
        completed_at = sqlc.arg(completed_at),
        updated_at = sqlc.arg(completed_at)
    WHERE settlement_recovery_jobs.id = sqlc.arg(id)
      AND settlement_recovery_jobs.status IN ('pending', 'running')
    RETURNING *
)
SELECT *
FROM updated

UNION ALL

SELECT settlement_recovery_jobs.*
FROM settlement_recovery_jobs
WHERE settlement_recovery_jobs.id = sqlc.arg(id)
  AND settlement_recovery_jobs.status = 'succeeded'
  AND NOT EXISTS (SELECT 1 FROM updated);
