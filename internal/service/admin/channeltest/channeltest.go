// Package channeltest 编排 admin 管理端的「渠道检测 / 一键测渠道」（阶段一）。
//
// 检测 = 用 Provider origin + 渠道凭据，挑一个该渠道绑定的模型，向真实上游发一个最小 "hi" 请求，
// 验证「连得上 + 凭据有效 + 模型可用」；成功记录延迟，失败把上游错误翻译成可读原因（凭据无效 /
// 模型不可用 / 超时 / 连不上 / 限流 …）。它复用与网关完全一致的 adapter/HTTP 链路，故结果=真实行为。
//
// 探测超时取自运行时配置 admin_backend.channel_test，与用户请求的
// channels.response_timeout_ms / gateway.default_response_timeout_ms 完全正交——检测专用、互不影响。
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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	corechannel "github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/providerledger"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	adminchannel "github.com/ThankCat/unio-gateway/internal/service/admin/channel"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	"github.com/ThankCat/unio-gateway/internal/service/subscription"
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
	ProbeChannel(ctx context.Context, protocol, adapterKey string, rt corechannel.Runtime, upstreamModel string) (adapter.ProbeResult, error)
}

// StreamProber 是 Prober 的可选扩展：流式探测（responses-only adapter，如 codex）时把上游
// 输出文本增量实时透出。生产实现 lifecycle.AdapterRegistry 支持；测试替身不实现时自动回退
// 非流式 ProbeChannel（无增量，行为不变）。
type StreamProber interface {
	ProbeChannelStream(ctx context.Context, protocol, adapterKey string, rt corechannel.Runtime, upstreamModel string, onDelta func(text string)) (adapter.ProbeResult, error)
}

// ProbeEvent 是流式检测（TestStream）透出的一个过程事件，供 adminapi 以 SSE 推给检测弹窗。
type ProbeEvent struct {
	// Type 取 ProbeEventStart / ProbeEventContent。
	Type string
	// Model 是本次真实上游尝试的上游模型名（仅 probe_start）。
	Model string
	// AccountName 是池型渠道本次被测账号名，credential 型为空（仅 probe_start）。
	AccountName string
	// Text 是上游流式响应的文本增量（仅 content）。
	Text string
}

// 流式检测过程事件类型。
const (
	// ProbeEventStart：一次真实上游尝试开始（自动选模型顺延时每个候选各发一次）。
	ProbeEventStart = "probe_start"
	// ProbeEventContent：上游流式响应的文本增量（仅 responses-only 探测产生）。
	ProbeEventContent = "content"
)

// ProbeAccountant 记录真实探测事实，并在 usage 可靠时扣 Provider 内部余额。
type ProbeAccountant interface {
	AccountProbe(ctx context.Context, params providerledger.ProbeParams) error
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
	// AccountID 可选（仅池型渠道）：按号检测——以指定账号身份出站。0 表示自动选号
	// （enabled 中 priority 最小者，与真实调度同向）。credential 型渠道传非零值报 400。
	AccountID int64
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
	// TestedAccountID/TestedAccountName：池型渠道本次检测使用的账号（credential 型恒为零值）。
	TestedAccountID   int64
	TestedAccountName string
	// AccountUsage 是本次上游响应（成功头 / 429 失败头）携带的账号用量水位；无观测为 nil。
	// 检测弹窗必须能展示它：上游对水位满的最小请求可能仍回 200，「检测通过」与「窗口已满」
	// 并存是真实状态，不带水位回去就会显得自相矛盾。
	AccountUsage *adapter.AccountUsageFacts
	// AccountRuntime 是检测处置（冷却/清除/暂停）落地后的账号运行态回读（Redis 实况），
	// 供弹窗直接回答「这个号现在还接不接客户流量」。credential 型或读取失败为 nil。
	AccountRuntime *breakerstore.AccountRuntime
	facts          *adapter.ResponseFacts
	probeErr       error
	accountingErr  error
}

// PermissionRecheckInput 固化 403 发生时的内部绑定身份与三类 revision。
// ModelID 是数据库内部 models.id；Redis/worker 不传递模型字符串、credential、URL 或请求正文。
type PermissionRecheckInput struct {
	ChannelID              int64
	ModelID                int64
	ChannelConfigRevision  int64
	OriginRevision         int64
	ProviderStatusRevision int64
}

