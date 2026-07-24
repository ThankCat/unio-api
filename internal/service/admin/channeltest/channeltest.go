// Package channeltest 编排 admin 管理端的「渠道检测 / 一键测渠道」（阶段一）。
//
// 检测 = 用渠道自己的 base_url + 凭据，挑一个该渠道绑定的模型，向真实上游发一个最小 "hi" 请求，
// 验证「连得上 + 凭据有效 + 模型可用」；成功记录延迟，失败把上游错误翻译成可读原因（凭据无效 /
// 模型不可用 / 超时 / 连不上 / 限流 …）。它复用与网关完全一致的 adapter/HTTP 链路，故结果=真实行为。
//
// 探测超时取自运行时配置 admin_backend.channel_test，与用户请求的
// channels.timeout_ms / gateway.default_channel_timeout_ms 完全正交——检测专用、互不影响。
//
// 阶段一只报告不摘除：检测结果只落「最近一次检测」四列，绝不改渠道启停状态；与被动熔断/cooldown 正交。
package channeltest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	corechannel "github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	adminchannel "github.com/ThankCat/unio-gateway/internal/service/admin/channel"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

// channelModelStatusEnabled 是 channel_models 启用状态值（与 DB 约束一致）。
const channelModelStatusEnabled = "enabled"

// 检测失败的稳定错误码（供前端按类型渲染 / 运营归因）。
const (
	ErrCodeCredentialInvalid = "credential_invalid" // 凭据无效 / 无权限（401/403）
	ErrCodeModelUnavailable  = "model_unavailable"  // 模型不可用 / 上游源站不存在（404/其余 4xx）
	ErrCodeTimeout           = "timeout"            // 超时（未在超时时间内响应）
	ErrCodeUnreachable       = "unreachable"        // 连不上（连接失败 / DNS / 网络错误）
	ErrCodeRateLimited       = "rate_limited"       // 上游限流（429，可重试，不代表渠道坏）
	ErrCodeProtocolError     = "protocol_error"     // 已连通但响应无法解析 / 协议不符
	ErrCodeUpstreamError     = "upstream_error"     // 上游服务端错误（5xx）或其他
	ErrCodeCanceled          = "canceled"           // 检测被取消
)

// Store 定义渠道检测所需的存储能力。
type Store interface {
	GetChannel(ctx context.Context, id int64) (sqlc.Channel, error)
	GetChannelProbeSnapshot(ctx context.Context, channelID int64) (sqlc.GetChannelProbeSnapshotRow, error)
	PrepareChannelCredentialRotation(ctx context.Context, arg sqlc.PrepareChannelCredentialRotationParams) (sqlc.PrepareChannelCredentialRotationRow, error)
	ApplyChannelProbeResult(ctx context.Context, arg sqlc.ApplyChannelProbeResultParams) (sqlc.ApplyChannelProbeResultRow, error)
	InsertPermissionRecheckLog(ctx context.Context, arg sqlc.InsertPermissionRecheckLogParams) (int64, error)
	ListChannelModelsByChannel(ctx context.Context, channelID int64) ([]sqlc.ListChannelModelsByChannelRow, error)
	ListChannelTestLogsByChannel(ctx context.Context, arg sqlc.ListChannelTestLogsByChannelParams) ([]sqlc.ChannelTestLog, error)
	CountChannelTestLogsByChannel(ctx context.Context, channelID int64) (int64, error)
}

// Prober 向渠道真实上游发一次最小请求（复用与网关一致的 adapter/HTTP 链路）。
// 由 gateway lifecycle 的 AdapterRegistry 实现，bootstrap 注入；此处以接口解耦，便于测试替身。
type Prober interface {
	ProbeChannel(ctx context.Context, protocol, adapterKey string, rt corechannel.Runtime, upstreamModel string) (int, error)
}

