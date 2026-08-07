package channelmodelinventory

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/adapter/modeldiscovery"
	corechannel "github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

const maxDiscoveryAttempts = 3

func (s *Service) CreateDiscovery(ctx context.Context, channelID int64, source string) (Run, error) {
	if channelID <= 0 {
		return Run{}, invalidArgument("channel_id", "channel id must be positive")
	}
	if source != DiscoverySourceManual && source != DiscoverySourceSetup {
		return Run{}, invalidArgument("source", "source must be manual or setup")
	}
	row, err := s.queries.CreateChannelModelDiscoveryRun(ctx, sqlc.CreateChannelModelDiscoveryRunParams{
		Source: source, ChannelID: channelID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, notFound("channel not found or archived")
		}
		if isUniqueViolation(err) {
			return Run{}, conflict("channel already has an active model discovery")
		}
		return Run{}, storeFailed(err, "create channel model discovery")
	}
	return discoveryRun(row), nil
}

func (s *Service) GetDiscovery(ctx context.Context, channelID, runID int64) (Run, error) {
	row, err := s.queries.GetChannelModelDiscoveryRun(ctx, sqlc.GetChannelModelDiscoveryRunParams{RunID: runID, ChannelID: channelID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, notFound("model discovery run not found")
		}
		return Run{}, storeFailed(err, "get channel model discovery")
	}
	return discoveryRun(row), nil
}

func (s *Service) ListDiscoveries(ctx context.Context, channelID int64, limit, offset int32) (RunPage, error) {
	if channelID <= 0 || limit <= 0 || limit > 100 || offset < 0 {
		return RunPage{}, invalidArgument("page", "invalid discovery history pagination")
	}
	rows, err := s.queries.ListChannelModelDiscoveryRuns(ctx, sqlc.ListChannelModelDiscoveryRunsParams{
		ChannelID: channelID, PageLimit: limit, PageOffset: offset,
	})
	if err != nil {
		return RunPage{}, storeFailed(err, "list channel model discoveries")
	}
	total, err := s.queries.CountChannelModelDiscoveryRuns(ctx, channelID)
	if err != nil {
		return RunPage{}, storeFailed(err, "count channel model discoveries")
	}
	items := make([]Run, 0, len(rows))
	for _, row := range rows {
		items = append(items, discoveryRun(row))
	}
	return RunPage{Items: items, Total: total}, nil
}

// EnqueueDueDiscoveries 为到期的启用渠道创建 scheduled run；不会执行网络调用。
func (s *Service) EnqueueDueDiscoveries(ctx context.Context) (int, error) {
	settings := appsettings.AdminBackendChannelModelDiscovery(ctx, s.settings)
	if !settings.Enabled {
		return 0, nil
	}
	seconds := int32(math.Ceil(settings.Interval.Seconds()))
	rows, err := s.queries.EnqueueDueChannelModelDiscoveries(ctx, seconds)
	if err != nil {
		return 0, storeFailed(err, "enqueue due channel model discoveries")
	}
	return len(rows), nil
}