// PermissionRecheckResult 是一次只针对指定绑定的真实探测结果。
// Stale 表示探测前或探测后 PostgreSQL 当前绑定已经不再匹配领取时身份，结果只能审计。
type PermissionRecheckResult struct {
	Probe TestResult
	Stale bool
}

// AccountIdentityResolver 为池型渠道检测解析账号出站身份（生产实现 subscription.ProbeIdentityResolver）。
type AccountIdentityResolver interface {
	ResolveProbeIdentity(ctx context.Context, channelID, accountID int64) (subscription.ProbeIdentity, error)
}

// AccountHealthSink 消费池型探测的账号观测（用量快照/LRU/阈值暂停），
// 与请求路径同一实现（subscription/health.Recorder）。探测响应头本来就带 x-codex-* 水位，
// 不采集等于把免费观测扔掉——管理员装配阶段就该能看到用量水位。
type AccountHealthSink interface {
	RecordAccountSuccess(ctx context.Context, accountID int64, usage *adapter.AccountUsageFacts)
	RecordAccountUsageObservation(ctx context.Context, accountID int64, usage *adapter.AccountUsageFacts)
}

// AccountRuntimeFeedback 把检测结果写进账号 Redis 运行态（与请求路径同一原语）。
// 生产实现是 breakerstore.Store。429 必须落冷却，否则管理页一直显示「启用 · 正常」；
// 探测成功以 durationMs<=0 清除残留冷却（SetAccountCooldown 覆盖语义），付费重置后立即回池。
// AccountRuntimeMany 用于处置落地后的运行态回读（冷却/暂停/隔离剩余），随检测结果回给弹窗。
type AccountRuntimeFeedback interface {
	SetAccountCooldown(ctx context.Context, accountID, durationMs int64, window breakerstore.AccountUsageWindow) (int64, error)
	AccountRuntimeMany(ctx context.Context, accountIDs []int64) ([]breakerstore.AccountRuntime, error)
}

// Service 编排渠道主动检测：选模型 → 构造 Runtime → 发探测请求 → 归类 → 落库。
type Service struct {
	store          Store
	prober         Prober
	settings       *appsettings.SettingsStore
	metrics        CredentialRotationMetrics
	accountant     ProbeAccountant
	accounts       AccountIdentityResolver
	accountHealth  AccountHealthSink
	accountRuntime AccountRuntimeFeedback
}

// CredentialRotationMetrics records only the bounded five-state verification result.
type CredentialRotationMetrics interface {
	IncChannelCredentialRotationVerification(state string)
}

// NewService 创建渠道检测服务。settings 可为 nil（单测），此时探测超时回代码默认。
func NewService(store Store, prober Prober, settings *appsettings.SettingsStore, accountants ...ProbeAccountant) *Service {
	service := &Service{
		store:    store,
		prober:   prober,
		settings: settings,
	}
	if len(accountants) > 0 {
		service.accountant = accountants[0]
	}
	return service
}

// SetMetrics attaches optional credential-rotation telemetry.
func (s *Service) SetMetrics(recorder CredentialRotationMetrics) {
	if s != nil {
		s.metrics = recorder
	}
}

// WithAccountResolver 接入池型渠道的账号身份解析（bootstrap 注入）。
func (s *Service) WithAccountResolver(resolver AccountIdentityResolver) *Service {
	s.accounts = resolver
	return s
}

// WithAccountHealth 接入账号观测回写（bootstrap 注入；nil 表示探测不回填用量）。
func (s *Service) WithAccountHealth(sink AccountHealthSink) *Service {
	s.accountHealth = sink
	return s
}

// WithAccountRuntime 接入账号运行态写入（bootstrap 注入；nil 表示检测失败不写冷却）。
func (s *Service) WithAccountRuntime(feedback AccountRuntimeFeedback) *Service {
	s.accountRuntime = feedback
	return s
}

type probeSnapshot struct {
	ChannelID              int64
	ProviderID             int64
	Protocol               string
	AdapterKey             string
	Credential             string
	CredentialValid        bool
	ConfigRevision         int64
	SupplyForm             string
	ChannelProxyURL        string
	ProviderSlug           string
	Origin                 string
	OriginRevision         int64
	ProviderStatusRevision int64

	// Account 是池型渠道本次检测冻结的账号身份（credential 型恒零值）；
	// 池型时 Credential 已换成该账号的 access token。
	Account corechannel.AccountIdentity
	// AccountDisplayName 供检测结果回显（哪个号被测了）。
	AccountDisplayName string
}

