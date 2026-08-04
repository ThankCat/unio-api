package workers

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// StrandedReservationStore 定义清扫 worker 扫描搁浅预授权所需的存储能力。
type StrandedReservationStore interface {
	ListStrandedAuthorizedReservations(ctx context.Context, arg sqlc.ListStrandedAuthorizedReservationsParams) ([]sqlc.LedgerReservation, error)
}

// StrandedReservationFinalizer 定义在单事务内释放搁浅冻结的业务能力。
type StrandedReservationFinalizer interface {
	FinalizeStrandedReservation(ctx context.Context, reservation sqlc.LedgerReservation) error
}

// StrandedReservationSweeperWorker 周期扫描并回收「搁浅」预授权：请求已进入 failed/canceled 终态，
// 冻结余额却仍停留在 authorized。成因是网关失败路径「先 release 再写终态」两步非原子——release 自身
// 失败而随后的审计写入成功，残留同时落在孤儿清扫（只捞仍 running 的请求）与 settlement recovery 之外。
//
// 与另外两条兜底路径的边界：扫描查询用 NOT EXISTS 排除有补偿任务的预授权（那些归 settlement recovery），
// 用 r.status IN ('failed','canceled') 与孤儿清扫互斥。succeeded 不在回收范围内。
type StrandedReservationSweeperWorker struct {
	store        StrandedReservationStore
	finalizer    StrandedReservationFinalizer
	logger       *zap.Logger
	ageThreshold time.Duration
	batchSize    int32
}

// NewStrandedReservationSweeperWorker 创建搁浅预授权清扫 worker。
func NewStrandedReservationSweeperWorker(store StrandedReservationStore, finalizer StrandedReservationFinalizer, logger *zap.Logger, ageThreshold time.Duration, batchSize int32) *StrandedReservationSweeperWorker {
	if store == nil {
		panic("workers: stranded reservation store is required")
	}
	if finalizer == nil {
		panic("workers: stranded reservation finalizer is required")
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

	return &StrandedReservationSweeperWorker{
		store:        store,
		finalizer:    finalizer,
		logger:       logger,
		ageThreshold: ageThreshold,
		batchSize:    batchSize,
	}
}

// Name 返回 worker 名称。
func (w *StrandedReservationSweeperWorker) Name() string {
	return "stranded_reservation_sweeper"
}

// RunOnce 扫描并回收一批到期的搁浅预授权。
//
// 单条回收失败不阻断整批：记日志继续，下一 tick 安全重放（FinalizeStrandedReservation 以「请求为终态
// 且冻结仍 authorized」为幂等闸门）。返回 true（本批非空）让 runner 立即再跑一轮，直至排空。
func (w *StrandedReservationSweeperWorker) RunOnce(ctx context.Context) (bool, error) {
	cutoff := time.Now().Add(-w.ageThreshold)

	rows, err := w.store.ListStrandedAuthorizedReservations(ctx, sqlc.ListStrandedAuthorizedReservationsParams{
		CreatedBefore: pgtype.Timestamptz{Time: cutoff, Valid: true},
		BatchLimit:    w.batchSize,
	})
	if err != nil {
		return false, failure.Wrap(
			failure.CodeGatewayRequestStrandedReclaimed,
			err,
			failure.WithMessage("list stranded authorized reservations"),
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
		if err := w.finalizer.FinalizeStrandedReservation(ctx, reservation); err != nil {
			fields := append([]zap.Field{
				zap.String("worker", w.Name()),
				zap.Int64("reservation_id", reservation.ID),
				zap.Int64("request_record_id", reservation.RequestRecordID),
			}, failure.LogFields(err)...)
			w.logger.Error("stranded reservation sweep failed", fields...)
			continue
		}
		swept++
	}

	if swept > 0 {
		// 搁浅预授权意味着曾有一次 release 失败而请求仍被收口，属于需要人工追查的账务异常，附 alert 键便于告警路由。
		w.logger.Warn("stranded reservations reclaimed",
			zap.String("worker", w.Name()),
			zap.Int("swept", swept),
			zap.Int("batch", len(rows)),
			zap.String("age_threshold", w.ageThreshold.String()),
			zap.String("alert", "stranded_reservation_reclaimed"),
		)
	}

	return true, nil
}
