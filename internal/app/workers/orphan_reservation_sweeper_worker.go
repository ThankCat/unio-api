package workers

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

const (
	defaultOrphanReservationAgeThreshold = 15 * time.Minute
	defaultOrphanReservationBatchSize    = 100
)

// OrphanReservationStore 定义清扫 worker 扫描孤儿预授权所需的存储能力。
type OrphanReservationStore interface {
	ListOrphanAuthorizedReservations(ctx context.Context, arg sqlc.ListOrphanAuthorizedReservationsParams) ([]sqlc.LedgerReservation, error)
	ListRunningRequestAttemptPermits(ctx context.Context, requestRecordID int64) ([]sqlc.ListRunningRequestAttemptPermitsRow, error)
}

// OrphanReservationFinalizer 定义在单事务内释放孤儿冻结并把请求收口为 failed 的业务能力。
type OrphanReservationFinalizer interface {
	FinalizeOrphanReservation(ctx context.Context, reservation sqlc.LedgerReservation, attemptIDs []int64, permitIDs []string) (bool, error)
}

// OrphanAttemptPermitReader 提供 running attempt 的跨进程存活证明。
type OrphanAttemptPermitReader interface {
	IsAttemptPermitActive(ctx context.Context, permitID string) (bool, error)
}

// OrphanReservationSweeperWorker 周期扫描并收口进程崩溃遗留的「孤儿」预授权（status=authorized、请求永久 running、
// 未向客户交付内容、无 settlement 补偿任务）：无 running attempt 时直接收口；有 running attempt 时必须先确认
// permit 已失效。收口会释放冻结、记 risk_exposure 上界敞口并把 request/attempt 推进到 failed。
//
// 与 settlement_recovery worker 严格互补：扫描查询排除有补偿任务的预授权，单条收口在 request 行锁内再次
// 确认 delivery/recovery/attempt 集合，绝不释放仍在正常长流或「上游可能已成功、等待 capture」的冻结。
type OrphanReservationSweeperWorker struct {
	store        OrphanReservationStore
	finalizer    OrphanReservationFinalizer
	permitReader OrphanAttemptPermitReader
	logger       *zap.Logger
	ageThreshold time.Duration
	batchSize    int32
}

// NewOrphanReservationSweeperWorker 创建孤儿预授权清扫 worker。
func NewOrphanReservationSweeperWorker(store OrphanReservationStore, finalizer OrphanReservationFinalizer, permitReader OrphanAttemptPermitReader, logger *zap.Logger, ageThreshold time.Duration, batchSize int32) *OrphanReservationSweeperWorker {
	if store == nil {
		panic("workers: orphan reservation store is required")
	}
	if finalizer == nil {
		panic("workers: orphan reservation finalizer is required")
	}
	if permitReader == nil {
		panic("workers: orphan attempt permit reader is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	if ageThreshold <= 0 {
		ageThreshold = defaultOrphanReservationAgeThreshold
	}
	if batchSize <= 0 {
		batchSize = defaultOrphanReservationBatchSize
	}

	return &OrphanReservationSweeperWorker{
		store:        store,
		finalizer:    finalizer,
		permitReader: permitReader,
		logger:       logger,
		ageThreshold: ageThreshold,
		batchSize:    batchSize,
	}
}

// Name 返回 worker 名称。
func (w *OrphanReservationSweeperWorker) Name() string {
	return "orphan_reservation_sweeper"
}

// RunOnce 扫描并收口一批到期的孤儿预授权。
//
// 单条收口失败不阻断整批：记日志继续，下一 tick 安全重放（FinalizeOrphanReservation 以「请求仍 running」为幂等闸门）。
// 只有本轮真实收口至少一条时返回 true，让 runner 继续排空；仅遇到活跃长流时按正常间隔再检查。
func (w *OrphanReservationSweeperWorker) RunOnce(ctx context.Context) (bool, error) {
	cutoff := time.Now().Add(-w.ageThreshold)

	rows, err := w.store.ListOrphanAuthorizedReservations(ctx, sqlc.ListOrphanAuthorizedReservationsParams{
		CreatedBefore: pgtype.Timestamptz{Time: cutoff, Valid: true},
		BatchLimit:    w.batchSize,
	})
	if err != nil {
		return false, failure.Wrap(
			failure.CodeGatewayRequestOrphanReclaimed,
			err,
			failure.WithMessage("list orphan authorized reservations"),
		)
	}
	if len(rows) == 0 {
		return false, nil
	}

	swept := 0
	for _, reservation := range rows {
		if ctx.Err() != nil {
			break
		}
		attempts, err := w.store.ListRunningRequestAttemptPermits(ctx, reservation.RequestRecordID)
		if err != nil {
			w.logItemFailure("list orphan running attempts", reservation, err)
			continue
		}

		attemptIDs := make([]int64, 0, len(attempts))
		permitIDs := make([]string, 0, len(attempts))
		orphaned := true
		for _, attempt := range attempts {
			attemptIDs = append(attemptIDs, attempt.ID)
			permitID := ""
			if attempt.PermitID.Valid {
				permitID = attempt.PermitID.String
				active, permitErr := w.permitReader.IsAttemptPermitActive(ctx, permitID)
				if permitErr != nil {
					w.logItemFailure("read orphan attempt permit", reservation, permitErr)
					orphaned = false
					break
				}
				if active {
					orphaned = false
					break
				}
			} else if attempt.UpstreamStartedAt.Valid || attempt.GatewayFirstTokenAt.Valid {
				// 旧版本 attempt 没有 permit ID；只有从未开始 transport、也未交付首 Token 才能安全回收。
				orphaned = false
				break
			}
			permitIDs = append(permitIDs, permitID)
		}
		if !orphaned {
			continue
		}

		finalized, err := w.finalizer.FinalizeOrphanReservation(ctx, reservation, attemptIDs, permitIDs)
		if err != nil {
			fields := append([]zap.Field{
				zap.String("worker", w.Name()),
				zap.Int64("reservation_id", reservation.ID),
				zap.Int64("request_record_id", reservation.RequestRecordID),
			}, failure.LogFields(err)...)
			w.logger.Error("orphan reservation sweep failed", fields...)
			continue
		}
		if finalized {
			swept++
		}
	}

	if swept > 0 {
		// 孤儿预授权应当极其罕见；批量出现意味着曾发生进程崩溃，附 alert 键便于告警路由。
		w.logger.Warn("orphan reservations reclaimed",
			zap.String("worker", w.Name()),
			zap.Int("swept", swept),
			zap.Int("batch", len(rows)),
			zap.String("age_threshold", w.ageThreshold.String()),
			zap.String("alert", "orphan_reservation_reclaimed"),
		)
	}

	return swept > 0, nil
}

func (w *OrphanReservationSweeperWorker) logItemFailure(message string, reservation sqlc.LedgerReservation, err error) {
	fields := append([]zap.Field{
		zap.String("worker", w.Name()),
		zap.Int64("reservation_id", reservation.ID),
		zap.Int64("request_record_id", reservation.RequestRecordID),
	}, failure.LogFields(err)...)
	w.logger.Error(message, fields...)
}