// isPool 判断快照是否池型渠道。
func (p probeSnapshot) isPool() bool {
	return corechannel.SupplyForm(p.SupplyForm).IsPool()
}

// Test 对指定渠道执行一次主动检测。读取、探测与结果回写均冻结三类 revision；迟到结果只写历史日志。
func (s *Service) Test(ctx context.Context, in TestInput) (TestResult, error) {
	return s.test(ctx, in, nil)
}

// TestStream 与 Test 同一条编排（同样选模型、探测、归类、落库），另把探测过程事件经 emit
// 实时透出：每次真实上游尝试前发 probe_start（含模型与被测账号），流式探测的响应文本增量发
// content。emit 由 handler 写 SSE；写出失败不中断探测——检测结果必须落库，弹窗关闭不影响。
func (s *Service) TestStream(ctx context.Context, in TestInput, emit func(ProbeEvent)) (TestResult, error) {
	return s.test(ctx, in, emit)
}

func (s *Service) test(ctx context.Context, in TestInput, emit func(ProbeEvent)) (TestResult, error) {
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
	if err := s.attachProbeAccount(ctx, &snapshot, in.AccountID); err != nil {
		return TestResult{}, err
	}
	workCtx, cancel := s.detachedOperationContext(ctx)
	defer cancel()

	result, err := s.executeProbeStream(workCtx, snapshot, strings.TrimSpace(in.Model), in.Source, emit)
	if err != nil {
		return TestResult{}, err
	}
	if result.accountingErr != nil {
		return TestResult{}, result.accountingErr
	}
	// 池型失败带上被测账号名：检测日志（last_test_error / channel_test_logs.message 只存失败 Message）
	// 必须能回答「坏的是哪个号」，否则排障只能靠猜。成功不写 Message，避免污染 last_test_error。
	if snapshot.isPool() && !result.Success && result.Message != "" && snapshot.AccountDisplayName != "" {
		result.Message = "账号「" + snapshot.AccountDisplayName + "」：" + result.Message
	}
	// 探测成功回填用量 + LRU；失败（尤其 429）必须同步写账号运行态——
	// 检测已经拿到真实上游限流，再不落冷却，管理页会一直显示「启用 · 正常」，
	// 下一笔客户请求也会再撞一次同一面墙。账号 status 仍保持 enabled（429 不是吊销）。
	if snapshot.isPool() && result.TestedAccountID > 0 {
		if result.Success {
			if s.accountHealth != nil && result.facts != nil {
				s.accountHealth.RecordAccountSuccess(ctx, result.TestedAccountID, result.facts.AccountUsage)
			}
			// 探测成功 = 该账号此刻真实可服务：清除残留 429 冷却（durationMs<=0 即清除）。
			// 付费即时重置后管理员重测通过，页面与调度必须立即回「正常」，不能等旧冷却自然到期。
			if s.accountRuntime != nil {
				_, _ = s.accountRuntime.SetAccountCooldown(ctx, result.TestedAccountID, 0, "")
			}
		} else {
			s.applyAccountProbeFailure(ctx, result.TestedAccountID, result.probeErr)
		}
		// 处置落地后回读运行态：上游对水位满的最小请求可能仍回 200（限流按用量而非逐请求硬拒），
		// 「检测通过」与「已因水位满暂停调度」并存是真实状态——不带回运行态，弹窗只报「通过」
		// 会显得与列表页自相矛盾。回读失败不影响检测结果本身。
		result.AccountRuntime = s.readAccountRuntime(ctx, result.TestedAccountID)
	}
	if _, err := s.applyProbeResult(workCtx, snapshot, in.Source, result); err != nil {
		return TestResult{}, storeFailed(err, "persist channel probe result")
	}
	return result, nil
}

// readAccountRuntime 回读单个账号的 Redis 运行态（best-effort：失败返回 nil，不阻断检测）。
func (s *Service) readAccountRuntime(ctx context.Context, accountID int64) *breakerstore.AccountRuntime {
	if s.accountRuntime == nil || accountID <= 0 {
		return nil
	}
	runtimes, err := s.accountRuntime.AccountRuntimeMany(ctx, []int64{accountID})
	if err != nil || len(runtimes) != 1 {
		return nil
	}
	runtime := runtimes[0]
	return &runtime
}

