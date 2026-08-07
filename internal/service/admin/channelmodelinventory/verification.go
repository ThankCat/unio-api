package channelmodelinventory

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	corechannel "github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/providerledger"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

const maxVerificationBatch = 50

const (
	VerificationErrorCredentialInvalid = "credential_invalid"
	VerificationErrorPermissionDenied  = "permission_denied"
	VerificationErrorModelUnavailable  = "model_unavailable"
	VerificationErrorRateLimited       = "rate_limited"
	VerificationErrorTimeout           = "timeout"
	VerificationErrorUnreachable       = "unreachable"
	VerificationErrorProtocol          = "protocol_error"
	VerificationErrorUpstream          = "upstream_error"
	VerificationErrorCanceled          = "canceled"
	VerificationErrorAccounting        = "accounting_error"
)

type VerificationResult struct {
	Run   Run
	Items []VerificationItem
}

type VerificationTarget struct {
	ModelID       int64
	UpstreamModel string
}

type VerificationItem struct {
	ID                    int64
	RunID                 int64
	ModelID               int64
	UpstreamModel         string
	Status                string
	Success               *bool
	HTTPStatus            int32
	ErrorCode             string
	Message               string
	LatencyMs             *int64
	ProviderProbeRecordID *int64
	CreatedAt             time.Time
	CompletedAt           *time.Time
}

