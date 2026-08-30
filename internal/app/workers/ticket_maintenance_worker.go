package workers

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// TicketMaintenanceStore 定义工单维护所需的存储能力，由 *sqlc.Queries 满足。
type TicketMaintenanceStore interface {
	AutoCloseResolvedTickets(ctx context.Context, cutoff pgtype.Timestamptz) (int64, error)
	DeleteOrphanTicketAttachments(ctx context.Context, cutoff pgtype.Timestamptz) (int64, error)
}

// TicketMaintenanceWorker 周期执行两件工单维护事务：
//  1. resolved 工单超过 autoCloseAfter 无新消息 → 自动关闭；
//  2. 孤儿附件（上传后始终未随消息提交）超过 orphanTTL → 删除。
type TicketMaintenanceWorker struct {
	store          TicketMaintenanceStore
	logger         *zap.Logger
	interval       time.Duration
	autoCloseAfter time.Duration
	orphanTTL      time.Duration
	now            func() time.Time

	nextRunAt time.Time
}

// NewTicketMaintenanceWorker 创建工单维护 worker。
func NewTicketMaintenanceWorker(
	store TicketMaintenanceStore,
	logger *zap.Logger,
	interval, autoCloseAfter, orphanTTL time.Duration,
) *TicketMaintenanceWorker {
	if store == nil {
		panic("workers: ticket maintenance store is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	if autoCloseAfter <= 0 {
		autoCloseAfter = 168 * time.Hour
	}
	if orphanTTL <= 0 {
		orphanTTL = 24 * time.Hour
	}
	return &TicketMaintenanceWorker{
		store:          store,
		logger:         logger,
		interval:       interval,
		autoCloseAfter: autoCloseAfter,
		orphanTTL:      orphanTTL,
		now:            time.Now,
	}
}

// Name 返回 worker 名称。
func (w *TicketMaintenanceWorker) Name() string { return "ticket_maintenance" }

// RunOnce 到期时执行一轮维护；两个动作彼此独立，单个失败不影响另一个下轮重试。
func (w *TicketMaintenanceWorker) RunOnce(ctx context.Context) (bool, error) {
	now := w.now()
	if now.Before(w.nextRunAt) {
		return false, nil
	}
	w.nextRunAt = now.Add(w.interval)

	worked := false
	closed, err := w.store.AutoCloseResolvedTickets(ctx, pgtype.Timestamptz{
		Time:  now.Add(-w.autoCloseAfter),
		Valid: true,
	})
	if err != nil {
		w.logger.Error("ticket maintenance: auto close resolved tickets failed", failure.LogFields(err)...)
	} else if closed > 0 {
		worked = true
		w.logger.Info("ticket maintenance: auto closed resolved tickets", zap.Int64("count", closed))
	}

	deleted, err := w.store.DeleteOrphanTicketAttachments(ctx, pgtype.Timestamptz{
		Time:  now.Add(-w.orphanTTL),
		Valid: true,
	})
	if err != nil {
		w.logger.Error("ticket maintenance: delete orphan attachments failed", failure.LogFields(err)...)
	} else if deleted > 0 {
		worked = true
		w.logger.Info("ticket maintenance: deleted orphan attachments", zap.Int64("count", deleted))
	}
	return worked, nil
}