// applyAccountProbeFailure 把池型检测失败写进账号运行态（与请求路径 recordAccountRuntimeFeedback 对齐）：
//
//   - 429 → 账号冷却（时长取 adapter 已折算的 RetryAfter：x-codex 重置头或 usage_limit_reached 错误体；
//     解析不出时落渠道 429 策略的秒级兜底，与请求路径同一口径——绝不能因解析失败而完全不冷却）；
//     同时回写失败响应携带的用量观测（水位通常已到顶，管理页冷却期内也能看到真实窗口）。
//   - 其它失败本次不处置账号：401/403 仍走既有隔离路径由请求反馈确认，避免检测一次误伤。
//
// 全部 best-effort：运行态写入失败不阻断检测结果落库。
func (s *Service) applyAccountProbeFailure(ctx context.Context, accountID int64, probeErr error) {
	if accountID <= 0 || probeErr == nil {
		return
	}
	category, ok := adapter.UpstreamCategoryOf(probeErr)
	if !ok || category != adapter.UpstreamErrorRateLimit {
		return
	}
	meta, _ := adapter.UpstreamMetadataOf(probeErr)
	cooldown := meta.RetryAfter
	if cooldown <= 0 {
		cooldown = s.rateLimitCooldownFallback(ctx)
	}
	if durationMs := cooldown.Milliseconds(); durationMs > 0 && s.accountRuntime != nil {
		_, _ = s.accountRuntime.SetAccountCooldown(ctx, accountID, durationMs, "")
	}
	if s.accountHealth != nil && meta.AccountUsage != nil {
		s.accountHealth.RecordAccountUsageObservation(ctx, accountID, meta.AccountUsage)
	}
}

// rateLimitCooldownFallback 取渠道 429 策略的默认冷却时长（settings 未注入时回代码默认）。
// 上游 429 既无重置头也无可解析错误体时用它兜底，保证「检测到限流」必然在运行态留下痕迹。
func (s *Service) rateLimitCooldownFallback(ctx context.Context) time.Duration {
	if s.settings == nil {
		return appsettings.DefaultChannelCooldownSettings().Cooldown
	}
	return appsettings.GatewayChannelCooldown(ctx, s.settings).Cooldown
}

// attachProbeAccount 为池型渠道解析并冻结检测账号身份；credential 型校验不带账号维度。
func (s *Service) attachProbeAccount(ctx context.Context, snapshot *probeSnapshot, accountID int64) error {
	if !snapshot.isPool() {
		if accountID != 0 {
			return invalidArgument("account_id", "credential 型渠道没有账号维度，不能按号检测")
		}
		return nil
	}
	if s.accounts == nil {
		return storeFailed(errors.New("account resolver is unavailable"), "resolve probe account")
	}
	identity, err := s.accounts.ResolveProbeIdentity(ctx, snapshot.ChannelID, accountID)
	if err != nil {
		return err
	}
	snapshot.Credential = identity.AccessToken
	snapshot.Account = corechannel.AccountIdentity{
		ID:                identity.AccountID,
		UpstreamAccountID: identity.UpstreamAccountID,
		ProxyURL:          identity.ProxyURL,
	}
	snapshot.AccountDisplayName = identity.DisplayName
	return nil
}