// 检测事件来源（写入 channel_test_logs.source）。
const (
	SourceManual            = "manual"             // 管理员在控制台手动点「检测」
	SourceWorker            = "worker"             // 渠道自动检测 worker 周期巡检
	SourceCredentialRotate  = "credential_rotate"  // credential PUT 保存后即时检测
	SourcePermissionRecheck = "permission_recheck" // 403 Channel-Model 自动复检（只写审计）
)

// TestInput 是一次渠道检测入参。
type TestInput struct {
	ChannelID int64
	// Model 可选：Unio 对外模型 ID 或直接的上游模型名；留空时自动取渠道第一个启用绑定模型。
	Model string
	// Source 是本次检测来源（manual/worker）；留空按 manual 处理。决定日志写入口径（R1(b)）。
	Source string
}

// TestResult 是一次渠道检测结果。它始终代表「检测已成功执行」；渠道是否健康看 Success。
type TestResult struct {
	Success       bool
	LatencyMs     int64
	TestedModel   string // 实际使用的上游模型名
	HTTPStatus    int    // 上游 HTTP 状态码（连接失败/超时未拿到响应时为 0）
	ErrorCode     string // 成功为空
	Message       string // 成功为空；失败为可读原因（归类后的中文说明）
	UpstreamError string // 失败时上游返回的原始错误体截断快照；成功/无响应体（连不上/超时）时为空
	TestedAt      time.Time
}

// PermissionRecheckInput 固化 403 发生时的内部绑定身份与三类 revision。
// ModelID 是数据库内部 models.id；Redis/worker 不传递模型字符串、credential、URL 或请求正文。
type PermissionRecheckInput struct {
	ChannelID               int64
	ModelID                 int64
	ChannelConfigRevision   int64
	OriginBaseURLRevision int64
	OriginStatusRevision  int64
}

// PermissionRecheckResult 是一次只针对指定绑定的真实探测结果。
// Stale 表示探测前或探测后 PostgreSQL 当前绑定已经不再匹配领取时身份，结果只能审计。
type PermissionRecheckResult struct {
	Probe TestResult
	Stale bool
}

// Service 编排渠道主动检测：选模型 → 构造 Runtime → 发探测请求 → 归类 → 落库。
type Service struct {
	store    Store
	prober   Prober
	settings *appsettings.SettingsStore
	metrics  CredentialRotationMetrics
}

// CredentialRotationMetrics records only the bounded five-state verification result.
type CredentialRotationMetrics interface {
	IncChannelCredentialRotationVerification(state string)
}

// NewService 创建渠道检测服务。settings 可为 nil（单测），此时探测超时回代码默认。
func NewService(store Store, prober Prober, settings *appsettings.SettingsStore) *Service {
	return &Service{
		store:    store,
		prober:   prober,
		settings: settings,
	}
}

// SetMetrics attaches optional credential-rotation telemetry.
func (s *Service) SetMetrics(recorder CredentialRotationMetrics) {
	if s != nil {
		s.metrics = recorder
	}
}

type probeSnapshot struct {
	ChannelID               int64
	Protocol                string
	AdapterKey              string
	Credential              string
	CredentialValid         bool
	ConfigRevision          int64
	ProviderSlug            string
	OriginBaseURL         string
	OriginBaseURLRevision int64
	OriginStatusRevision  int64
}

// Test 对指定渠道执行一次主动检测。读取、探测与结果回写均冻结三类 revision；迟到结果只写历史日志。
func (s *Service) Test(ctx context.Context, in TestInput) (TestResult, error) {
	if in.ChannelID <= 0 {
		return TestResult{}, invalidArgument("id", "channel id must be positive")
	}

	row, err := s.store.GetChannelProbeSnapshot(ctx, in.ChannelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TestResult{}, notFound("channel not found")
		}
		return TestResult{}, storeFailed(err, "get channel probe snapshot")
	}
	snapshot := probeSnapshotFromRow(row)
	workCtx, cancel := s.detachedOperationContext(ctx)
	defer cancel()

	result, err := s.executeProbe(workCtx, snapshot, strings.TrimSpace(in.Model))
	if err != nil {
		return TestResult{}, err
	}
	if _, err := s.applyProbeResult(workCtx, snapshot, in.Source, result); err != nil {
		return TestResult{}, storeFailed(err, "persist channel probe result")
	}
	return result, nil
}

