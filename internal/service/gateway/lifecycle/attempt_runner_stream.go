package lifecycle

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/logfields"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
)

// StreamUpstream 执行一次 timed 上游流式调用。
//
// 协议 service 在闭包内用 typed stream adapter 发起 StreamChatCompletions，并把 runner 提供的
// onChunk 原样透传给 adapter；onChunk 负责协议无关的 id/usage/emitted 维护，再分发给协议 EmitChunk。
// 返回 adapter 同次解析的 streamFacts（可能为 nil）与稳定错误，由 runner 分类。adapter span 由协议
// 闭包自行开启/结束（与非流式 Invoke 一致）。
type StreamUpstream func(ctx context.Context, candidate routing.ChatRouteCandidate, onChunk func(chatcompletionsadapter.ChatStreamChunk) error) (*adapter.ResponseFacts, error)

// StreamWriteAck 必须在对应客户 SSE 帧成功写出后调用。
type StreamWriteAck func()

// StreamWriteAcks 把任意协议帧写出和有效生成 Token 写出拆成两个独立事实。
// 一个上游 chunk 可能展开为多个客户帧，协议 encoder 必须只在真正携带生成负载的客户帧
// 成功写出后调用 FirstToken。
type StreamWriteAcks struct {
	Frame      StreamWriteAck
	FirstToken StreamWriteAck
}

// EmitStreamChunk 由协议把单个上游内容 chunk 翻译为协议 SSE 帧（chat chunk / responses 命名事件）。
type EmitStreamChunk func(chunk chatcompletionsadapter.ChatStreamChunk, acks StreamWriteAcks) error

// FinishStream 在流式结算成功后，让协议写出收尾帧。
//
// chat 按 include_usage 决定是否写 usage chunk；responses 总是写 response.completed。是否真正写帧的
// 协议差异由闭包内部决定，runner 在成功路径上总会调用一次。
type FinishStream func(streamID string, finalUsage *adapter.ChatUsage, finishReason string, acks StreamWriteAcks) error

// RunStreamParams 是驱动一次流式候选 fallback 循环所需的协议无关参数（chat chunk 载体）。
//
// 它是 RunStreamParamsGeneric[chatcompletionsadapter.ChatStreamChunk] 的具名别称：OpenAI chat completions 与
// 现有 responses→chat 桥接两个调用点继续用本类型，零改动。responses 直传等其它载体走
// RunStreamGeneric。
type RunStreamParams struct {
	RequestRecord    requestlog.RequestRecord
	Principal        *auth.APIKeyPrincipal
	Authorization    ChatAuthorization
	Candidates       []Candidate
	RequestedModelID string
	ResponseProtocol requestlog.Protocol
	ResolveAdapter   ResolveAdapter
	Stream           StreamUpstream
	EmitChunk        EmitStreamChunk
	Finish           FinishStream

	// ConservativeInputTokens 是预授权阶段的保守输入估算，供 partial settlement 复用为 input 事实。
	ConservativeInputTokens int64

	// CountOutputTokens 按 upstream model 估算一段可见输出文本的 token 数，供 partial settlement 计 output。
	// 为 nil 时 partial 的 output 记 0（偏保守）。
	CountOutputTokens func(model string, text string) int64
	Codes             RunStreamCodes

	// Sticky 是本请求的会话粘性上下文（大 uncache 缺口 P0），语义同 RunNonStreamParams.Sticky。
	Sticky *StickySession
}

// StreamChunkMeta 是从一个上游流式 chunk 提取出的协议无关元信息。
//
// 共享循环据此维护客户可见 stream id、final usage 与终态 finish，并决定该 chunk 是否对客户可见：
//   - ID 非空时更新 stream response id；
//   - FinishReason 非空时更新终态 finish；
//   - Usage 非 nil 时记为 final usage（仅供协议写出收尾帧，账务只认 adapter facts）；
//   - SuppressEmit 为 true 时该 chunk 仅用于内部事实提取（如 chat 的 usage 控制 chunk），
//     不写客户 SSE、也不置 emitted（保持「客户帧写出前可 fallback」语义）。
type StreamChunkMeta struct {
	ID                 string
	FinishReason       string
	Usage              *adapter.ChatUsage
	SuppressEmit       bool
	FirstTokenEligible bool // 独立协议元数据，不得由 !SuppressEmit 推导。
	ProtocolEventType  string
	TokenKind          string
	Classification     string

	// VisibleText 是该 chunk 对客户可见的输出文本增量，仅供流式 partial settlement 估算 output token。
	// 不参与 full bill（账务只认 adapter facts）；usage 控制 chunk / 非文本帧应为空。
	VisibleText string
}

// StreamUpstreamGeneric 执行一次 timed 上游流式调用（泛型载体版）。
type StreamUpstreamGeneric[C any] func(ctx context.Context, candidate routing.ChatRouteCandidate, onChunk func(C) error) (*adapter.ResponseFacts, error)

// EmitStreamChunkGeneric 把单个上游内容 chunk 翻译/透传为协议 SSE 帧（泛型载体版）。
type EmitStreamChunkGeneric[C any] func(chunk C, acks StreamWriteAcks) error

const (
	maxPreludeEvents = 64
	maxPreludeBytes  = 256 * 1024
	maxStreamAudits  = 256
)

var errStreamPreludeBufferExceeded = errors.New("gateway stream prelude buffer exceeded")
var streamAuditSlots = make(chan struct{}, maxStreamAudits)

// launchStreamAudit keeps live-observation writes off the customer stream without allowing a slow
// database to create an unbounded number of goroutines. Terminal timing and first-token facts are
// still persisted synchronously by the attempt closeout/settlement paths.
func launchStreamAudit(write func()) {
	select {
	case streamAuditSlots <- struct{}{}:
		go func() {
			defer func() { <-streamAuditSlots }()
			write()
		}()
	default:
	}
}

type bufferedStreamChunk[C any] struct {
	chunk C
	meta  StreamChunkMeta
	size  int
}