// RecheckPermission 复用渠道检测 adapter 链路，对指定内部 model_id 的当前绑定发一次真实探测。
// 它只写 source=permission_recheck 审计，不调用 ApplyChannelProbeResult，因此 403/401/其它失败
// 都不会翻整个 Channel 的 credential_valid 或覆盖 last_test_*。调用方随后按 Stale/Success CAS 收口 Redis。
func (s *Service) RecheckPermission(ctx context.Context, in PermissionRecheckInput) (PermissionRecheckResult, error) {
	if in.ChannelID <= 0 || in.ModelID <= 0 || in.ChannelConfigRevision <= 0 ||
		in.OriginRevision <= 0 || in.ProviderStatusRevision <= 0 {
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
	// 池型渠道的 403 复检同样必须以账号身份出站；池空（无 enabled 账号）时复检无法执行，按 stale 审计收口。
	if err := s.attachProbeAccount(ctx, &snapshot, 0); err != nil {
		result := PermissionRecheckResult{Stale: true, Probe: stalePermissionProbe(binding.UpstreamModel)}
		result.Probe.Message = "池内无可用账号，权限复检无法执行：" + err.Error()
		if auditErr := s.insertPermissionRecheckAudit(ctx, in, result); auditErr != nil {
			return PermissionRecheckResult{}, auditErr
		}
		return result, nil
	}

	probeTimeout := appsettings.AdminBackendChannelTestProbeTimeout(ctx, s.settings)
	workCtx, cancel := context.WithTimeout(ctx, probeTimeout+10*time.Second)
	probe := s.executeProbeCandidates(workCtx, snapshot, []probeCandidate{{ModelID: binding.ModelID, UpstreamModel: binding.UpstreamModel}}, SourcePermissionRecheck)
	cancel()
	if probe.accountingErr != nil {
		return PermissionRecheckResult{}, probe.accountingErr
	}
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

	probeResult, err := s.executeProbe(workCtx, snapshot, "", SourceCredentialRotate)
	if err != nil {
		s.populateExecutionFailed(workCtx, &result, snapshot, nil)
		return result, nil
	}
	if probeResult.accountingErr != nil {
		s.populateExecutionFailed(workCtx, &result, snapshot, &probeResult)
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
		ChannelID: row.ChannelID, ProviderID: row.ProviderID, Protocol: primaryProtocol(row.Protocols), AdapterKey: row.AdapterKey,
		Credential: row.Credential, CredentialValid: row.CredentialValid, ConfigRevision: row.ConfigRevision,
		SupplyForm:      row.SupplyForm,
		ChannelProxyURL: channelProxyURL(row.ChannelProxyUrl),
		ProviderSlug:    row.ProviderSlug, Origin: row.Origin,
		OriginRevision: row.OriginRevision, ProviderStatusRevision: row.StatusRevision,
	}
}

// channelProxyURL 取可空代理列（NULL/disabled → 空串直连）。
func channelProxyURL(v pgtype.Text) string {
	if !v.Valid {
		return ""
	}
	return v.String
}

func probeSnapshotFromRotation(row sqlc.PrepareChannelCredentialRotationRow) probeSnapshot {
	return probeSnapshot{
		ChannelID: row.ChannelID, ProviderID: row.ProviderID, Protocol: primaryProtocol(row.Protocols), AdapterKey: row.AdapterKey,
		Credential: row.Credential, CredentialValid: row.CredentialValid, ConfigRevision: row.ConfigRevision,
		ChannelProxyURL: channelProxyURL(row.ChannelProxyUrl),
		ProviderSlug:    row.ProviderSlug, Origin: row.Origin,
		OriginRevision: row.OriginRevision, ProviderStatusRevision: row.StatusRevision,
	}
}

func (s *Service) detachedOperationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	probeTimeout := appsettings.AdminBackendChannelTestProbeTimeout(ctx, s.settings)
	return context.WithTimeout(context.WithoutCancel(ctx), probeTimeout+10*time.Second)
}

func (s *Service) executeProbe(ctx context.Context, snapshot probeSnapshot, model, source string) (TestResult, error) {
	return s.executeProbeStream(ctx, snapshot, model, source, nil)
}

func (s *Service) executeProbeStream(ctx context.Context, snapshot probeSnapshot, model, source string, emit func(ProbeEvent)) (TestResult, error) {
	candidates, err := s.resolveUpstreamCandidates(ctx, snapshot.ChannelID, model)
	if err != nil {
		return TestResult{}, err
	}
	return s.executeProbeCandidatesStream(ctx, snapshot, candidates, source, emit), nil
}

type probeCandidate struct {
	ModelID       int64
	UpstreamModel string
}

func (s *Service) executeProbeCandidates(ctx context.Context, snapshot probeSnapshot, candidates []probeCandidate, source string) TestResult {
	return s.executeProbeCandidatesStream(ctx, snapshot, candidates, source, nil)
}