func (s *Service) CreateVerification(
	ctx context.Context,
	channelID int64,
	targets []VerificationTarget,
	source string,
) (VerificationResult, error) {
	if channelID <= 0 {
		return VerificationResult{}, invalidArgument("channel_id", "channel id must be positive")
	}
	if source != VerificationSourceManual && source != VerificationSourceSetup {
		return VerificationResult{}, invalidArgument("source", "source must be manual or setup")
	}
	targets, err := normalizeVerificationTargets(targets)
	if err != nil {
		return VerificationResult{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return VerificationResult{}, storeFailed(err, "begin channel model verification transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)
	run, err := q.CreateChannelModelVerificationRun(ctx, sqlc.CreateChannelModelVerificationRunParams{
		Source: source, TotalCount: int32(len(targets)), ChannelID: channelID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return VerificationResult{}, notFound("channel not found or archived")
		}
		if isUniqueViolation(err) {
			return VerificationResult{}, conflict("channel already has an active model verification")
		}
		return VerificationResult{}, storeFailed(err, "create channel model verification")
	}
	items := make([]VerificationItem, 0, len(targets))
	for _, target := range targets {
		item, createErr := q.CreateChannelModelVerificationItem(ctx, sqlc.CreateChannelModelVerificationItemParams{
			UpstreamModel: textParam(target.UpstreamModel), ModelID: target.ModelID, RunID: run.ID,
		})
		if createErr != nil {
			if errors.Is(createErr, pgx.ErrNoRows) {
				return VerificationResult{}, invalidArgument("targets", fmt.Sprintf("model %d is not bound to the channel", target.ModelID))
			}
			return VerificationResult{}, storeFailed(createErr, "create channel model verification item")
		}
		items = append(items, verificationItem(item))
	}
	if err := tx.Commit(ctx); err != nil {
		return VerificationResult{}, storeFailed(err, "commit channel model verification")
	}
	return VerificationResult{Run: verificationRun(run), Items: items}, nil
}

func (s *Service) GetVerification(ctx context.Context, channelID, runID int64) (VerificationResult, error) {
	run, err := s.queries.GetChannelModelVerificationRun(ctx, sqlc.GetChannelModelVerificationRunParams{
		RunID: runID, ChannelID: channelID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return VerificationResult{}, notFound("model verification run not found")
		}
		return VerificationResult{}, storeFailed(err, "get channel model verification")
	}
	rows, err := s.queries.ListChannelModelVerificationItems(ctx, runID)
	if err != nil {
		return VerificationResult{}, storeFailed(err, "list channel model verification items")
	}
	items := make([]VerificationItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, verificationItem(row))
	}
	return VerificationResult{Run: verificationRun(run), Items: items}, nil
}

// ExecuteNextVerification 领取并执行一个批量验证任务。
func (s *Service) ExecuteNextVerification(ctx context.Context) (bool, error) {
	if s.prober == nil || s.accountant == nil {
		return false, conflict("model verification dependencies are unavailable")
	}
	run, err := s.queries.ClaimNextChannelModelVerificationRun(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, storeFailed(err, "claim channel model verification")
	}
	snapshot, err := s.queries.GetChannelModelVerificationExecutionSnapshot(ctx, run.ID)
	if err != nil {
		return true, storeFailed(err, "load channel model verification snapshot")
	}
	items, err := s.queries.ListChannelModelVerificationItems(ctx, run.ID)
	if err != nil {
		return true, storeFailed(err, "list channel model verification work")
	}
	if verificationSnapshotStale(snapshot) {
		_, _ = s.queries.SkipRemainingChannelModelVerificationItems(ctx, sqlc.SkipRemainingChannelModelVerificationItemsParams{
			ErrorCode: textParam("stale_revision"), Message: textParam("渠道或 Provider 配置已变化"), RunID: run.ID,
		})
		_, finishErr := s.queries.FinishChannelModelVerificationRun(ctx, sqlc.FinishChannelModelVerificationRunParams{RunID: run.ID})
		if finishErr != nil {
			return true, storeFailed(finishErr, "finish stale channel model verification")
		}
		return true, nil
	}

	probeTimeout := appsettings.AdminBackendChannelTestProbeTimeout(ctx, s.settings)
	runtime := corechannel.Runtime{
		ID: snapshot.ChannelID, Origin: snapshot.Origin, APIKey: strings.TrimSpace(snapshot.Credential),
		ProviderSlug: snapshot.ProviderSlug, ResponseTimeout: probeTimeout,
	}
	var terminalCode, terminalMessage string
	for _, item := range items {
		start := time.Now()
		probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		probeResult, probeErr := s.prober.ProbeChannel(probeCtx, snapshot.Protocol, snapshot.AdapterKey, runtime, item.UpstreamModel)
		latency := time.Since(start).Milliseconds()
		cancel()

		success := probeErr == nil
		errorCode, message := "", ""
		if probeErr != nil {
			errorCode, message = classifyVerificationError(probeErr, probeTimeout)
		}
		idempotencyKey := "model-verification:" + uuid.NewString()
		accountErr := s.accountant.AccountProbe(ctx, providerledger.ProbeParams{
			ProviderID: snapshot.ProviderID, ChannelID: snapshot.ChannelID, ModelID: item.ModelID,
			Protocol: snapshot.Protocol, Source: "model_verification", UpstreamModel: item.UpstreamModel,
			Success: success, HTTPStatus: probeResult.StatusCode, ErrorCode: errorCode, Message: message,
			LatencyMs: latency, StartedAt: start.UTC(), Facts: probeResult.Facts, IdempotencyKey: idempotencyKey,
		})
		var probeID pgtype.Int8
		if accountErr == nil {
			probe, getErr := s.queries.GetProviderProbeRecordByIdempotencyKey(ctx, idempotencyKey)
			if getErr != nil {
				accountErr = getErr
			} else {
				probeID = pgtype.Int8{Int64: probe.ID, Valid: true}
			}
		}
		if accountErr != nil {
			success = false
			errorCode = VerificationErrorAccounting
			message = "探测事实或成本记录失败"
		}

		_, completeErr := s.queries.CompleteChannelModelVerificationItem(ctx, sqlc.CompleteChannelModelVerificationItemParams{
			Success: pgtype.Bool{Bool: success, Valid: true}, HttpStatus: int32(probeResult.StatusCode),
			ErrorCode: textParam(errorCode), Message: textParam(message), LatencyMs: pgtype.Int8{Int64: latency, Valid: true},
			ProviderProbeRecordID: probeID, ItemID: item.ID,
		})
		if completeErr != nil {
			return true, storeFailed(completeErr, "complete channel model verification item")
		}
		if accountErr != nil || verificationFailureStopsBatch(errorCode) {
			terminalCode, terminalMessage = errorCode, message
			_, _ = s.queries.SkipRemainingChannelModelVerificationItems(ctx, sqlc.SkipRemainingChannelModelVerificationItemsParams{
				ErrorCode: textParam(errorCode), Message: textParam("因渠道级失败停止后续模型验证：" + message), RunID: run.ID,
			})
			break
		}
	}
	_, err = s.queries.FinishChannelModelVerificationRun(ctx, sqlc.FinishChannelModelVerificationRunParams{
		ErrorCode: textParam(terminalCode), Message: textParam(terminalMessage), RunID: run.ID,
	})
	if err != nil {
		return true, storeFailed(err, "finish channel model verification")
	}
	return true, nil
}

func verificationSnapshotStale(row sqlc.GetChannelModelVerificationExecutionSnapshotRow) bool {
	return row.ChannelStatus == "archived" || row.ChannelConfigRevision != row.CurrentChannelConfigRevision ||
		row.ProviderOriginRevision != row.CurrentProviderOriginRevision ||
		row.ProviderStatusRevision != row.CurrentProviderStatusRevision
}

func classifyVerificationError(err error, timeout time.Duration) (string, string) {
	category, ok := adapter.UpstreamCategoryOf(err)
	metadata, _ := adapter.UpstreamMetadataOf(err)
	if !ok || (category == adapter.UpstreamErrorUnknown && metadata.StatusCode >= 200 && metadata.StatusCode < 300) {
		return VerificationErrorProtocol, "响应解析失败或协议不符"
	}
	switch category {
	case adapter.UpstreamErrorAuth:
		return VerificationErrorCredentialInvalid, "凭据无效或未授权（401）"
	case adapter.UpstreamErrorPermission:
		return VerificationErrorPermissionDenied, "当前凭据无权使用该模型（403）"
	case adapter.UpstreamErrorRateLimit:
		return VerificationErrorRateLimited, "上游限流（429）"
	case adapter.UpstreamErrorTimeout:
		return VerificationErrorTimeout, fmt.Sprintf("模型验证在 %.0fs 内未完成", timeout.Seconds())
	case adapter.UpstreamErrorBadRequest:
		if metadata.StatusCode == http.StatusNotFound {
			return VerificationErrorModelUnavailable, "上游未找到该模型（404）"
		}
		return VerificationErrorModelUnavailable, fmt.Sprintf("上游拒绝该模型请求（%d）", metadata.StatusCode)
	case adapter.UpstreamErrorCanceled:
		return VerificationErrorCanceled, "模型验证已取消"
	case adapter.UpstreamErrorServer:
		if metadata.StatusCode == 0 {
			return VerificationErrorUnreachable, "无法连接上游"
		}
		return VerificationErrorUpstream, fmt.Sprintf("上游服务端错误（%d）", metadata.StatusCode)
	default:
		if metadata.StatusCode == 0 {
			return VerificationErrorUnreachable, "无法连接上游"
		}
		return VerificationErrorUpstream, fmt.Sprintf("上游调用失败（%d）", metadata.StatusCode)
	}
}

func verificationFailureStopsBatch(code string) bool {
	return code == VerificationErrorCredentialInvalid || code == VerificationErrorRateLimited ||
		code == VerificationErrorTimeout || code == VerificationErrorUnreachable ||
		code == VerificationErrorProtocol || code == VerificationErrorUpstream || code == VerificationErrorCanceled
}

func verificationItem(row sqlc.ChannelModelVerificationItem) VerificationItem {
	result := VerificationItem{
		ID: row.ID, RunID: row.RunID, ModelID: row.ModelID, UpstreamModel: row.UpstreamModel,
		Status: row.Status, HTTPStatus: row.HttpStatus, ErrorCode: textValue(row.ErrorCode),
		Message: textValue(row.Message), CreatedAt: row.CreatedAt.Time, CompletedAt: timeValue(row.CompletedAt),
	}
	if row.Success.Valid {
		value := row.Success.Bool
		result.Success = &value
	}
	if row.LatencyMs.Valid {
		value := row.LatencyMs.Int64
		result.LatencyMs = &value
	}
	if row.ProviderProbeRecordID.Valid {
		value := row.ProviderProbeRecordID.Int64
		result.ProviderProbeRecordID = &value
	}
	return result
}

func normalizeVerificationTargets(values []VerificationTarget) ([]VerificationTarget, error) {
	if len(values) == 0 || len(values) > maxVerificationBatch {
		return nil, invalidArgument("targets", "targets must contain 1 to 50 items")
	}
	seen := make(map[int64]string, len(values))
	result := make([]VerificationTarget, 0, len(values))
	for _, value := range values {
		if value.ModelID <= 0 {
			return nil, invalidArgument("targets", "each target requires a positive model_id")
		}
		upstream := strings.TrimSpace(value.UpstreamModel)
		if previous, ok := seen[value.ModelID]; ok {
			if previous != upstream {
				return nil, invalidArgument("targets", fmt.Sprintf("model %d appears with different upstream models", value.ModelID))
			}
			continue
		}
		seen[value.ModelID] = upstream
		result = append(result, VerificationTarget{ModelID: value.ModelID, UpstreamModel: upstream})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ModelID < result[j].ModelID })
	return result, nil
}