// RecheckPermission 复用渠道检测 adapter 链路，对指定内部 model_id 的当前绑定发一次真实探测。
// 它只写 source=permission_recheck 审计，不调用 ApplyChannelProbeResult，因此 403/401/其它失败
// 都不会翻整个 Channel 的 credential_valid 或覆盖 last_test_*。调用方随后按 Stale/Success CAS 收口 Redis。
func (s *Service) RecheckPermission(ctx context.Context, in PermissionRecheckInput) (PermissionRecheckResult, error) {
	if in.ChannelID <= 0 || in.ModelID <= 0 || in.ChannelConfigRevision <= 0 ||
		in.OriginBaseURLRevision <= 0 || in.OriginStatusRevision <= 0 {
		return PermissionRecheckResult{}, invalidArgument("permission_recheck", "permission recheck identity is invalid")
	}

	snapshot, binding, stale, err := s.permissionRecheckSnapshot(ctx, in)
	if err != nil {
		return PermissionRecheckResult{}, err
	}
	if stale {
		result := PermissionRecheckResult{Stale: true, Probe: stalePermissionProbe(binding.UpstreamModel)}
		if snapshot.ChannelID > 0 {
			if err := s.insertPermissionRecheckAudit(ctx, in, result); err != nil {
				return PermissionRecheckResult{}, err
			}
		}
		return result, nil
	}

	probeTimeout := appsettings.AdminBackendChannelTestProbeTimeout(ctx, s.settings)
	workCtx, cancel := context.WithTimeout(ctx, probeTimeout+10*time.Second)
	probe := s.executeProbeCandidates(workCtx, snapshot, []string{binding.UpstreamModel})
	cancel()
	// permission_recheck 审计禁止持久化/向 worker 暴露上游响应 body；只保留稳定归类与状态码。
	probe.UpstreamError = ""

	// 探测可能跨过配置更新；完成后必须重新读取三类 revision 和同一 model_id 绑定。
	_, currentBinding, postProbeStale, err := s.permissionRecheckSnapshot(ctx, in)
	if err != nil {
		return PermissionRecheckResult{}, err
	}
	if !postProbeStale && currentBinding.UpstreamModel != binding.UpstreamModel {
		postProbeStale = true
	}
	result := PermissionRecheckResult{Probe: probe, Stale: postProbeStale}
	if result.Stale {
		result.Probe.Message = stalePermissionMessage(result.Probe.Message)
	}
	if err := s.insertPermissionRecheckAudit(ctx, in, result); err != nil {
		return PermissionRecheckResult{}, err
	}
	return result, nil
}