// ExecuteNextDiscovery 领取并执行一个发现任务。返回 false 表示当前没有到期任务。
func (s *Service) ExecuteNextDiscovery(ctx context.Context) (bool, error) {
	if s.lister == nil {
		return false, failure.New(failure.CodeAdapterInvalidRegistration, failure.WithMessage("model lister is unavailable"))
	}
	run, err := s.queries.ClaimNextChannelModelDiscoveryRun(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, storeFailed(err, "claim channel model discovery")
	}

	snapshot, err := s.queries.GetChannelModelDiscoveryExecutionSnapshot(ctx, run.ID)
	if err != nil {
		return true, storeFailed(err, "load channel model discovery snapshot")
	}
	if discoverySnapshotStale(snapshot) {
		_, err = s.queries.FinishChannelModelDiscoveryRun(ctx, sqlc.FinishChannelModelDiscoveryRunParams{RunID: run.ID})
		if err != nil {
			return true, storeFailed(err, "finish stale channel model discovery")
		}
		return true, nil
	}

	settings := appsettings.AdminBackendChannelModelDiscovery(ctx, s.settings)
	workCtx, cancel := context.WithTimeout(ctx, settings.Timeout)
	result, listErr := s.lister.ListChannelModels(workCtx, snapshot.Protocol, snapshot.AdapterKey, corechannel.Runtime{
		ID: snapshot.ChannelID, Origin: snapshot.Origin, APIKey: strings.TrimSpace(snapshot.Credential),
		ProviderSlug: snapshot.ProviderSlug, ResponseTimeout: settings.Timeout,
	})
	cancel()
	if listErr != nil {
		return true, s.finishDiscoveryError(ctx, run, listErr)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return true, storeFailed(err, "begin channel model discovery transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)
	for _, item := range result.Items {
		var created pgtype.Timestamptz
		if item.CreatedAt != nil {
			created = pgtype.Timestamptz{Time: *item.CreatedAt, Valid: true}
		}
		if err := q.InsertChannelModelDiscoveryItem(ctx, sqlc.InsertChannelModelDiscoveryItemParams{
			RunID: run.ID, UpstreamModel: item.ID, OwnedBy: textParam(item.OwnedBy), UpstreamCreatedAt: created,
		}); err != nil {
			return true, storeFailed(err, "insert channel model discovery item")
		}
	}
	warning := ""
	if len(result.Items) == 0 {
		warning = "empty_result"
	}
	finished, err := q.FinishChannelModelDiscoveryRun(ctx, sqlc.FinishChannelModelDiscoveryRunParams{
		RunID: run.ID, ModelCount: int32(len(result.Items)), WarningCode: textParam(warning),
	})
	if err != nil {
		return true, storeFailed(err, "finish channel model discovery")
	}
	if err := tx.Commit(ctx); err != nil {
		return true, storeFailed(err, "commit channel model discovery")
	}
	_, _ = s.queries.DeleteOldChannelModelDiscoveryRuns(ctx, sqlc.DeleteOldChannelModelDiscoveryRunsParams{
		TargetChannelID: run.ChannelID, KeepPerChannel: int32(settings.RetentionPerChannel),
	})
	_ = finished
	return true, nil
}

func discoverySnapshotStale(row sqlc.GetChannelModelDiscoveryExecutionSnapshotRow) bool {
	return row.ChannelStatus == "archived" || row.ChannelConfigRevision != row.CurrentChannelConfigRevision ||
		row.ProviderOriginRevision != row.CurrentProviderOriginRevision ||
		row.ProviderStatusRevision != row.CurrentProviderStatusRevision
}

func (s *Service) finishDiscoveryError(ctx context.Context, run sqlc.ChannelModelDiscoveryRun, err error) error {
	code, message, retryAfter := classifyDiscoveryError(err)
	if retryableDiscoveryCode(code) && run.AttemptCount < maxDiscoveryAttempts {
		backoff := 5 * time.Second * time.Duration(1<<max(0, int(run.AttemptCount)-1))
		if retryAfter > backoff {
			backoff = retryAfter
		}
		if backoff > 5*time.Minute {
			backoff = 5 * time.Minute
		}
		_, updateErr := s.queries.RetryChannelModelDiscoveryRun(ctx, sqlc.RetryChannelModelDiscoveryRunParams{
			BackoffSeconds: int32(math.Ceil(backoff.Seconds())), ErrorCode: textParam(code), Message: textParam(message), RunID: run.ID,
		})
		if updateErr != nil {
			return storeFailed(updateErr, "retry channel model discovery")
		}
		return nil
	}
	_, updateErr := s.queries.FailChannelModelDiscoveryRun(ctx, sqlc.FailChannelModelDiscoveryRunParams{
		ErrorCode: textParam(code), Message: textParam(message), RunID: run.ID,
	})
	if updateErr != nil {
		return storeFailed(updateErr, "fail channel model discovery")
	}
	return nil
}

func classifyDiscoveryError(err error) (string, string, time.Duration) {
	if discovered, ok := modeldiscovery.ErrorOf(err); ok {
		message := map[string]string{
			modeldiscovery.CodeCredentialInvalid:   "上游拒绝凭据（401）",
			modeldiscovery.CodePermissionDenied:    "当前凭据无权读取模型列表（403）",
			modeldiscovery.CodeUnsupportedEndpoint: "上游不支持模型列表接口（404/405），请使用手工绑定",
			modeldiscovery.CodeRateLimited:         "上游模型列表接口限流（429）",
			modeldiscovery.CodeTimeout:             "读取上游模型列表超时",
			modeldiscovery.CodeUnreachable:         "无法连接上游模型列表接口",
			modeldiscovery.CodeProtocolError:       "上游模型列表响应不符合协议或超过安全限制",
			modeldiscovery.CodeUpstreamError:       "上游模型列表接口暂时异常",
			modeldiscovery.CodeCanceled:            "模型发现已取消",
		}[discovered.Code]
		if message == "" {
			message = "上游模型发现失败"
		}
		return discovered.Code, message, discovered.RetryAfter
	}
	return modeldiscovery.CodeUnsupportedEndpoint, "当前协议适配器不支持上游模型发现，请使用手工绑定", 0
}

func retryableDiscoveryCode(code string) bool {
	return code == modeldiscovery.CodeRateLimited || code == modeldiscovery.CodeTimeout ||
		code == modeldiscovery.CodeUnreachable || code == modeldiscovery.CodeUpstreamError
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
