-- name: GetCapabilityCalibrationWatermark :one
-- GetCapabilityCalibrationWatermark 读取增量游标（上次处理到的 request_attempts.id 上界）。
SELECT last_processed_attempt_id
FROM capability_calibration_state
WHERE id = 1;

-- name: SetCapabilityCalibrationWatermark :exec
-- SetCapabilityCalibrationWatermark 推进增量游标到新的 request_attempts.id 上界。
UPDATE capability_calibration_state
SET last_processed_attempt_id = sqlc.arg(last_processed_attempt_id),
    updated_at = now()
WHERE id = 1;

-- name: AcquireCapabilityCalibrationLease :one
-- AcquireCapabilityCalibrationLease 抢占单例校正租约：仅当空闲或已过期时才能抢到（多实例互斥）。
-- 抢不到返回 0 行（pgx.ErrNoRows），调用方据此判定「另一实例正在跑」。
UPDATE capability_calibration_state
SET locked_by = sqlc.arg(locked_by),
    locked_until = sqlc.arg(locked_until),
    updated_at = now()
WHERE id = 1
    AND (locked_until IS NULL OR locked_until < sqlc.arg(now_at))
RETURNING locked_by, locked_until;

-- name: RenewCapabilityCalibrationLease :one
-- RenewCapabilityCalibrationLease 续租：仅当本实例仍持有且未过期时才延长（防止抢占他人租约）。
-- 续不到返回 0 行，说明租约已丢失（被抢占或过期），调用方应中止本轮运行。
UPDATE capability_calibration_state
SET locked_until = sqlc.arg(locked_until),
    updated_at = now()
WHERE id = 1
    AND locked_by = sqlc.arg(locked_by)
    AND locked_until IS NOT NULL
    AND locked_until >= sqlc.arg(now_at)
RETURNING locked_by;

-- name: ReleaseCapabilityCalibrationLease :exec
-- ReleaseCapabilityCalibrationLease 释放租约：仅清除本实例持有的锁（幂等，非持有者不动）。
UPDATE capability_calibration_state
SET locked_by = NULL,
    locked_until = NULL,
    updated_at = now()
WHERE id = 1
    AND locked_by = sqlc.arg(locked_by);