// RotateCredentialAndTest 原子保存 credential，并在独立有界 context 中用保存时快照即时检测。
// 保存成功后的任何检测编排错误都返回 execution_failed，不把已提交保存伪装成 HTTP 失败。
func (s *Service) RotateCredentialAndTest(ctx context.Context, in adminchannel.RotateCredentialInput) (result adminchannel.RotateCredentialResult, resultErr error) {
	defer func() {
		if resultErr == nil && result.CredentialSaved && result.Verification.State != "" && s.metrics != nil {
			s.metrics.IncChannelCredentialRotationVerification(string(result.Verification.State))
		}
	}()

	prepared, err := s.store.PrepareChannelCredentialRotation(ctx, sqlc.PrepareChannelCredentialRotationParams{
		ChannelID:  in.ID,
		Credential: strings.TrimSpace(in.Credential),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return adminchannel.RotateCredentialResult{}, notFound("channel not found")
		}
		return adminchannel.RotateCredentialResult{}, storeFailed(err, "save channel credential")
	}

	snapshot := probeSnapshotFromRotation(prepared)
	result = adminchannel.RotateCredentialResult{
		CredentialSaved:       true,
		CredentialChanged:     prepared.CredentialChanged,
		SavedConfigRevision:   prepared.ConfigRevision,
		CurrentConfigRevision: prepared.ConfigRevision,
	}
	if !prepared.CredentialChanged && prepared.CredentialValid {
		result.Verification = adminchannel.CredentialVerification{
			State:                adminchannel.CredentialVerificationNotRequired,
			CredentialValidAfter: true,
		}
		return result, nil
	}

	setTestedRevisions(&result.Verification, snapshot)
	workCtx, cancel := s.detachedOperationContext(ctx)
	defer cancel()

	probeResult, err := s.executeProbe(workCtx, snapshot, "")
	if err != nil {
		s.populateExecutionFailed(workCtx, &result, snapshot, nil)
		return result, nil
	}
	result.Verification.Result = credentialProbeResult(probeResult)

	applied, err := s.applyProbeResult(workCtx, snapshot, SourceCredentialRotate, probeResult)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			result.Verification.State = adminchannel.CredentialVerificationStale
			result.Verification.StateChangeApplied = false
			result.Verification.CredentialValidAfter = false
			return result, nil
		}
		s.populateExecutionFailed(workCtx, &result, snapshot, &probeResult)
		return result, nil
	}
	result.CurrentConfigRevision = applied.CurrentConfigRevision
	result.Verification.StateChangeApplied = applied.StateChangeApplied
	result.Verification.CredentialValidAfter = applied.CredentialValidAfter
	switch {
	case !applied.ResultApplied:
		result.Verification.State = adminchannel.CredentialVerificationStale
	case probeResult.Success:
		result.Verification.State = adminchannel.CredentialVerificationPassed
	default:
		result.Verification.State = adminchannel.CredentialVerificationFailed
	}
	return result, nil
}

func probeSnapshotFromRow(row sqlc.GetChannelProbeSnapshotRow) probeSnapshot {
	return probeSnapshot{
		ChannelID: row.ChannelID, Protocol: row.Protocol, AdapterKey: row.AdapterKey,
		Credential: row.Credential, CredentialValid: row.CredentialValid, ConfigRevision: row.ConfigRevision,
		ProviderSlug: row.ProviderSlug, OriginBaseURL: row.OriginBaseUrl,
		OriginBaseURLRevision: row.OriginBaseUrlRevision, OriginStatusRevision: row.OriginStatusRevision,
	}
}

func probeSnapshotFromRotation(row sqlc.PrepareChannelCredentialRotationRow) probeSnapshot {
	return probeSnapshot{
		ChannelID: row.ChannelID, Protocol: row.Protocol, AdapterKey: row.AdapterKey,
		Credential: row.Credential, CredentialValid: row.CredentialValid, ConfigRevision: row.ConfigRevision,
		ProviderSlug: row.ProviderSlug, OriginBaseURL: row.OriginBaseUrl,
		OriginBaseURLRevision: row.OriginBaseUrlRevision, OriginStatusRevision: row.OriginStatusRevision,
	}
}

func (s *Service) detachedOperationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	probeTimeout := appsettings.AdminBackendChannelTestProbeTimeout(ctx, s.settings)
	return context.WithTimeout(context.WithoutCancel(ctx), probeTimeout+10*time.Second)
}

func (s *Service) executeProbe(ctx context.Context, snapshot probeSnapshot, model string) (TestResult, error) {
	candidates, err := s.resolveUpstreamCandidates(ctx, snapshot.ChannelID, model)
	if err != nil {
		return TestResult{}, err
	}
	return s.executeProbeCandidates(ctx, snapshot, candidates), nil
}