// RunStreamParamsGeneric 是泛型流式候选 fallback 循环参数。
//
// 资金关键流程与 RunStreamParams 完全一致；唯一差异是 chunk 载体类型 C 由调用方决定，并要求提供
// ChunkMeta 提取器把 C 归一为 StreamChunkMeta。RunStream（chat 载体）是 C=chatcompletionsadapter.ChatStreamChunk 的
// 薄封装。
type RunStreamParamsGeneric[C any] struct {
	RequestRecord    requestlog.RequestRecord
	Principal        *auth.APIKeyPrincipal
	Authorization    ChatAuthorization
	Candidates       []Candidate
	RequestedModelID string
	ResponseProtocol requestlog.Protocol
	ResolveAdapter   ResolveAdapter
	Stream           StreamUpstreamGeneric[C]
	EmitChunk        EmitStreamChunkGeneric[C]
	Finish           FinishStream
	// ChunkMeta 从一个上游 chunk 提取协议无关元信息；不得为 nil。
	ChunkMeta func(C) StreamChunkMeta
	// ChunkSize 返回暂存首字前协议事件的近似字节数；用于固定内存上限。
	ChunkSize func(C) int

	// ConservativeInputTokens 是预授权阶段的保守输入估算，供 partial settlement 复用为 input 事实。
	ConservativeInputTokens int64

	// CountOutputTokens 按 upstream model 估算一段可见输出文本的 token 数，供 partial settlement 计 output。
	// 为 nil 时 partial 的 output 记 0（偏保守）。
	CountOutputTokens func(model string, text string) int64
	Codes             RunStreamCodes

	// Sticky 是本请求的会话粘性上下文（大 uncache 缺口 P0），语义同 RunNonStreamParams.Sticky。
	Sticky *StickySession
}

// RunStreamCodes 是共享流式候选循环里的审计 code/reason 覆盖项。
//
// 空值使用 OpenAI chat 既有默认值，保证现有调用点零改动、历史观测语义不漂移。
type RunStreamCodes struct {
	AuthorizationReleaseFailedCode string
	SettlementFailedCode           string

	PartialSettlementBillingExceptionReasonCode string
	PartialSettlementBillingExceptionReason     string
	SettlementBillingExceptionReasonCode        string
	SettlementBillingExceptionReason            string
}

func (c RunStreamCodes) withDefaults() RunStreamCodes {
	if c.AuthorizationReleaseFailedCode == "" {
		c.AuthorizationReleaseFailedCode = "chat_authorization_release_failed"
	}
	if c.SettlementFailedCode == "" {
		c.SettlementFailedCode = "stream_chat_settlement_failed"
	}
	if c.PartialSettlementBillingExceptionReasonCode == "" {
		c.PartialSettlementBillingExceptionReasonCode = "stream_settlement_failed_after_upstream_success"
	}
	if c.PartialSettlementBillingExceptionReason == "" {
		c.PartialSettlementBillingExceptionReason = "stream partial settlement permanently failed without recovery job"
	}
	if c.SettlementBillingExceptionReasonCode == "" {
		c.SettlementBillingExceptionReasonCode = "stream_settlement_failed_after_upstream_success"
	}
	if c.SettlementBillingExceptionReason == "" {
		c.SettlementBillingExceptionReason = "stream settlement permanently failed after upstream success without recovery job"
	}
	return c
}

// RunStream 执行 authorization 之后的流式候选 fallback 循环（chat chunk 载体）。
//
// 它把 chat_stream.go 原有的资金关键流式链路收口到一处，供 OpenAI chat 与 responses 桥接复用；现已
// 委托给泛型 RunStreamGeneric，逻辑逐字不变（chunk 元信息提取由 chatStreamChunkMeta 等价复刻原
// inline onChunk）。
func (r *AttemptRunner) RunStream(ctx context.Context, params RunStreamParams) (RunResult, error) {
	return RunStreamGeneric(ctx, r, RunStreamParamsGeneric[chatcompletionsadapter.ChatStreamChunk]{
		RequestRecord:           params.RequestRecord,
		Principal:               params.Principal,
		Authorization:           params.Authorization,
		Candidates:              params.Candidates,
		RequestedModelID:        params.RequestedModelID,
		ResponseProtocol:        params.ResponseProtocol,
		ResolveAdapter:          params.ResolveAdapter,
		Stream:                  StreamUpstreamGeneric[chatcompletionsadapter.ChatStreamChunk](params.Stream),
		EmitChunk:               EmitStreamChunkGeneric[chatcompletionsadapter.ChatStreamChunk](params.EmitChunk),
		Finish:                  params.Finish,
		ChunkMeta:               chatStreamChunkMeta,
		ChunkSize:               chatStreamChunkSize,
		ConservativeInputTokens: params.ConservativeInputTokens,
		CountOutputTokens:       params.CountOutputTokens,
		Codes:                   params.Codes,
		Sticky:                  params.Sticky,
	})
}

func chatStreamChunkSize(chunk chatcompletionsadapter.ChatStreamChunk) int {
	return 128 + len(chunk.ID) + len(chunk.Model) + len(chunk.Role) + len(chunk.Content) +
		len(chunk.ToolCalls) + len(chunk.FunctionCall) +
		len(chatcompletionsadapter.FirstTokenPayload(chunk))
}

// chatStreamChunkMeta 等价复刻原 chat inline onChunk 的元信息提取：usage 控制 chunk 抑制 emit，
// 普通内容 chunk 透传。
func chatStreamChunkMeta(chunk chatcompletionsadapter.ChatStreamChunk) StreamChunkMeta {
	// 首字判定与可见文本同源：非空生成负载既是「算首字」的依据，也是 partial settlement 的计量文本。
	firstTokenPayload := chatcompletionsadapter.FirstTokenPayload(chunk)
	meta := StreamChunkMeta{
		ID:                 chunk.ID,
		Usage:              chunk.Usage,
		SuppressEmit:       chunk.Usage != nil,
		FirstTokenEligible: firstTokenPayload != "",
		VisibleText:        firstTokenPayload,
		ProtocolEventType:  "chat.completion.chunk",
		TokenKind:          chatStreamTokenKind(chunk),
		Classification:     chatStreamClassification(chunk, firstTokenPayload),
	}
	if chunk.FinishReason != nil {
		meta.FinishReason = *chunk.FinishReason
	}
	return meta
}

func chatStreamTokenKind(chunk chatcompletionsadapter.ChatStreamChunk) string {
	switch {
	case chunk.Content != "":
		return "text"
	case chunk.ReasoningContent != nil && *chunk.ReasoningContent != "":
		return "reasoning"
	case chunk.Refusal != nil && *chunk.Refusal != "":
		return "refusal"
	case len(chunk.ToolCalls) > 0:
		return "tool_call"
	case len(chunk.FunctionCall) > 0:
		return "function_call"
	default:
		return ""
	}
}

func chatStreamClassification(chunk chatcompletionsadapter.ChatStreamChunk, firstTokenPayload string) string {
	switch {
	case firstTokenPayload != "":
		return "effective_token"
	case chunk.Usage != nil:
		return "usage"
	case chunk.FinishReason != nil:
		return "terminal"
	case chunk.Role != "":
		return "role_only"
	default:
		return "empty_generation"
	}
}