func (s *Service) executeProbeCandidatesStream(ctx context.Context, snapshot probeSnapshot, candidates []probeCandidate, source string, emit func(ProbeEvent)) TestResult {
	if source == "" {
		source = SourceManual
	}
	probeTimeout := appsettings.AdminBackendChannelTestProbeTimeout(ctx, s.settings)
	runtime := corechannel.Runtime{
		ID: snapshot.ChannelID, Origin: snapshot.Origin,
		APIKey: strings.TrimSpace(snapshot.Credential), ProviderSlug: snapshot.ProviderSlug,
		// 渠道巡检是探测，只需要响应超时；探测超时独立于业务默认值（业务默认 0=不限制，探测不能悬挂）。
		// 只收流式的 wire（Codex）会以流式出站聚合，同一个值同时约束其响应头阶段。
		ResponseTimeout: probeTimeout,
		// 渠道级出站代理；账号自带代理时 adapter 的回退链会优先账号代理。
		ProxyURL: snapshot.ChannelProxyURL,
		// 池型渠道以账号身份出站（access token + 上游账号头 + 账号代理）；credential 型为零值。
		Account: snapshot.Account,
	}
	var result TestResult
	for i, candidate := range candidates {
		upstreamModel := candidate.UpstreamModel
		if emit != nil {
			emit(ProbeEvent{Type: ProbeEventStart, Model: upstreamModel, AccountName: snapshot.AccountDisplayName})
		}
		start := time.Now()
		probeCtx, probeCancel := context.WithTimeout(ctx, probeTimeout)
		probeResult, probeErr := s.probeOnce(probeCtx, snapshot, runtime, upstreamModel, emit)
		latency := time.Since(start)
		probeCancel()

		result = TestResult{
			LatencyMs: latency.Milliseconds(), TestedModel: upstreamModel,
			HTTPStatus: probeResult.StatusCode, TestedAt: time.Now().UTC(), facts: probeResult.Facts,
			TestedAccountID: snapshot.Account.ID, TestedAccountName: snapshot.AccountDisplayName,
		}
		if probeErr == nil {
			result.Success = true
			if probeResult.Facts != nil {
				result.AccountUsage = probeResult.Facts.AccountUsage
			}
		} else {
			result.probeErr = probeErr
			result.ErrorCode, result.Message = classifyProbeError(probeErr, probeTimeout, latency)
			if meta, ok := adapter.UpstreamMetadataOf(probeErr); ok {
				result.UpstreamError = meta.ResponseSnippet
				result.AccountUsage = meta.AccountUsage
			}
		}
		if s.accountant != nil {
			result.accountingErr = s.accountant.AccountProbe(ctx, providerledger.ProbeParams{
				ProviderID: snapshot.ProviderID, ChannelID: snapshot.ChannelID, ModelID: candidate.ModelID,
				Protocol: snapshot.Protocol, Source: source, UpstreamModel: upstreamModel,
				Success: result.Success, HTTPStatus: result.HTTPStatus, ErrorCode: result.ErrorCode, Message: result.Message,
				LatencyMs: result.LatencyMs, StartedAt: start.UTC(), Facts: probeResult.Facts,
				IdempotencyKey: fmt.Sprintf("probe:%s", uuid.NewString()),
			})
			if result.accountingErr != nil {
				return result
			}
		}
		if probeErr == nil || result.ErrorCode != ErrCodeModelUnavailable || i == len(candidates)-1 {
			break
		}
	}
	return result
}

// probeOnce 发起一次真实上游探测：需要透出过程（emit 非 nil）且 prober 支持流式时走
// ProbeChannelStream 把文本增量译成 content 事件，否则走原非流式 ProbeChannel。
func (s *Service) probeOnce(ctx context.Context, snapshot probeSnapshot, runtime corechannel.Runtime, upstreamModel string, emit func(ProbeEvent)) (adapter.ProbeResult, error) {
	if emit != nil {
		if streamer, ok := s.prober.(StreamProber); ok {
			return streamer.ProbeChannelStream(ctx, snapshot.Protocol, snapshot.AdapterKey, runtime, upstreamModel, func(text string) {
				emit(ProbeEvent{Type: ProbeEventContent, Text: text})
			})
		}
	}
	return s.prober.ProbeChannel(ctx, snapshot.Protocol, snapshot.AdapterKey, runtime, upstreamModel)
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
		snapshot.OriginRevision != in.OriginRevision ||
		snapshot.ProviderStatusRevision != in.ProviderStatusRevision ||
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
		Message:              optText(message),
		TestedOriginRevision: pgtype.Int8{Int64: in.OriginRevision, Valid: true},
		TestedStatusRevision: pgtype.Int8{Int64: in.ProviderStatusRevision, Valid: true},
		TestedConfigRevision: pgtype.Int8{Int64: in.ChannelConfigRevision, Valid: true},
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
	// 池型渠道不持渠道级凭据：credential_valid 是路由候选的硬过滤（gateway 候选 SQL
	// `AND c.credential_valid`），而池型检测失败只说明「被测的那个账号」有问题——
	// 账号级健康由请求路径的账号反馈隔离。这里绝不能因一个账号 401 把整条池踢出路由。
	case snapshot.isPool():
		// 保持 NULL：不动 credential_valid（池型建渠道时恒 true）。
		// 被测账号的 429/401 由 applyAccountProbeFailure 写入账号运行态，不在这里翻渠道。
	case result.Success:
		nextCredentialValid = pgtype.Bool{Bool: true, Valid: true}
	case result.ErrorCode == ErrCodeCredentialInvalid:
		nextCredentialValid = pgtype.Bool{Bool: false, Valid: true}
	}
	return s.store.ApplyChannelProbeResult(ctx, sqlc.ApplyChannelProbeResultParams{
		ChannelID: snapshot.ChannelID, ExpectedConfigRevision: snapshot.ConfigRevision,
		ExpectedOriginRevision: snapshot.OriginRevision,
		ExpectedStatusRevision: snapshot.ProviderStatusRevision,
		Success:                pgtype.Bool{Bool: result.Success, Valid: true},
		LastTestLatencyMs:      pgtype.Int4{Int32: clampInt32(result.LatencyMs), Valid: true},
		LastTestError:          testErrorParam(result), NextCredentialValid: nextCredentialValid,
		Source: source, ErrorCode: optText(result.ErrorCode), HttpStatus: optInt4(int32(result.HTTPStatus)),
		TestedModel: optText(result.TestedModel), UpstreamError: optText(result.UpstreamError),
	})
}