func (s *Service) executeProbeCandidates(ctx context.Context, snapshot probeSnapshot, candidates []string) TestResult {
	probeTimeout := appsettings.AdminBackendChannelTestProbeTimeout(ctx, s.settings)
	runtime := corechannel.Runtime{
		ID: snapshot.ChannelID, BaseURL: snapshot.OriginBaseURL,
		APIKey: strings.TrimSpace(snapshot.Credential), Timeout: probeTimeout, ProviderSlug: snapshot.ProviderSlug,
	}
	var result TestResult
	for i, upstreamModel := range candidates {
		start := time.Now()
		probeCtx, probeCancel := context.WithTimeout(ctx, probeTimeout)
		status, probeErr := s.prober.ProbeChannel(probeCtx, snapshot.Protocol, snapshot.AdapterKey, runtime, upstreamModel)
		latency := time.Since(start)
		probeCancel()

		result = TestResult{
			LatencyMs: latency.Milliseconds(), TestedModel: upstreamModel,
			HTTPStatus: status, TestedAt: time.Now().UTC(),
		}
		if probeErr == nil {
			result.Success = true
			break
		}
		result.ErrorCode, result.Message = classifyProbeError(probeErr, probeTimeout, latency)
		if meta, ok := adapter.UpstreamMetadataOf(probeErr); ok {
			result.UpstreamError = meta.ResponseSnippet
		}
		if result.ErrorCode != ErrCodeModelUnavailable || i == len(candidates)-1 {
			break
		}
	}
	return result
}

func (s *Service) permissionRecheckSnapshot(
	ctx context.Context,
	in PermissionRecheckInput,
) (probeSnapshot, sqlc.ListChannelModelsByChannelRow, bool, error) {
	row, err := s.store.GetChannelProbeSnapshot(ctx, in.ChannelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return probeSnapshot{}, sqlc.ListChannelModelsByChannelRow{}, true, nil
		}
		return probeSnapshot{}, sqlc.ListChannelModelsByChannelRow{}, false, storeFailed(err, "get permission recheck snapshot")
	}
	snapshot := probeSnapshotFromRow(row)
	bindings, err := s.store.ListChannelModelsByChannel(ctx, in.ChannelID)
	if err != nil {
		return probeSnapshot{}, sqlc.ListChannelModelsByChannelRow{}, false, storeFailed(err, "list permission recheck bindings")
	}
	var binding sqlc.ListChannelModelsByChannelRow
	found := false
	for _, candidate := range bindings {
		if candidate.ModelID == in.ModelID {
			binding = candidate
			found = true
			break
		}
	}
	stale := snapshot.ConfigRevision != in.ChannelConfigRevision ||
		snapshot.OriginBaseURLRevision != in.OriginBaseURLRevision ||
		snapshot.OriginStatusRevision != in.OriginStatusRevision ||
		!found || binding.Status != channelModelStatusEnabled
	return snapshot, binding, stale, nil
}

func (s *Service) insertPermissionRecheckAudit(
	ctx context.Context,
	in PermissionRecheckInput,
	result PermissionRecheckResult,
) error {
	probe := result.Probe
	message := probe.Message
	if result.Stale {
		message = stalePermissionMessage(message)
	}
	_, err := s.store.InsertPermissionRecheckLog(ctx, sqlc.InsertPermissionRecheckLogParams{
		ChannelID: in.ChannelID, Success: probe.Success,
		ErrorCode: optText(probe.ErrorCode), HttpStatus: optInt4(int32(probe.HTTPStatus)),
		LatencyMs: optInt4(clampInt32(probe.LatencyMs)), TestedModel: optText(probe.TestedModel),
		Message:                       optText(message),
		TestedOriginBaseUrlRevision: pgtype.Int8{Int64: in.OriginBaseURLRevision, Valid: true},
		TestedOriginStatusRevision:  pgtype.Int8{Int64: in.OriginStatusRevision, Valid: true},
		TestedConfigRevision:          pgtype.Int8{Int64: in.ChannelConfigRevision, Valid: true},
	})
	if err != nil {
		return storeFailed(err, "insert permission recheck audit")
	}
	return nil
}

func stalePermissionProbe(testedModel string) TestResult {
	return TestResult{
		Success: false, TestedModel: testedModel, ErrorCode: "stale_revision",
		Message:  "权限复检对应的渠道、上游源站或模型绑定已变化，旧结果仅留审计",
		TestedAt: time.Now().UTC(),
	}
}