// RunStreamGeneric 执行 authorization 之后的流式候选 fallback 循环（泛型载体）。
//
// 资金关键链路（attempt 审计、熔断跳过、adapter 解析、上游流式调用、emitted 后禁止 fallback、
// final usage 缺失处理、客户端取消处理、tail-error 仍尽力结算、settlement 与 request/attempt 终态写入）
// 与原 chat 实现逐字一致；唯一抽象点是 chunk 载体类型 C 与 ChunkMeta 提取器。所有审计 error_code、
// release 原因码与 stream metrics 事件均不变。
//
// 账务只消费 adapter 同次解析返回的 streamFacts；finalUsage 仅供协议向客户写出 usage/completed 帧。
func RunStreamGeneric[C any](ctx context.Context, r *AttemptRunner, params RunStreamParamsGeneric[C]) (RunResult, error) {
	result := RunResult{Outcome: metrics.ChatOutcomeFailed}
	l := r.lifecycle
	requestRecord := params.RequestRecord
	authorization := params.Authorization
	codes := params.Codes.withDefaults()

	var lastErr error
	var denials attemptDenialSummary
	// 全池短等（§9.3/§9.4）：先扫描完整候选池；只有「从未取得 permit 且全部只因并发满」才等待一次并重扫一次。
	// attemptedChannels 从状态机上禁止 A -> B -> A（§3.5）。
	capacityWaitUsed := false
	capacityWaitLogged := false
	var capacityWaitDuration time.Duration
	attemptedChannels := make(map[int64]bool, len(params.Candidates))
	permitAcquired := false

scan:
	for pass := 0; ; pass++ {
		for candIdx, prepared := range params.Candidates {
			index := prepared.RouteIndex
			candidate := prepared.Route
			candidateInputTokens := prepared.InputEstimate
			if candidateInputTokens <= 0 {
				candidateInputTokens = params.ConservativeInputTokens
			}

			// 已真实发起过上游调用的 Channel 不得再次调用（§3.5 禁止 A -> B -> A）。
			if attemptedChannels[candidate.Channel.ID] {
				continue
			}

			// Adapter lookup is local and side-effect free, so do it before acquiring candidate resources.
			if params.ResolveAdapter != nil {
				if err := params.ResolveAdapter(candidate); err != nil {
					if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
						l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
						return result, releaseErr
					}
					l.MarkRequestFailed(ctx, requestRecord, "adapter_not_registered", err)
					return result, err
				}
			}

			var permitOwner *AttemptPermitOwner
			if r.permitManager != nil {
				admission, owner, err := r.permitManager.Acquire(ctx, AttemptPermitAcquireParams{
					Candidate:        candidate,
					UpstreamEndpoint: l.upstreamEndpoint(),
					RequestMode:      breakerstore.ModeStream,
					InputEstimate:    candidateInputTokens,
				})
				if err != nil {
					if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
						l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
						return result, releaseErr
					}
					l.MarkRequestFailed(ctx, requestRecord, RoutingFailureCode(err), err)
					return result, err
				}
				if admission.Mode == breakerstore.AdmissionDenied {
					if admission.Reason == breakerstore.ReasonBreakerStoreUnavailable {
						err := failure.New(failure.CodeGatewayBreakerStoreUnavailable, failure.WithMessage("attempt admission store unavailable"))
						if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
							l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
							return result, releaseErr
						}
						l.MarkRequestFailed(ctx, requestRecord, string(failure.CodeGatewayBreakerStoreUnavailable), err)
						return result, err
					}
					denials.Record(admission)
					skipReason := attemptDeniedSkipReason(admission.Reason)
					result.recordScan(pass, candidate.Channel.ID, false, skipReason)
					r.recordRoutingSkip(skipReason)
					r.logRouting(ctx, "routing candidate skipped",
						zap.Int64("channel_id", candidate.Channel.ID),
						zap.String("skip_reason", skipReason),
					)
					// Sticky：breaker open 永久失格清绑定；并发满 / 429 冷却只临时绕行（§10.6/§10.7）。
					switch admission.Reason {
					case breakerstore.ReasonOpen, breakerstore.ReasonHalfOpenBusy:
						params.Sticky.ClearIfBound(ctx, candidate.Channel.ID, string(admission.Reason))
					case breakerstore.ReasonConcurrencyFull, breakerstore.ReasonCooldown:
						params.Sticky.PreserveOnTemporaryBypass(ctx, candidate.Channel.ID, string(admission.Reason))
					}
					if candIdx+1 < len(params.Candidates) {
						result.RoutingFallback = true
						l.RecordBalanceFallback(routeIDOf(params.Principal), skipReason)
					}
					continue
				}
				permitOwner = owner
				permitAcquired = true
				if capacityWaitUsed && pass > 0 {
					result.CapacityWaitResult = string(capacityWaitAcquired)
					if !capacityWaitLogged {
						l.LogCapacityWaitCompleted(ctx, requestRecord, capacityWaitDuration, capacityWaitAcquired)
						capacityWaitLogged = true
					}
				}
				result.recordScan(pass, candidate.Channel.ID, true, "")
				// Install the terminal fallback before attempt persistence and stream setup can fail or panic.
				defer abortAttemptPermitOnExit(ctx, permitOwner)
			}

			// 每个 stream candidate 也必须先创建 attempt：流式失败可能发生在首 chunk 前、首 chunk 后或
			// 客户端取消时，提前记录 attempt 才能审计这些状态。
			attemptRecord, err := l.CreateAttemptForEndpoint(
				ctx,
				requestRecord,
				result.Attempts,
				index,
				candidate,
				l.upstreamEndpoint(),
				permitOwner.PermitID(),
			)
			if err != nil {
				if permitOwner != nil {
					_ = permitOwner.Abort(ctx)
				}
				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
					return result, releaseErr
				}
				l.MarkRequestFailed(ctx, requestRecord, "request_attempt_create_failed", err)
				return result, err
			}
			result.recordTransportAttempt(candidate, l.upstreamEndpoint())
			// 真实上游调用即将发生：登记 Channel，后续（含短等重扫）不得再次调用它（§3.5）。
			attemptedChannels[candidate.Channel.ID] = true

			// emitted 表示是否已向客户端写出过 SSE 帧。一旦写出开始就不能再 fallback，否则同一个 SSE
			// 响应会混入不同上游内容。
			emitted := false

			// partialOutputTokens 累计「已 emit 可见文本」的估算 output token，仅用于 partial settlement。
			var partialOutputTokens int64

			// finalUsage 仅用于协议向客户写出 usage/completed 帧；结算只消费 streamFacts。
			var finalUsage *adapter.ChatUsage

			// streamFacts 是 adapter 流式解析结束时返回的不可变结算事实。
			var streamFacts *adapter.ResponseFacts

			// streamResponseID 用于客户端可见的 stream id 与收尾帧。
			streamResponseID := ""

			// finishReason 取上游最后一个非空 finish_reason，供协议收尾帧映射终态。
			finishReason := ""

			settledRequestStatus := requestlog.RequestStatusSucceeded
			settledAttemptStatus := requestlog.AttemptStatusSucceeded
			settledErrorCode, settledErrorMessage, settledInternalErrorDetail := "", "", ""

			// gatewayFirstTokenAt 记录首个有效生成 Token 成功写给客户的时间，用于 Gateway TTFT。
			var gatewayFirstTokenAt *time.Time
			firstTokenDelivered := false
			prelude := make([]bufferedStreamChunk[C], 0, maxPreludeEvents)
			preludeBytes := 0
			leadingEventCount := 0
			leadingEventBytes := 0
			streamEventCount := 0
			streamBytes := 0

			tpmScope := l.newTPMAttemptScope(requestRecord, attemptRecord, candidate, permitOwner, candidateInputTokens)
			timingObserver := newAttemptTimingObserverWithHooks(
				true,
				time.Now,
				l.attemptTimingHooks(ctx, requestRecord, attemptRecord, candidate, true, tpmScope),
			)
			attemptCtx := adapter.WithAttemptTimingObserver(ctx, timingObserver)
			firstTokenPersisted := false
			upstreamFirstTokenLogged := false

			// settleStreamFacts 使用 adapter 最终 facts 结算流式请求。结算不能依赖原始请求 ctx：客户端
			// 可能已断开，但只要上游已返回 final usage，平台就有准确账务事实，必须尽力完成结算。
			settleStreamFacts := func() error {
				if streamFacts == nil {
					return failure.New(
						failure.CodeGatewayStreamUsageMissing,
						failure.WithMessage("gateway stream response facts are missing"),
					)
				}

				l.RecordRoutingSelected(candidate.ProviderID, candidate.Channel.ID, params.RequestedModelID)
				l.RecordBalanceSelected(routeIDOf(params.Principal), candidate.Channel.ID)

				settlementCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()

				settleCtx, settleSpan := StartGatewaySpan(settlementCtx, "gateway.settlement")
				responseID := streamResponseID
				if responseID == "" {
					responseID = streamFacts.UpstreamResponseID
				}
				settleErr := r.settlement.SettleSuccessfulChat(settleCtx, ChatSettlementParams{
					RequestRecord:           requestRecord,
					AttemptRecord:           attemptRecord,
					Principal:               params.Principal,
					Authorization:           authorization,
					ResponseProtocol:        params.ResponseProtocol,
					ResponseID:              responseID,
					ResponseModelID:         params.RequestedModelID,
					GatewayFirstTokenAt:     gatewayFirstTokenAt,
					RequestFinalStatus:      settledRequestStatus,
					AttemptFinalStatus:      settledAttemptStatus,
					ErrorCode:               settledErrorCode,
					ErrorMessage:            settledErrorMessage,
					InternalErrorDetail:     settledInternalErrorDetail,
					ModelDBID:               candidate.ModelDBID,
					FinalProviderID:         candidate.ProviderID,
					FinalChannelID:          candidate.Channel.ID,
					ChannelPriceID:          candidate.ChannelPriceID,
					CostBaseModelPriceID:    candidate.CostBaseModelPriceID,
					ChannelCostMultiplierID: candidate.ChannelCostMultiplierID,
					ChannelRechargeFactorID: candidate.ChannelRechargeFactorID,
					SalePrice:               candidate.SalePrice,
					PriceRatio:              candidate.PriceRatio,
					LongContextPolicy:       candidate.LongContextPolicy,
					Facts:                   *streamFacts,
				})
				EndSettlementSpan(settleSpan, settleErr)
				l.RecordSettlement(SettlementOutcomeFromErr(settleErr))
				l.LogSettlementResult(ctx, requestRecord, attemptRecord, candidate, authorization, *streamFacts, gatewayFirstTokenAt != nil, settleErr)
				return settleErr
			}

			var acknowledgeWrite func()

			// finishPartial 处理「已 emit 但无 adapter final usage」的 partial settlement（路线 B/D）：
			// 合成 partial_stream_estimate 事实走与 full bill 相同的结算管道（attempt/request 标 succeeded、
			// final_usage_received=false）；settlement 永久失败且无 recovery 接管时，退回释放冻结并记风险敞口
			// （与上游成功后 settlement 失败同语义）。reason 落到 upstream_finish_reason 区分 B/D。
			finishPartial := func(reason string, outcome metrics.ChatOutcome, streamEvent metrics.StreamEvent, deliveryCompleted bool, returnErr error) (RunResult, error) {
				settledRequestStatus = requestlog.RequestStatusSucceeded
				settledAttemptStatus = requestlog.AttemptStatusSucceeded
				settledErrorCode, settledErrorMessage, settledInternalErrorDetail = "", "", ""
				switch reason {
				case PartialReasonClientCanceled:
					settledRequestStatus = requestlog.RequestStatusCanceled
					settledAttemptStatus = requestlog.AttemptStatusCanceled
					settledErrorCode, settledErrorMessage, settledInternalErrorDetail = l.requestLogCancelFacts(returnErr)
				case PartialReasonInterrupted:
					settledRequestStatus = requestlog.RequestStatusFailed
					settledAttemptStatus = requestlog.AttemptStatusFailed
					settledErrorCode, settledErrorMessage, settledInternalErrorDetail = l.requestLogErrorFacts("stream_adapter_error", returnErr)
				}
				facts := BuildPartialStreamFacts(PartialStreamFactsParams{
					Candidate:        candidate,
					StreamResponseID: streamResponseID,
					RequestRecordID:  requestRecord.ID,
					InputTokens:      params.ConservativeInputTokens,
					OutputTokens:     partialOutputTokens,
					Reason:           reason,
				})
				streamFacts = &facts

				if settleErr := settleStreamFacts(); settleErr != nil {
					if !IsChatSettlementRecoveryScheduled(settleErr) {
						if releaseErr := l.ReleaseAuthorizationForBillingException(
							ctx,
							authorization,
							codes.PartialSettlementBillingExceptionReasonCode,
							codes.PartialSettlementBillingExceptionReason,
						); releaseErr != nil {
							l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
							return result, releaseErr
						}
						l.MarkRequestFailed(ctx, requestRecord, codes.SettlementFailedCode, settleErr)
						return result, settleErr
					}
				}

				// 路线 D 仍必须写出协议终态。usage 是估算事实，不伪装成上游 final usage，
				// 因此向客户收尾时传 nil；Responses/Anthropic 仍会分别写 completed/message_stop。
				if deliveryCompleted {
					if err := params.Finish(streamResponseID, nil, finishReason, StreamWriteAcks{
						Frame: acknowledgeWrite,
					}); err != nil {
						l.MarkDeliveryInterrupted(ctx, requestRecord, firstTokenDelivered, err)
						result.Outcome = metrics.ChatOutcomeFailed
						l.RecordStreamEvent(metrics.StreamEventInterrupted)
						return result, err
					}
					l.MarkDeliveryCompleted(ctx, requestRecord)
				} else {
					l.MarkDeliveryInterrupted(ctx, requestRecord, firstTokenDelivered, returnErr)
				}

				// 仅路线 D（上游完整服务、仅缺 usage）视为成功 attempt 参与 sticky 绑定；
				// 取消/中断不绑（渠道未完整交付，不据此改写粘性事实）。
				if outcome == metrics.ChatOutcomeSuccess {
					params.Sticky.BindSuccess(ctx, candidate)
				}

				result.Outcome = outcome
				l.RecordStreamEvent(streamEvent)
				// partial settlement 按已吐内容保守估算收费，系统性偏少收（P2-2）：记指标供监控占比/滥用。
				l.RecordPartialSettlement(reason)
				return result, returnErr
			}

			acknowledgeWrite = func() {
				if emitted {
					return
				}
				emitted = true
				launchStreamAudit(func() { l.MarkDeliveryStarted(ctx, requestRecord) })
				l.RecordStreamEvent(metrics.StreamEventStarted)
			}

			emitChunk := func(chunk C, meta StreamChunkMeta) (bool, bool, error) {
				// 只分词一次：TPM 观测与 partial settlement 必须共用同一个输出口径，
				// 而且 tokenest 没有缓存，重复调用会把 tokenizer 开销翻倍。
				chunkOutputTokens := int64(0)
				if params.CountOutputTokens != nil && meta.VisibleText != "" {
					chunkOutputTokens = params.CountOutputTokens(candidate.UpstreamModel, meta.VisibleText)
				}
				// Channel 输出在上游 chunk 解析完成时就成立，与客户是否收到无关。
				chunkObservedAt := time.Now()
				l.ObserveChannelOutput(tpmScope, chunkObservedAt, chunkOutputTokens)

				frameAcked := false
				firstTokenAcked := false
				err := params.EmitChunk(chunk, StreamWriteAcks{
					Frame: func() {
						frameAcked = true
						acknowledgeWrite()
					},
					FirstToken: func() {
						if !meta.FirstTokenEligible {
							return
						}
						firstTokenAcked = true
						acknowledgeWrite()
					},
				})
				if firstTokenAcked && chunkOutputTokens > 0 {
					partialOutputTokens += chunkOutputTokens
					// Route 输出以客户写入确认为准：客户端提前断开时 Channel 会比 Route 多记一点。
					l.ObserveRouteOutput(tpmScope, time.Now(), chunkOutputTokens)
				}
				return frameAcked, firstTokenAcked, err
			}

			markGatewayFirstToken := func(tokenKind string) {
				if firstTokenDelivered {
					return
				}
				now := time.Now()
				gatewayFirstTokenAt = &now
				firstTokenDelivered = true
				// The terminal HTTP summary may be written before the asynchronous audit
				// goroutine runs. Publish this in-memory fact synchronously after the
				// customer write acknowledgement; durable persistence remains async.
				logfields.SetGatewayTTFT(ctx, nonNegativeDuration(requestRecord.StartedAt, now).Milliseconds())
				launchStreamAudit(func() { l.MarkGatewayFirstToken(ctx, requestRecord, attemptRecord, now, tokenKind) })
			}

			onChunk := func(chunk C) error {
				meta := params.ChunkMeta(chunk)
				size := 1
				if params.ChunkSize != nil {
					size = params.ChunkSize(chunk)
					if size <= 0 {
						size = 1
					}
				}
				streamEventCount++
				streamBytes += size
				if meta.FirstTokenEligible && !firstTokenPersisted {
					facts := timingObserver.Snapshot()
					if facts.UpstreamFirstTokenAt != nil {
						firstTokenPersisted = true
						if !upstreamFirstTokenLogged {
							upstreamFirstTokenLogged = true
							l.LogUpstreamFirstToken(
								ctx, requestRecord, attemptRecord, candidate, meta, facts,
								leadingEventCount, leadingEventBytes,
							)
						}
						// FirstToken persistence must not delay the customer's first SSE frame.
						// The synchronous write after adapter return still guarantees the
						// complete snapshot is stored before settlement/recovery starts.
						launchStreamAudit(func() { l.RecordAttemptTiming(ctx, attemptRecord, facts) })
					}
				}
				if meta.ID != "" {
					streamResponseID = meta.ID
				}
				if meta.FinishReason != "" {
					finishReason = meta.FinishReason
				}

				if meta.Usage != nil {
					usage := *meta.Usage
					finalUsage = &usage
				}

				if !firstTokenDelivered && !meta.FirstTokenEligible {
					leadingEventCount++
					leadingEventBytes += size
					l.LogLeadingStreamEvent(
						ctx, requestRecord, attemptRecord, candidate, meta, timingObserver.Snapshot(),
						leadingEventCount, size,
					)
					if leadingEventCount > maxPreludeEvents || leadingEventBytes > maxPreludeBytes {
						return failure.Wrap(
							failure.CodeAdapterInvalidResponse,
							errStreamPreludeBufferExceeded,
							failure.WithMessage("upstream stream prelude exceeds gateway buffer limit"),
							failure.WithField("prelude_events", leadingEventCount),
							failure.WithField("prelude_bytes", leadingEventBytes),
						)
					}
					if meta.SuppressEmit {
						// 仅内部事实提取的控制 chunk（如 chat 的 usage 控制 chunk）：不置 emitted、不写 SSE。
						return nil
					}
					prelude = append(prelude, bufferedStreamChunk[C]{chunk: chunk, meta: meta, size: size})
					preludeBytes += size
					return nil
				}

				if meta.SuppressEmit {
					return nil
				}

				if !firstTokenDelivered && meta.FirstTokenEligible {
					for _, buffered := range prelude {
						if _, _, err := emitChunk(buffered.chunk, buffered.meta); err != nil {
							return err
						}
					}
					prelude = nil
					preludeBytes = 0
					_, firstTokenAcked, err := emitChunk(chunk, meta)
					if err != nil {
						return err
					}
					if firstTokenAcked {
						markGatewayFirstToken(meta.TokenKind)
					}
					return nil
				}

				_, _, err := emitChunk(chunk, meta)
				return err
			}

			upstreamStart := time.Now()
			var panicValue any
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						panicValue = recovered
					}
				}()
				streamFacts, err = params.Stream(attemptCtx, candidate, onChunk)
			}()
			adapter.MarkTransportCompleted(attemptCtx)
			timingFacts := timingObserver.Snapshot()
			l.RecordAttemptTiming(ctx, attemptRecord, timingFacts)
			// 流式超时可能卡在响应头、首字或首字之后的 idle（§11.2/§11.4）；非超时失败是 no-op。
			l.RecordAttemptTimeoutPhase(ctx, attemptRecord, timingFacts, true, err)
			outcomeErr := err
			if panicValue != nil {
				outcomeErr = errAttemptInvokePanic
			}
			finishOutcome := streamFinishOutcome(streamFacts, timingFacts, outcomeErr)
			finishOutcome.RequestWriteState = breakerstore.RequestWriteState(timingFacts.RequestWriteState)
			finishOutcome.ResponseHeadersReceived = timingFacts.ResponseHeadersSeen
			finishOutcome.FirstTokenEligible = timingFacts.UpstreamFirstTokenAt != nil

			if permitOwner != nil {
				if !timingFacts.HasChannelUsageEvidence() {
					if abortErr := permitOwner.Abort(ctx); abortErr != nil {
						r.logRouting(ctx, "stream attempt permit abort result unknown",
							zap.Int64("channel_id", candidate.Channel.ID),
							zap.String("mode", "stream"),
							zap.String("error_code", "attempt_permit_abort_result_unknown"),
							zap.String("error_category", "runtime_state"),
							zap.String("error_message", normalizeAttemptStoreError(abortErr).Error()),
						)
					}
				} else {
					finishResult, finishErr := permitOwner.FinishTransport(
						ctx,
						finishOutcome,
						outcomeErr,
					)
					if finishErr != nil {
						if errors.Is(finishErr, ErrAttemptRuntimeFeedback) {
							l.RecordAttemptBreakerDisposition(
								ctx,
								attemptRecord,
								string(finishResult.ProviderDisposition),
								string(finishResult.ChannelDisposition),
							)
							r.logRouting(ctx, "stream attempt runtime feedback failed",
								zap.Int64("channel_id", candidate.Channel.ID),
								zap.Error(finishErr),
							)
							err = finishErr
						} else {
							l.RecordAttemptBreakerDisposition(
								ctx,
								attemptRecord,
								string(breakerstore.DispositionResultUnknown),
								string(breakerstore.DispositionResultUnknown),
							)
							r.logRouting(ctx, "stream attempt permit finish result unknown",
								zap.Int64("channel_id", candidate.Channel.ID),
								zap.String("mode", "stream"),
								zap.String("error_code", "attempt_permit_finish_result_unknown"),
								zap.String("error_category", "runtime_state"),
								zap.String("error_message", normalizeAttemptStoreError(finishErr).Error()),
							)
							if err != nil {
								err = errors.Join(errAttemptPermitFinish, finishErr, err)
							}
						}
					} else {
						l.RecordAttemptBreakerDisposition(
							ctx,
							attemptRecord,
							string(finishResult.ProviderDisposition),
							string(finishResult.ChannelDisposition),
						)
					}
				}
			}

			l.RecordAttemptRuntimeMetrics(candidate, attemptRecord.UpstreamEndpoint, true, timingFacts, finishOutcome, outcomeErr)
			l.RecordAttemptSample(ctx, candidate, attemptRecord, true, timingFacts, finishOutcome, outcomeErr)
			if timingFacts.HasChannelUsageEvidence() {
				if timingFacts.UpstreamStartedAt != nil {
					// 响应头从未到达时，这里是输入观测的唯一记录机会；已记过的 scope 会被忽略。
					l.ObserveAttemptInput(tpmScope, *timingFacts.UpstreamStartedAt)
				}
				completedAt := time.Now()
				if timingFacts.UpstreamCompletedAt != nil {
					completedAt = *timingFacts.UpstreamCompletedAt
				}
				l.FinalizeTPMObservation(tpmScope, completedAt, streamFacts)
			}
			l.RecordUpstream(candidate.ProviderID, candidate.Channel.ID, time.Since(upstreamStart), err)
			l.RecordCredentialResult(candidate, err)
			attemptResultLogged := false
			logAttemptResult := func(resultErr error, fallbackAllowed bool) {
				if attemptResultLogged {
					return
				}
				attemptResultLogged = true
				l.LogUpstreamAttemptResult(
					ctx,
					requestRecord,
					attemptRecord,
					candidate,
					true,
					timingFacts,
					streamFacts,
					AttemptStreamStats{EventCount: streamEventCount, Bytes: streamBytes},
					resultErr,
					fallbackAllowed,
					emitted,
				)
			}
			// Sticky：先按 §10.7/§10.8 处置绑定，再决定是否 fallback。流式的额外约束是
			// 首个客户可见帧之后即使清了绑定也不能再 fallback（§10.10），这由下面的 emitted 分支保证。
			applyStickyAttemptFailure(ctx, params.Sticky, candidate.Channel.ID, err)
			if panicValue != nil {
				logAttemptResult(errAttemptInvokePanic, false)
				panic(panicValue)
			}
			if (errors.Is(err, ErrAttemptRuntimeFeedback) || errors.Is(err, errAttemptPermitFinish)) &&
				streamFacts == nil && !emitted {
				logAttemptResult(err, false)
				l.MarkAttemptFailed(ctx, attemptRecord, FailureCodeOrFallback(err, string(failure.CodeGatewayBreakerStoreUnavailable)), err)
				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
					return result, releaseErr
				}
				l.MarkRequestFailed(ctx, requestRecord, RoutingFailureCode(err), err)
				return result, err
			}

			if err != nil {
				// 有 final usage 时优先结算：上游已给出准确 token 用量，即使尾部出错也不能让已产生成本的请求免费。
				if streamFacts != nil {
					streamEvent := metrics.StreamEventInterrupted
					if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
						settledRequestStatus = requestlog.RequestStatusCanceled
						settledAttemptStatus = requestlog.AttemptStatusCanceled
						settledErrorCode, settledErrorMessage, settledInternalErrorDetail = l.requestLogCancelFacts(err)
						result.Outcome = metrics.ChatOutcomeCanceled
						streamEvent = metrics.StreamEventCanceled
					} else {
						settledRequestStatus = requestlog.RequestStatusFailed
						settledAttemptStatus = requestlog.AttemptStatusFailed
						settledErrorCode, settledErrorMessage, settledInternalErrorDetail = l.requestLogErrorFacts("stream_adapter_error", err)
						result.Outcome = metrics.ChatOutcomeFailed
					}
					logAttemptResult(err, false)
					if settleErr := settleStreamFacts(); settleErr != nil {
						if !IsChatSettlementRecoveryScheduled(settleErr) {
							// settlement 永久失败且无 recovery job 接管：释放冻结余额并记账务异常风险，
							// 否则用户余额永久冻结（同非流式 settlement_failed_after_upstream_success 处理）。
							if releaseErr := l.ReleaseAuthorizationForBillingException(
								ctx,
								authorization,
								codes.SettlementBillingExceptionReasonCode,
								codes.SettlementBillingExceptionReason,
							); releaseErr != nil {
								l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
								return result, releaseErr
							}
							l.MarkRequestFailed(ctx, requestRecord, codes.SettlementFailedCode, settleErr)
							return result, settleErr
						}
					}
					// 账务按真实 usage 收口，但请求/attempt 仍按尾部错误保存 failed/canceled，避免数据库成功、
					// 外层失败的矛盾。调用方继续收到原错误；HTTP 层若已写出 SSE 只能中断连接。
					if emitted {
						l.MarkDeliveryInterrupted(ctx, requestRecord, firstTokenDelivered, err)
						l.RecordStreamEvent(streamEvent)
					} else if streamEvent == metrics.StreamEventCanceled {
						l.RecordStreamEvent(streamEvent)
					}
					return result, err
				}

				// 客户端取消不是上游失败，也不触发 fallback。有效 Token 已交付时按 partial settlement
				// 计费；仅前导帧已交付或首 token 前取消则普通释放冻结、不扣费。
				if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
					logAttemptResult(err, false)
					if firstTokenDelivered {
						// 已 emit → partial settlement 会落真实成本快照，不再记敞口（避免双计）。
						return finishPartial(PartialReasonClientCanceled, metrics.ChatOutcomeCanceled, metrics.StreamEventCanceled, false, err)
					}

					// 有效 Token 交付前取消 + bill-on-disconnect 渠道：上游照常生成并计费，记平台成本敞口（阶段一）。
					l.RecordCostExposure(ctx, requestRecord, attemptRecord, candidate, params.ConservativeInputTokens, err)

					if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
						l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
						return result, releaseErr
					}

					result.Outcome = metrics.ChatOutcomeCanceled
					l.RecordStreamEvent(metrics.StreamEventCanceled)
					l.MarkRequestCanceled(ctx, requestRecord, attemptRecord, err)
					return result, err
				}

				if emitted {
					logAttemptResult(err, false)
					// SSE 已写出后无法再 fallback 或改写 JSON error。
					if firstTokenDelivered {
						// 已 emit 可用输出内容：按 partial settlement 计费（路线 B）；不在此处 MarkAttemptFailed——
						// partial 走 settlement 会先结算 usage/ledger 再把 attempt 标 failed。
						return finishPartial(PartialReasonInterrupted, metrics.ChatOutcomeFailed, metrics.StreamEventInterrupted, false, err)
					}
					// 已 emit 帧但无可用输出内容（仅控制帧/空内容后上游中断）：视同「上游流中断、无可用输出」——
					// 一分钱不扣、全额释放预扣（对齐 new-api PR #4199）。
					l.MarkAttemptFailed(ctx, attemptRecord, "stream_adapter_error", err)
					// 客户侧不扣费，但 bill-on-disconnect 上游已开始生成、大概率照常计费：记平台成本敞口（阶段一）。
					l.RecordCostExposure(ctx, requestRecord, attemptRecord, candidate, params.ConservativeInputTokens, err)
					if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
						l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
						return result, releaseErr
					}
					l.MarkRequestFailed(ctx, requestRecord, "stream_adapter_error", err)
					l.MarkDeliveryInterrupted(ctx, requestRecord, firstTokenDelivered, err)
					result.Outcome = metrics.ChatOutcomeFailed
					l.RecordStreamEvent(metrics.StreamEventInterrupted)
					return result, err
				}

				// 首 token 前失败：attempt 记失败；客户端还没看到上游内容，只有这时允许同模型 fallback。
				l.MarkAttemptFailed(ctx, attemptRecord, "stream_adapter_error", err)

				// bill-on-disconnect 渠道的 timeout/5xx：上游可能已生成并计费，记平台成本敞口（阶段一）。
				// 注意在 fallback 判定之前记录：即使换渠道成功，本渠道的敞口已经产生。
				l.RecordCostExposure(ctx, requestRecord, attemptRecord, candidate, params.ConservativeInputTokens, err)

				retryable := r.retryClassifier.IsRetryable(err)
				logAttemptResult(err, retryable)
				if !retryable {
					if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
						l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
						return result, releaseErr
					}

					l.MarkRequestFailed(ctx, requestRecord, "stream_adapter_error", err)
					return result, err
				}

				// 客户帧写出前的可重试错误切换候选：前一候选可能已在上游产生成本却不会被结算（P2-3），记指标供监控。
				l.RecordRetryableFallback(err)
				if candIdx+1 < len(params.Candidates) {
					result.RoutingFallback = true
					category, _ := adapter.UpstreamCategoryOf(err)
					l.RecordBalanceFallback(routeIDOf(params.Principal), "upstream_"+string(category))
					l.LogRoutingFallback(
						ctx, requestRecord, attemptRecord, candidate,
						FailureCodeOrFallback(err, "upstream_retryable"), true, false, false, "",
					)
				}
				lastErr = err
				continue
			}

			// 账务唯一真源是 adapter facts（B4）：只看 streamFacts 是否缺失，不依赖客户帧用的 finalUsage。
			if streamFacts == nil {
				logAttemptResult(failure.New(failure.CodeGatewayStreamUsageMissing), false)
				// adapter 正常结束但缺 final usage（上游不支持 include_usage、代理吞尾包或 parser 漏解析）。
				// 已 emit 时按 partial settlement 计费并标渠道异常（路线 D）；未 emit 则普通释放、不扣费（路线 C）。
				if firstTokenDelivered {
					return finishPartial(PartialReasonFinalUsageMissing, metrics.ChatOutcomeSuccess, metrics.StreamEventMissingUsage, true, nil)
				}
				if emitted {
					// 仅前导帧已交付但没有有效 Token：不进入 partial settlement。
					l.MarkAttemptFailed(ctx, attemptRecord, "stream_usage_missing", failure.New(failure.CodeGatewayStreamUsageMissing))
					l.RecordCostExposure(ctx, requestRecord, attemptRecord, candidate, params.ConservativeInputTokens, failure.New(failure.CodeGatewayStreamUsageMissing))
					if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
						l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
						return result, releaseErr
					}
					l.MarkRequestFailed(ctx, requestRecord, "stream_usage_missing", failure.New(failure.CodeGatewayStreamUsageMissing))
					l.MarkDeliveryInterrupted(ctx, requestRecord, firstTokenDelivered, failure.New(failure.CodeGatewayStreamUsageMissing))
					return result, failure.New(failure.CodeGatewayStreamUsageMissing)
				}

				err := failure.New(
					failure.CodeGatewayStreamUsageMissing,
					failure.WithMessage("gateway stream final usage is missing"),
				)

				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
					return result, releaseErr
				}

				l.RecordStreamEvent(metrics.StreamEventMissingUsage)
				l.MarkAttemptFailed(ctx, attemptRecord, "stream_usage_missing", err)
				l.MarkRequestFailed(ctx, requestRecord, "stream_usage_missing", err)
				return result, err
			}

			logAttemptResult(nil, false)
			if settleErr := settleStreamFacts(); settleErr != nil {
				if !IsChatSettlementRecoveryScheduled(settleErr) {
					// settlement 永久失败且无 recovery job 接管：释放冻结余额并记账务异常风险，
					// 否则用户余额永久冻结（同非流式 settlement_failed_after_upstream_success 处理）。
					if releaseErr := l.ReleaseAuthorizationForBillingException(
						ctx,
						authorization,
						codes.SettlementBillingExceptionReasonCode,
						codes.SettlementBillingExceptionReason,
					); releaseErr != nil {
						l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
						return result, releaseErr
					}
					l.MarkRequestFailed(ctx, requestRecord, codes.SettlementFailedCode, settleErr)
					return result, settleErr
				}
			}

			// 零输出成功流没有 Gateway 首字，但仍需在 durable settlement 完成后交付协议前导/终态事件。
			if !firstTokenDelivered && len(prelude) > 0 {
				for _, buffered := range prelude {
					if _, _, err := emitChunk(buffered.chunk, buffered.meta); err != nil {
						l.MarkDeliveryInterrupted(ctx, requestRecord, firstTokenDelivered, err)
						return result, err
					}
				}
				prelude = nil
				preludeBytes = 0
			}

			// B4：streamFacts 非空即 full bill；finalUsage 仅用于客户收尾帧，缺失时跳过（不影响计费）。
			if finalUsage != nil {
				if err := params.Finish(streamResponseID, finalUsage, finishReason, StreamWriteAcks{Frame: acknowledgeWrite}); err != nil {
					if emitted {
						l.MarkDeliveryInterrupted(ctx, requestRecord, firstTokenDelivered, err)
					}
					return result, err
				}
			}

			// 流式正常结束（路线 A）：所有 chunk 与收尾帧已写出，交付完成。
			l.MarkDeliveryCompleted(ctx, requestRecord)

			// attempt 成功：sticky bind/改绑（决议 2）。
			params.Sticky.BindSuccess(ctx, candidate)

			// 零价渠道误配监控（P2-4）：售价快照全部非正即客户侧 $0 收入，记指标供运维定位误配渠道。
			if candidate.SalePrice.IsEffectivelyFree() {
				l.RecordZeroPriceServed(candidate.ProviderID, candidate.Channel.ID, params.RequestedModelID)
			}

			result.Outcome = metrics.ChatOutcomeSuccess
			l.RecordStreamEvent(metrics.StreamEventCompleted)
			return result, nil
		}

		// 本轮扫描结束：仅在 §9.3 条件全部成立时进入一次全池短等，随后只重扫一次。
		if capacityWaitUsed && pass > 0 {
			switch {
			case denials.AllCooldown():
				result.CapacityWaitResult = string(capacityWaitRateLimited)
			case denials.AllConcurrencyFull():
				result.CapacityWaitResult = string(capacityWaitCapacityExhausted)
			}
			if !capacityWaitLogged {
				l.LogCapacityWaitCompleted(ctx, requestRecord, capacityWaitDuration, capacityWaitOutcome(result.CapacityWaitResult))
				capacityWaitLogged = true
			}
		}
		if permitAcquired || capacityWaitUsed || pass > 0 || !denials.AllConcurrencyFull() {
			break scan
		}
		l.LogCapacityWaitStarted(ctx, requestRecord, len(params.Candidates), r.capacityWait.Timeout())
		waited, outcome := r.waitForChannelCapacity(ctx)
		capacityWaitUsed = true
		capacityWaitDuration = waited
		result.recordCapacityWait(waited, outcome)
		if outcome == capacityWaitCanceled || outcome == capacityWaitNotWaited {
			l.LogCapacityWaitCompleted(ctx, requestRecord, waited, outcome)
			capacityWaitLogged = true
			break scan
		}
		denials.Reset()
	}

	if lastErr != nil {
		l.LogRoutingFallbackExhausted(ctx, requestRecord, result.Attempts, lastAttemptedChannelID(result), FailureCodeOrFallback(lastErr, "upstream_retryable"), lastErr)
		if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
			l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
			return result, releaseErr
		}
		l.MarkRequestFailed(ctx, requestRecord, "stream_adapter_error", lastErr)
		return result, lastErr
	}

	if denials.seen {
		if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
			l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
			return result, releaseErr
		}
		err := denials.FinalError()
		l.MarkRequestFailed(ctx, requestRecord, RoutingFailureCode(err), err)
		return result, err
	}

	if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
		l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
		return result, releaseErr
	}

	err := failure.Wrap(
		failure.CodeRoutingNoAvailableChannel,
		routing.ErrNoAvailableChannel,
		failure.WithMessage(routing.ErrNoAvailableChannel.Error()),
	)
	l.MarkRequestFailed(ctx, requestRecord, "no_available_channel", err)
	return result, err
}

func streamFinishOutcome(facts *adapter.ResponseFacts, timing AttemptTimingFacts, err error) breakerstore.FinishOutcome {
	out := breakerstore.FinishOutcome{
		ProviderOutcome: breakerstore.OutcomeIgnored,
		ChannelOutcome:  breakerstore.OutcomeIgnored,
	}
	if err == nil && facts != nil {
		out.ProviderOutcome = breakerstore.OutcomeEligibleSuccess
		out.ChannelOutcome = breakerstore.OutcomeEligibleSuccess
		return out
	}
	if err == nil {
		// A transport that completed without protocol/usage facts is a channel protocol failure.
		out.ChannelOutcome = breakerstore.OutcomeEligibleFailure
		return out
	}
	if nonStreamChannelFailureEligible(err) {
		out.ChannelOutcome = breakerstore.OutcomeEligibleFailure
	}
	applyProviderFailureAttribution(&out, timing, true, err)
	return out
}
