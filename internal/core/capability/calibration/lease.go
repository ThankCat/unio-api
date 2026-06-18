package calibration

import (
	"context"
	"errors"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Lease 是能力自动校正的单例分布式租约（DB 行锁，复用单行表 capability_calibration_state）。
//
// 用途：多实例部署 worker-server 时互斥执行校正——校正本身靠 watermark 幂等，但 rollup 计数是累加的，
// 并发跑同一段成功流量会重复计数、虚高证据比例进而误自动补（DESIGN 风险 A）。Acquire 仅在租约空闲或
// 已过期时成功；运行中由 worker 周期 Renew 续租；崩溃后租约自动过期可被其他实例接管。
type Lease struct {
	queries  *sqlc.Queries
	workerID string
}

// NewLease 创建能力自动校正分布式租约。workerID 标识当前实例（用于持有判定与可观测）。
func NewLease(queries *sqlc.Queries, workerID string) *Lease {
	if queries == nil {
		panic("calibration: lease queries is required")
	}
	if workerID == "" {
		workerID = "capability-calibration-worker"
	}
	return &Lease{queries: queries, workerID: workerID}
}

// Acquire 抢占租约：成功返回 true；被他人持有（未过期）返回 false；DB 故障返回 error。
func (l *Lease) Acquire(ctx context.Context, ttl time.Duration) (bool, error) {
	now := time.Now()
	_, err := l.queries.AcquireCapabilityCalibrationLease(ctx, sqlc.AcquireCapabilityCalibrationLeaseParams{
		LockedBy:    pgtype.Text{String: l.workerID, Valid: true},
		LockedUntil: pgtype.Timestamptz{Time: now.Add(ttl), Valid: true},
		NowAt:       pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, storeFailure(err, "acquire calibration lease")
	}
	return true, nil
}

// Renew 续租：本实例仍持有且未过期时返回 true；租约已丢失（被抢占/过期）返回 false；DB 故障返回 error。
func (l *Lease) Renew(ctx context.Context, ttl time.Duration) (bool, error) {
	now := time.Now()
	_, err := l.queries.RenewCapabilityCalibrationLease(ctx, sqlc.RenewCapabilityCalibrationLeaseParams{
		LockedUntil: pgtype.Timestamptz{Time: now.Add(ttl), Valid: true},
		LockedBy:    pgtype.Text{String: l.workerID, Valid: true},
		NowAt:       pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, storeFailure(err, "renew calibration lease")
	}
	return true, nil
}

// Release 释放租约（仅清除本实例持有的锁，幂等）。
func (l *Lease) Release(ctx context.Context) error {
	if err := l.queries.ReleaseCapabilityCalibrationLease(ctx, pgtype.Text{String: l.workerID, Valid: true}); err != nil {
		return storeFailure(err, "release calibration lease")
	}
	return nil
}