func stalePermissionMessage(message string) string {
	const suffix = "权限复检期间配置已变化，结果仅留审计"
	if message == "" {
		return suffix
	}
	if strings.Contains(message, suffix) {
		return message
	}
	return message + "；" + suffix
}

func (s *Service) applyProbeResult(ctx context.Context, snapshot probeSnapshot, source string, result TestResult) (sqlc.ApplyChannelProbeResultRow, error) {
	if source == "" {
		source = SourceManual
	}
	nextCredentialValid := pgtype.Bool{}
	switch {
	case result.Success:
		nextCredentialValid = pgtype.Bool{Bool: true, Valid: true}
	case result.ErrorCode == ErrCodeCredentialInvalid:
		nextCredentialValid = pgtype.Bool{Bool: false, Valid: true}
	}
	return s.store.ApplyChannelProbeResult(ctx, sqlc.ApplyChannelProbeResultParams{
		ChannelID: snapshot.ChannelID, ExpectedConfigRevision: snapshot.ConfigRevision,
		ExpectedOriginBaseUrlRevision: snapshot.OriginBaseURLRevision,
		ExpectedOriginStatusRevision:  snapshot.OriginStatusRevision,
		Success:                         pgtype.Bool{Bool: result.Success, Valid: true},
		LastTestLatencyMs:               pgtype.Int4{Int32: clampInt32(result.LatencyMs), Valid: true},
		LastTestError:                   testErrorParam(result), NextCredentialValid: nextCredentialValid,
		Source: source, ErrorCode: optText(result.ErrorCode), HttpStatus: optInt4(int32(result.HTTPStatus)),
		TestedModel: optText(result.TestedModel), UpstreamError: optText(result.UpstreamError),
	})
}

func setTestedRevisions(verification *adminchannel.CredentialVerification, snapshot probeSnapshot) {
	verification.TestedOriginBaseURLRevision = int64Ptr(snapshot.OriginBaseURLRevision)
	verification.TestedOriginStatusRevision = int64Ptr(snapshot.OriginStatusRevision)
	verification.TestedConfigRevision = int64Ptr(snapshot.ConfigRevision)
}

func (s *Service) populateExecutionFailed(ctx context.Context, result *adminchannel.RotateCredentialResult, snapshot probeSnapshot, probe *TestResult) {
	result.Verification.State = adminchannel.CredentialVerificationExecutionFailed
	result.Verification.StateChangeApplied = false
	result.Verification.CredentialValidAfter = snapshot.CredentialValid
	if probe != nil {
		result.Verification.Result = credentialProbeResult(*probe)
	}
	if current, err := s.store.GetChannel(ctx, snapshot.ChannelID); err == nil {
		result.CurrentConfigRevision = current.ConfigRevision
		result.Verification.CredentialValidAfter = current.CredentialValid
	}
}

func credentialProbeResult(result TestResult) *adminchannel.CredentialProbeResult {
	return &adminchannel.CredentialProbeResult{
		Success: result.Success, LatencyMs: result.LatencyMs, TestedModel: result.TestedModel,
		HTTPStatus: result.HTTPStatus, ErrorCode: result.ErrorCode, Message: result.Message,
		UpstreamError: result.UpstreamError, TestedAt: result.TestedAt,
	}
}

func int64Ptr(value int64) *int64 {
	return &value
}

// optText 空串→NULL。
func optText(v string) pgtype.Text {
	if v == "" {
		return pgtype.Text{Valid: false}
	}
	return pgtype.Text{String: v, Valid: true}
}

// optInt4 非正数（含探测未拿到状态码的 0）→NULL。
func optInt4(v int32) pgtype.Int4 {
	if v <= 0 {
		return pgtype.Int4{Valid: false}
	}
	return pgtype.Int4{Int32: v, Valid: true}
}