func setTestedRevisions(verification *adminchannel.CredentialVerification, snapshot probeSnapshot) {
	verification.TestedOriginRevision = int64Ptr(snapshot.OriginRevision)
	verification.TestedProviderStatusRevision = int64Ptr(snapshot.ProviderStatusRevision)
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
	ID                           int64
	CreatedAt                    time.Time
	Source                       string
	Success                      bool
	ErrorCode                    string
	HTTPStatus                   int
	LatencyMs                    int64
	TestedModel                  string
	CredentialValidAfter         bool
	Message                      string
	UpstreamError                string
	TestedOriginRevision         *int64
	TestedProviderStatusRevision *int64
	TestedConfigRevision         *int64
	StateChangeApplied           bool
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
			TestedOriginRevision:         nullableInt64(r.TestedOriginRevision),
			TestedProviderStatusRevision: nullableInt64(r.TestedStatusRevision),
			TestedConfigRevision:         nullableInt64(r.TestedConfigRevision),
			StateChangeApplied:           r.StateChangeApplied,
		})
	}
	return out, total, nil
}

// resolveUpstreamCandidates 决定本次检测按序尝试哪些上游模型。
//
//   - 入参指定 model：映射校验后只返回该模型（不顺延——尊重管理员显式选择）。
//   - 未指定：返回全部启用绑定的上游模型（按绑定顺序、去重），供 Test 在命中「模型不可用」时
//     依次顺延，直到某个模型通得过或全部试完。
func (s *Service) resolveUpstreamCandidates(ctx context.Context, channelID int64, model string) ([]probeCandidate, error) {
	bindings, err := s.store.ListChannelModelsByChannel(ctx, channelID)
	if err != nil {
		return nil, storeFailed(err, "list channel models")
	}

	if model != "" {
		// 允许前端传 Unio 对外模型 ID（下拉展示值）或直接的上游模型名。
		for _, b := range bindings {
			if b.ModelExternalID == model || b.UpstreamModel == model {
				return []probeCandidate{{ModelID: b.ModelID, UpstreamModel: b.UpstreamModel}}, nil
			}
		}
		return nil, invalidArgument("model", "model is not bound to this channel")
	}

	candidates := make([]probeCandidate, 0, len(bindings))
	seen := make(map[string]struct{}, len(bindings))
	for _, b := range bindings {
		if b.Status != channelModelStatusEnabled {
			continue
		}
		if _, ok := seen[b.UpstreamModel]; ok {
			continue
		}
		seen[b.UpstreamModel] = struct{}{}
		candidates = append(candidates, probeCandidate{ModelID: b.ModelID, UpstreamModel: b.UpstreamModel})
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

// primaryProtocol 取渠道主协议（protocols 首项）。
// 连通性探测只需一个协议形态：同一把凭据的可达性不随入口形态变化。
func primaryProtocol(protocols []string) string {
	if len(protocols) == 0 {
		return ""
	}
	return protocols[0]
}
