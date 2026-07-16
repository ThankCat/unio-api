package workers

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

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
}

// OrphanReservationFinalizer 定义在单事务内释放孤儿冻结并把请求收口为 failed 的业务能力。
type OrphanReservationFinalizer interface {
	FinalizeOrphanReservation(ctx context.Context, reservation sqlc.LedgerReservation) error
}

// OrphanReservationSweeperWorker 周期扫描并收口进程崩溃遗留的「孤儿」预授权（status=authorized、请求永久 running、
// 无 settlement 补偿任务）：释放冻结余额、记 risk_exposure 上界敞口、把请求推进到 failed。
//
// 与 settlement_recovery worker 严格互补：扫描查询用 NOT EXISTS 排除有补偿任务的预授权，绝不在此释放
// 「上游可能已成功、等待 capture」的冻结，避免误释放导致白嫖。
type OrphanReservationSweeperWorker struct {
	store        OrphanReservationStore
	finalizer    OrphanReservationFinalizer
	logger       *slog.Logger
	ageThreshold time.Duration
	batchSize    int32
}

// NewOrphanReservationSweeperWorker 创建孤儿预授权清扫 worker。
func NewOrphanReservationSweeperWorker(store OrphanReservationStore, finalizer OrphanReservationFinalizer, logger *slog.Logger, ageThreshold time.Duration, batchSize int32) *OrphanReservationSweeperWorker {
	if store == nil {
		panic("workers: orphan reservation store is required")
	}
	if finalizer == nil {
		panic("workers: orphan reservation finalizer is required")
	}
	if logger == nil {
		logger = slog.Default()
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
// 返回 true（本批非空）让 runner 立即再跑一轮，直至排空。
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
		if err := w.finalizer.FinalizeOrphanReservation(ctx, reservation); err != nil {
			w.logger.Error("orphan reservation sweep failed",
				append([]any{
					"worker", w.Name(),
					"reservation_id", reservation.ID,
					"request_record_id", reservation.RequestRecordID,
				}, failure.LogArgs(err)...)...)
			continue
		}
		swept++
	}

	if swept > 0 {
		// 孤儿预授权应当极其罕见；批量出现意味着曾发生进程崩溃，附 alert 键便于告警路由。
		w.logger.Warn("orphan reservations reclaimed",
			"worker", w.Name(),
			"swept", swept,
			"batch", len(rows),
			"age_threshold", w.ageThreshold.String(),
			"alert", "orphan_reservation_reclaimed",
		)
	}

	return true, nil
}