// LogEntry 是一条渠道检测/凭据事件日志（详情页「检测日志」区块展示）。
type LogEntry struct {
	ID                            int64
	CreatedAt                     time.Time
	Source                        string
	Success                       bool
	ErrorCode                     string
	HTTPStatus                    int
	LatencyMs                     int64
	TestedModel                   string
	CredentialValidAfter          bool
	Message                       string
	UpstreamError                 string
	TestedOriginBaseURLRevision *int64
	TestedOriginStatusRevision  *int64
	TestedConfigRevision          *int64
	StateChangeApplied            bool
}

// ListLogs 分页返回某渠道的检测日志（倒序）。返回本页 + 总数。
func (s *Service) ListLogs(ctx context.Context, channelID int64, limit, offset int32) ([]LogEntry, int64, error) {
	if channelID <= 0 {
		return nil, 0, invalidArgument("id", "channel id must be positive")
	}

	rows, err := s.store.ListChannelTestLogsByChannel(ctx, sqlc.ListChannelTestLogsByChannelParams{
		ChannelID:  channelID,
		PageLimit:  limit,
		PageOffset: offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "list channel test logs")
	}

	total, err := s.store.CountChannelTestLogsByChannel(ctx, channelID)
	if err != nil {
		return nil, 0, storeFailed(err, "count channel test logs")
	}

	out := make([]LogEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, LogEntry{
			ID: r.ID, CreatedAt: r.CreatedAt.Time, Source: r.Source, Success: r.Success,
			ErrorCode: r.ErrorCode.String, HTTPStatus: int(r.HttpStatus.Int32), LatencyMs: int64(r.LatencyMs.Int32),
			TestedModel: r.TestedModel.String, CredentialValidAfter: r.CredentialValidAfter,
			Message: r.Message.String, UpstreamError: r.UpstreamError.String,
			TestedOriginBaseURLRevision: nullableInt64(r.TestedOriginBaseUrlRevision),
			TestedOriginStatusRevision:  nullableInt64(r.TestedOriginStatusRevision),
			TestedConfigRevision:          nullableInt64(r.TestedConfigRevision),
			StateChangeApplied:            r.StateChangeApplied,
		})
	}
	return out, total, nil
}

// resolveUpstreamCandidates 决定本次检测按序尝试哪些上游模型。
//
//   - 入参指定 model：映射校验后只返回该模型（不顺延——尊重管理员显式选择）。
//   - 未指定：返回全部启用绑定的上游模型（按绑定顺序、去重），供 Test 在命中「模型不可用」时
//     依次顺延，直到某个模型通得过或全部试完。
func (s *Service) resolveUpstreamCandidates(ctx context.Context, channelID int64, model string) ([]string, error) {
	bindings, err := s.store.ListChannelModelsByChannel(ctx, channelID)
	if err != nil {
		return nil, storeFailed(err, "list channel models")
	}

	if model != "" {
		// 允许前端传 Unio 对外模型 ID（下拉展示值）或直接的上游模型名。
		for _, b := range bindings {
			if b.ModelExternalID == model || b.UpstreamModel == model {
				return []string{b.UpstreamModel}, nil
			}
		}
		return nil, invalidArgument("model", "model is not bound to this channel")
	}

	candidates := make([]string, 0, len(bindings))
	seen := make(map[string]struct{}, len(bindings))
	for _, b := range bindings {
		if b.Status != channelModelStatusEnabled {
			continue
		}
		if _, ok := seen[b.UpstreamModel]; ok {
			continue
		}
		seen[b.UpstreamModel] = struct{}{}
		candidates = append(candidates, b.UpstreamModel)
	}
	if len(candidates) == 0 {
		return nil, invalidArgument("model", "channel has no enabled model binding to test")
	}
	return candidates, nil
}

