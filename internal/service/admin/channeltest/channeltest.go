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

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/appsettings"
)

// channelModelStatusEnabled 是 channel_models 启用状态值（与 DB 约束一致）。
const channelModelStatusEnabled = "enabled"

// 检测失败的稳定错误码（供前端按类型渲染 / 运营归因）。
const (
	ErrCodeCredentialInvalid = "credential_invalid" // 凭据无效 / 无权限（401/403）
	ErrCodeModelUnavailable  = "model_unavailable"  // 模型不可用 / 端点不存在（404/其余 4xx）
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
	GetProvider(ctx context.Context, id int64) (sqlc.Provider, error)
	ListChannelModelsByChannel(ctx context.Context, channelID int64) ([]sqlc.ListChannelModelsByChannelRow, error)
	SetChannelTestResult(ctx context.Context, arg sqlc.SetChannelTestResultParams) (int64, error)
	// 阶段二凭据闸门：检测成功→翻有效、credential_invalid→翻失效（均幂等，返回受影响行数判断是否跳变）。
	SetChannelCredentialValid(ctx context.Context, id int64) (int64, error)
	SetChannelCredentialInvalid(ctx context.Context, id int64) (int64, error)
	InsertChannelTestLog(ctx context.Context, arg sqlc.InsertChannelTestLogParams) error
	ListChannelTestLogsByChannel(ctx context.Context, arg sqlc.ListChannelTestLogsByChannelParams) ([]sqlc.ChannelTestLog, error)
	CountChannelTestLogsByChannel(ctx context.Context, channelID int64) (int64, error)
}

// Prober 向渠道真实上游发一次最小请求（复用与网关一致的 adapter/HTTP 链路）。
// 由 gateway lifecycle 的 AdapterRegistry 实现，bootstrap 注入；此处以接口解耦，便于测试替身。
type Prober interface {
	ProbeChannel(ctx context.Context, protocol, adapterKey string, rt channel.Runtime, upstreamModel string) (int, error)
}

// 检测事件来源（写入 channel_test_logs.source）。
const (
	SourceManual = "manual" // 管理员在控制台手动点「检测」
	SourceWorker = "worker" // 渠道自动检测 worker 周期巡检
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

// Service 编排渠道主动检测：选模型 → 构造 Runtime → 发探测请求 → 归类 → 落库。
type Service struct {
	store    Store
	prober   Prober
	settings *appsettings.SettingsStore
}

// NewService 创建渠道检测服务。settings 可为 nil（单测），此时探测超时回代码默认。
func NewService(store Store, prober Prober, settings *appsettings.SettingsStore) *Service {
	return &Service{
		store:    store,
		prober:   prober,
		settings: settings,
	}
}

// Test 对指定渠道执行一次主动检测，并持久化「最近一次检测结果」。
func (s *Service) Test(ctx context.Context, in TestInput) (TestResult, error) {
	if in.ChannelID <= 0 {
		return TestResult{}, invalidArgument("id", "channel id must be positive")
	}

	ch, err := s.store.GetChannel(ctx, in.ChannelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TestResult{}, notFound("channel not found")
		}
		return TestResult{}, storeFailed(err, "get channel")
	}

	candidates, err := s.resolveUpstreamCandidates(ctx, in.ChannelID, strings.TrimSpace(in.Model))
	if err != nil {
		return TestResult{}, err
	}

	// 检测超时只读系统设置，绝不使用渠道 timeout_ms（那是用户请求超时）。
	pt := appsettings.AdminBackendChannelTestProbeTimeout(ctx, s.settings)
	rt := channel.Runtime{
		ID:           ch.ID,
		BaseURL:      ch.BaseUrl,
		APIKey:       strings.TrimSpace(ch.Credential),
		Timeout:      pt,
		ProviderSlug: s.providerSlug(ctx, ch.ProviderID),
	}

	// 逐个候选探测。自动选模型（未显式指定 model）时，若命中「模型不可用/端点不存在」，
	// 顺延到下一个启用绑定——避免绑定列表里排在前面的坏模型（如已下线的旧模型返回 404）
	// 让整条渠道被误判为异常。其它失败（凭据无效/超时/限流/连不上…）属渠道级问题，换模型
	// 无意义，立即停止并上报。显式指定 model 时候选只有一个，天然不会顺延。
	// 探测用独立超时上下文：不要继承 admin HTTP 请求的 ReadTimeout/WriteTimeout/客户端断开。
	// 否则会出现「文案写系统检测超时，延迟却只有 ~10s（HTTP_READ_TIMEOUT）」的错位——
	// 实际是入口请求 ctx 先被掐断，classify 却仍按 probeTimeout 报错。
	var result TestResult
	for i, upstreamModel := range candidates {
		start := time.Now()
		probeCtx, probeCancel := context.WithTimeout(context.WithoutCancel(ctx), pt)
		status, probeErr := s.prober.ProbeChannel(probeCtx, ch.Protocol, ch.AdapterKey, rt, upstreamModel)
		latency := time.Since(start)
		probeCancel()

		result = TestResult{
			LatencyMs:   latency.Milliseconds(),
			TestedModel: upstreamModel,
			HTTPStatus:  status,
			TestedAt:    time.Now().UTC(),
		}
		if probeErr == nil {
			result.Success = true
			break
		}
		result.ErrorCode, result.Message = classifyProbeError(probeErr, pt, latency)
		// 把上游返回的原始错误体（截断快照）一并记下，供排障时看到完整错误而非只有归类后的中文原因。
		if meta, ok := adapter.UpstreamMetadataOf(probeErr); ok {
			result.UpstreamError = meta.ResponseSnippet
		}
		if result.ErrorCode != ErrCodeModelUnavailable || i == len(candidates)-1 {
			break
		}
	}

	if _, err := s.store.SetChannelTestResult(ctx, sqlc.SetChannelTestResultParams{
		ID:                in.ChannelID,
		LastTestOk:        pgtype.Bool{Bool: result.Success, Valid: true},
		LastTestLatencyMs: pgtype.Int4{Int32: clampInt32(result.LatencyMs), Valid: true},
		LastTestError:     testErrorParam(result),
	}); err != nil {
		return TestResult{}, storeFailed(err, "persist channel test result")
	}

	if err := s.applyCredentialState(ctx, ch, in.Source, result); err != nil {
		return TestResult{}, err
	}

	return result, nil
}

