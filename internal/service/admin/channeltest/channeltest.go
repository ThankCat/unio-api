// Package channeltest 编排 admin 管理端的「渠道检测 / 一键测渠道」（阶段一）。
//
// 检测 = 用渠道自己的 base_url + 凭据，挑一个该渠道绑定的模型，向真实上游发一个最小 "hi" 请求，
// 验证「连得上 + 凭据有效 + 模型可用」；成功记录延迟，失败把上游错误翻译成可读原因（凭据无效 /
// 模型不可用 / 超时 / 连不上 / 限流 …）。它复用与网关完全一致的 adapter/HTTP 链路，故结果=真实行为。
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
)

// defaultProbeTimeout 是检测超时上限：坏渠道不该把检测拖很久。实际超时取 min(渠道 timeout, 本值)。
const defaultProbeTimeout = 15 * time.Second

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
}

// Prober 向渠道真实上游发一次最小请求（复用与网关一致的 adapter/HTTP 链路）。
// 由 gateway lifecycle 的 AdapterRegistry 实现，bootstrap 注入；此处以接口解耦，便于测试替身。
type Prober interface {
	ProbeChannel(ctx context.Context, protocol, adapterKey string, rt channel.Runtime, upstreamModel string) (int, error)
}

// TestInput 是一次渠道检测入参。
type TestInput struct {
	ChannelID int64
	// Model 可选：Unio 对外模型 ID 或直接的上游模型名；留空时自动取渠道第一个启用绑定模型。
	Model string
}

// TestResult 是一次渠道检测结果。它始终代表「检测已成功执行」；渠道是否健康看 Success。
type TestResult struct {
	Success     bool
	LatencyMs   int64
	TestedModel string // 实际使用的上游模型名
	HTTPStatus  int    // 上游 HTTP 状态码（连接失败/超时未拿到响应时为 0）
	ErrorCode   string // 成功为空
	Message     string // 成功为空；失败为可读原因
	TestedAt    time.Time
}

// Service 编排渠道主动检测：选模型 → 构造 Runtime → 发探测请求 → 归类 → 落库。
type Service struct {
	store  Store
	prober Prober
}

// NewService 创建渠道检测服务。
func NewService(store Store, prober Prober) *Service {
	return &Service{store: store, prober: prober}
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

	upstreamModel, err := s.resolveUpstreamModel(ctx, in.ChannelID, strings.TrimSpace(in.Model))
	if err != nil {
		return TestResult{}, err
	}

	rt := channel.Runtime{
		ID:           ch.ID,
		BaseURL:      ch.BaseUrl,
		APIKey:       strings.TrimSpace(ch.Credential),
		Timeout:      probeTimeout(ch.TimeoutMs),
		ProviderSlug: s.providerSlug(ctx, ch.ProviderID),
	}

	start := time.Now()
	status, probeErr := s.prober.ProbeChannel(ctx, ch.Protocol, ch.AdapterKey, rt, upstreamModel)
	latency := time.Since(start)

	result := TestResult{
		LatencyMs:   latency.Milliseconds(),
		TestedModel: upstreamModel,
		HTTPStatus:  status,
		TestedAt:    time.Now().UTC(),
	}
	if probeErr != nil {
		result.ErrorCode, result.Message = classifyProbeError(probeErr)
	} else {
		result.Success = true
	}

	if _, err := s.store.SetChannelTestResult(ctx, sqlc.SetChannelTestResultParams{
		ID:                in.ChannelID,
		LastTestOk:        pgtype.Bool{Bool: result.Success, Valid: true},
		LastTestLatencyMs: pgtype.Int4{Int32: clampInt32(result.LatencyMs), Valid: true},
		LastTestError:     testErrorParam(result),
	}); err != nil {
		return TestResult{}, storeFailed(err, "persist channel test result")
	}

	return result, nil
}

// resolveUpstreamModel 决定本次检测用哪个上游模型：入参指定则映射校验，否则取第一个启用绑定。
func (s *Service) resolveUpstreamModel(ctx context.Context, channelID int64, model string) (string, error) {
	bindings, err := s.store.ListChannelModelsByChannel(ctx, channelID)
	if err != nil {
		return "", storeFailed(err, "list channel models")
	}

	if model != "" {
		// 允许前端传 Unio 对外模型 ID（下拉展示值）或直接的上游模型名。
		for _, b := range bindings {
			if b.ModelExternalID == model || b.UpstreamModel == model {
				return b.UpstreamModel, nil
			}
		}
		return "", invalidArgument("model", "model is not bound to this channel")
	}

	for _, b := range bindings {
		if b.Status == channelModelStatusEnabled {
			return b.UpstreamModel, nil
		}
	}
	return "", invalidArgument("model", "channel has no enabled model binding to test")
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
func classifyProbeError(err error) (code string, message string) {
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
		return ErrCodeTimeout, "检测超时：上游在超时时间内未响应"
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

// probeTimeout 取 min(渠道 timeout, defaultProbeTimeout)；渠道未设或非正数时用默认上限。
func probeTimeout(timeoutMs pgtype.Int4) time.Duration {
	if timeoutMs.Valid && timeoutMs.Int32 > 0 {
		ct := time.Duration(timeoutMs.Int32) * time.Millisecond
		if ct < defaultProbeTimeout {
			return ct
		}
	}
	return defaultProbeTimeout
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