func nullableInt64(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

// classifyProbeError 把 adapter 返回的上游错误归类成稳定错误码 + 可读中文原因。
// probeTimeout 是本次探测配置的超时上限；waited 是实际等待时长。超时文案优先用 waited，
// 避免「配置上限 60s、实际 10s 被掐断」时仍显示 60s 造成误解。
func classifyProbeError(err error, probeTimeout time.Duration, waited time.Duration) (code string, message string) {
	category, hasCategory := adapter.UpstreamCategoryOf(err)
	meta, _ := adapter.UpstreamMetadataOf(err)
	status := meta.StatusCode

	if !hasCategory {
		// 非 UpstreamError：多为本地请求构造失败等（2xx 协议解析失败现已带 UpstreamError+snippet）。
		return ErrCodeProtocolError, "响应解析失败或协议不符（可能已连通但返回不符合预期）"
	}

	// 2xx + unknown：上游已响应但 body 不符协议（decode/空 choices），仍归 protocol_error。
	if category == adapter.UpstreamErrorUnknown && status >= http.StatusOK && status < http.StatusMultipleChoices {
		return ErrCodeProtocolError, "响应解析失败或协议不符（可能已连通但返回不符合预期）"
	}

	switch category {
	case adapter.UpstreamErrorAuth:
		return ErrCodeCredentialInvalid, "凭据无效或未授权（401）"
	case adapter.UpstreamErrorPermission:
		return ErrCodeCredentialInvalid, "凭据被拒绝或无权限（403）"
	case adapter.UpstreamErrorRateLimit:
		return ErrCodeRateLimited, "上游限流（429）：稍后重试，通常不代表渠道故障"
	case adapter.UpstreamErrorTimeout:
		return ErrCodeTimeout, fmt.Sprintf("检测超时：上游在 %.0fs 内未响应", timeoutSecondsForMessage(probeTimeout, waited))
	case adapter.UpstreamErrorBadRequest:
		if status == http.StatusNotFound {
			return ErrCodeModelUnavailable, "上游未找到该模型或上游源站（404）"
		}
		return ErrCodeModelUnavailable, fmt.Sprintf("上游拒绝请求（%d）：可能模型不可用或参数不被支持", status)
	case adapter.UpstreamErrorCanceled:
		return ErrCodeCanceled, "检测被取消"
	case adapter.UpstreamErrorServer:
		if status == 0 {
			return ErrCodeUnreachable, "连不上上游：连接失败 / DNS / 网络错误"
		}
		return ErrCodeUpstreamError, fmt.Sprintf("上游服务端错误（%d）", status)
	default:
		if status == 0 {
			return ErrCodeUnreachable, "连不上上游：连接失败 / DNS / 网络错误"
		}
		return ErrCodeUpstreamError, fmt.Sprintf("上游调用失败（%d）", status)
	}
}

// timeoutSecondsForMessage 选超时文案里的秒数：实际等待明显短于配置上限时用等待值（反映真实掐断点）。
func timeoutSecondsForMessage(probeTimeout, waited time.Duration) float64 {
	shown := probeTimeout
	if waited > 0 && waited+500*time.Millisecond < probeTimeout {
		shown = waited
	}
	sec := shown.Seconds()
	if sec < 1 {
		return 1
	}
	return sec
}

// testErrorParam 成功或无原因时写 NULL，失败时写可读原因（供渠道表悬浮展示最近失败）。
func testErrorParam(r TestResult) pgtype.Text {
	if r.Success || r.Message == "" {
		return pgtype.Text{Valid: false}
	}
	return pgtype.Text{String: r.Message, Valid: true}
}

// clampInt32 把毫秒延迟安全收敛到 int32（探测超时约束下不会溢出，仅作防御）。
func clampInt32(v int64) int32 {
	switch {
	case v < 0:
		return 0
	case v > int64(^uint32(0)>>1):
		return int32(^uint32(0) >> 1)
	default:
		return int32(v)
	}
}

func invalidArgument(field, message string) error {
	return failure.New(
		failure.CodeAdminInvalidArgument,
		failure.WithMessage(message),
		failure.WithField("field", field),
	)
}

func notFound(message string) error {
	return failure.New(failure.CodeAdminNotFound, failure.WithMessage(message))
}

func storeFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}