// applyCredentialState 按检测结果翻 credential_valid（C-7）并落一条检测日志。
//
// 翻牌：成功→有效、credential_invalid→失效、其它失败不动。均幂等。
// 写日志口径：每次检测都写一条——手动是管理员显式留痕，worker 是巡检心跳。
// 过去 worker 成功且状态未变时被静默（防刷屏），但这样检测日志里自动巡检「只剩失败行」，
// 会被误读成「自动巡检老是异常」；现改为成功也留痕（每渠道每轮一条，总量由
// LogRetentionPerChannel 控制）。日志写入 best-effort，失败不影响检测结果。
func (s *Service) applyCredentialState(ctx context.Context, ch sqlc.Channel, source string, result TestResult) error {
	if source == "" {
		source = SourceManual
	}

	credentialValidAfter := ch.CredentialValid
	switch {
	case result.Success:
		if _, err := s.store.SetChannelCredentialValid(ctx, ch.ID); err != nil {
			return storeFailed(err, "set channel credential valid")
		}
		credentialValidAfter = true
	case result.ErrorCode == ErrCodeCredentialInvalid:
		if _, err := s.store.SetChannelCredentialInvalid(ctx, ch.ID); err != nil {
			return storeFailed(err, "set channel credential invalid")
		}
		credentialValidAfter = false
	}

	_ = s.store.InsertChannelTestLog(ctx, sqlc.InsertChannelTestLogParams{
		ChannelID:            ch.ID,
		Source:               source,
		Success:              result.Success,
		ErrorCode:            optText(result.ErrorCode),
		HttpStatus:           optInt4(int32(result.HTTPStatus)),
		LatencyMs:            pgtype.Int4{Int32: clampInt32(result.LatencyMs), Valid: true},
		TestedModel:          optText(result.TestedModel),
		CredentialValidAfter: credentialValidAfter,
		Message:              optText(result.Message),
		UpstreamError:        optText(result.UpstreamError),
	})
	return nil
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
	ID                   int64
	CreatedAt            time.Time
	Source               string
	Success              bool
	ErrorCode            string
	HTTPStatus           int
	LatencyMs            int64
	TestedModel          string
	CredentialValidAfter bool
	Message              string
	UpstreamError        string
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
			ID:                   r.ID,
			CreatedAt:            r.CreatedAt.Time,
			Source:               r.Source,
			Success:              r.Success,
			ErrorCode:            r.ErrorCode.String,
			HTTPStatus:           int(r.HttpStatus.Int32),
			LatencyMs:            int64(r.LatencyMs.Int32),
			TestedModel:          r.TestedModel.String,
			CredentialValidAfter: r.CredentialValidAfter,
			Message:              r.Message.String,
			UpstreamError:        r.UpstreamError.String,
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

// providerSlug 取渠道所属 provider 的 slug 供 adapter 选择 provider 专属处理；
// 拿不到不阻断检测（ProviderSlug 只影响流式翻译，非流式探测不依赖）。
func (s *Service) providerSlug(ctx context.Context, providerID int64) string {
	if providerID <= 0 {
		return ""
	}
	p, err := s.store.GetProvider(ctx, providerID)
	if err != nil {
		return ""
	}
	return p.Slug
}

// classifyProbeError 把 adapter 返回的上游错误归类成稳定错误码 + 可读中文原因。
// probeTimeout 是本次探测配置的超时上限；waited 是实际等待时长。超时文案优先用 waited，
// 避免「配置上限 60s、实际 10s 被掐断」时仍显示 60s 造成误解。
func classifyProbeError(err error, probeTimeout time.Duration, waited time.Duration) (code string, message string) {
	category, hasCategory := adapter.UpstreamCategoryOf(err)
	meta, _ := adapter.UpstreamMetadataOf(err)
	status := meta.StatusCode

	if !hasCategory {
		// 非 UpstreamError：多为已连通但响应无法解析 / 协议不符，或本地请求构造失败。
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
			return ErrCodeModelUnavailable, "上游未找到该模型或端点（404）"
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
