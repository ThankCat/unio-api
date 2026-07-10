package query

import (
	"context"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/opsutil"
	"github.com/ThankCat/unio-api/internal/service/appsettings"
)

// ChannelHealthStore 定义系统级 channel 健康只读聚合所需的存储能力（M8）。
type ChannelHealthStore interface {
	SystemChannelHealth(ctx context.Context, arg sqlc.SystemChannelHealthParams) ([]sqlc.SystemChannelHealthRow, error)
}

// ChannelHealth 是单个 channel 的健康明细（按区间内 attempt 成功率派生）。
//
// channel 熔断是 gateway 进程内内存态（见 lifecycle/breaker.go），admin 跨进程读不到实时电路；
// 这里的 Bucket 是从落库的 request_attempts 派生的近似，供运营观测，而非熔断器实时状态。
type ChannelHealth struct {
	ChannelID             int64
	Name                  string
	Status                string
	ProviderID            int64
	AttemptTotal          int64
	AttemptSucceeded      int64
	AttemptFailed         int64
	AttemptUpstreamFailed int64
	AttemptCanceled       int64
	SuccessRate           float64
	LastAttemptAt         *time.Time
	Bucket                string // healthy / degraded / unhealthy / no_data
}

// ChannelHealthService 提供系统级 channel 健康只读聚合。
type ChannelHealthService struct {
	store ChannelHealthStore
	// settings 供每请求现读健康分桶阈值(admin_backend.channel_health_thresholds);
	// nil(单测)回代码默认。
	settings *appsettings.SettingsStore
}

// NewChannelHealthService 创建 channel 健康只读聚合服务。
func NewChannelHealthService(store ChannelHealthStore, settings *appsettings.SettingsStore) *ChannelHealthService {
	return &ChannelHealthService{store: store, settings: settings}
}

// List 在可选时间范围内返回每个 channel 的健康明细（失败多者靠前）。
func (s *ChannelHealthService) List(ctx context.Context, from, to *time.Time) ([]ChannelHealth, error) {
	rows, err := s.store.SystemChannelHealth(ctx, sqlc.SystemChannelHealthParams{
		FromTime: tsNarg(from),
		ToTime:   tsNarg(to),
	})
	if err != nil {
		return nil, storeFailed(err, "aggregate system channel health")
	}

	th := appsettings.AdminBackendChannelHealthThresholds(ctx, s.settings)
	out := make([]ChannelHealth, 0, len(rows))
	for _, row := range rows {
		ch := ChannelHealth{
			ChannelID:             row.ChannelID,
			Name:                  row.Name,
			Status:                row.Status,
			ProviderID:            row.ProviderID,
			AttemptTotal:          row.AttemptTotal,
			AttemptSucceeded:      row.AttemptSucceeded,
			AttemptFailed:         row.AttemptFailed,
			AttemptUpstreamFailed: row.AttemptUpstreamFailed,
			AttemptCanceled:       row.AttemptCanceled,
			LastAttemptAt:         timePtr(row.LastAttemptAt),
		}
		// 健康分桶按「合格 attempt」= succeeded + 上游故障失败（fault_party='upstream'），
		// 排除客户端取消 / 进行中 / 平台错误 / 上游 4xx（bad_request），与运行时熔断器 IsChannelFaultError 一致，
		// 不因客户端取消或我方/请求本身问题误判渠道不健康。
		eligible := row.AttemptSucceeded + row.AttemptUpstreamFailed
		if eligible > 0 {
			ch.SuccessRate = float64(row.AttemptSucceeded) / float64(eligible)
		}
		ch.Bucket = opsutil.HealthBucket(row.AttemptSucceeded, eligible, th.HealthyRate, th.DegradedRate)
		out = append(out, ch)
	}
	return out, nil
}
