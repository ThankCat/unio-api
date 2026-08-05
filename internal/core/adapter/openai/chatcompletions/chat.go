package chatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	adaptersse "github.com/ThankCat/unio-gateway/internal/core/adapter/sse"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const (
	// maxOpenAIStreamEventBytes 是单个上游 OpenAI-compatible SSE event 的读取上限。
	maxOpenAIStreamEventBytes = 4 * 1024 * 1024
)

// Adapter 调用 OpenAI-compatible 上游接口。
//
// 流式响应翻译是 OpenAI 协议族的基线能力，直接内联在本 adapter；provider 专属的 stream 差异
// 由对应 provider adapter（如 adapter/openai/deepseek）在调用 base 前后收口，不再经过独立的
// stream translator 抽象层（AGENTS：streamtranslate 不是独立架构层）。
type Adapter struct {
	client *http.Client
}

// NewAdapter 创建 OpenAI-compatible adapter。
func NewAdapter(client *http.Client) *Adapter {
	if client == nil {
		client = http.DefaultClient
	}

	return &Adapter{
		client: client,
	}
}

// base adapter 是忠实官方基线，直接作为 OpenAI 官方 1P adapter 注册（adapter_key="openai"）。
var (
	_ ChatAdapter        = (*Adapter)(nil)
	_ StreamChatAdapter  = (*Adapter)(nil)
	_ ChatInputTokenizer = (*Adapter)(nil)
)

// ChatCompletions 调用上游 /chat/completions，并转换为统一 adapter 响应。
func (a *Adapter) ChatCompletions(ctx context.Context, ch channel.Runtime, req ChatRequest) (*ChatResponse, error) {
	if ch.Origin == "" {
		return nil, failure.New(
			failure.CodeAdapterChannelInvalid,
			failure.WithMessage("openai adapter channel base url is empty"),
		)
	}

	// 非流式：response_timeout_ms 覆盖连接、响应头、完整响应体与 adapter 解析（§11.1）。
	// 首字预算不参与非流式。
	if ch.ResponseTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ch.ResponseTimeout)
		defer cancel()
	}

	url, err := adapter.BuildUpstreamURL(ch.Origin, adapter.OperationPathChatCompletions)
	if err != nil {
		return nil, err
	}

	buf, err := encodeRequestBody(req, false)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterEncodeRequestFailed,
			err,
			failure.WithMessage("openai adapter encode chat completion request"),
		)
	}

	ctx = adapter.WithAttemptTransportTrace(ctx)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, buf)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterCreateRequestFailed,
			err,
			failure.WithMessage("openai adapter create chat completion request"),
		)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", ch.APIKey))

	adapter.MarkTransportStarted(ctx)
	upstreamResp, err := a.client.Do(request)
	if upstreamResp != nil {
		adapter.MarkResponseHeadersReceived(ctx, adapter.UpstreamMetadata{
			StatusCode: upstreamResp.StatusCode,
			RequestID:  upstreamResp.Header.Get(upstreamRequestIDHeader),
		})
	}
	if err != nil {
		return nil, newUpstreamSendError(err, "send chat completion request")
	}
	defer upstreamResp.Body.Close()

	if upstreamResp.StatusCode < http.StatusOK || upstreamResp.StatusCode >= http.StatusMultipleChoices {
		return nil, newUpstreamStatusError(upstreamResp, "upstream")
	}

	body, exceeded, err := adapter.ReadUpstreamBodyLimited(upstreamResp.Body)
	if err != nil {
		return nil, newUpstreamBodyReadError(
			err, context.Cause(ctx), "openai adapter read chat completion response body",
		)
	}
	if exceeded {
		return nil, failure.New(
			failure.CodeAdapterResponseTooLarge,
			failure.WithMessage("openai adapter chat completion response body exceeds limit"),
			failure.WithField("limit_bytes", adapter.MaxUpstreamResponseBytes()),
		)
	}

	requestID := upstreamResp.Header.Get(upstreamRequestIDHeader)
	var upstreamRespBody chatCompletionResponse
	if err := json.Unmarshal(body, &upstreamRespBody); err != nil {
		return nil, newUpstreamProtocolError(
			upstreamResp.StatusCode,
			requestID,
			body,
			failure.Wrap(
				failure.CodeAdapterDecodeResponseFailed,
				err,
				failure.WithMessage("openai adapter decode chat completion response"),
			),
		)
	}

	if len(upstreamRespBody.Choices) == 0 {
		return nil, newUpstreamProtocolError(
			upstreamResp.StatusCode,
			requestID,
			body,
			failure.New(
				failure.CodeAdapterInvalidResponse,
				failure.WithMessage("openai adapter empty chat completion choices"),
			),
		)
	}

	usage, err := chatUsageFromOpenAI(upstreamRespBody.Usage)
	if err != nil {
		return nil, newUpstreamProtocolError(upstreamResp.StatusCode, requestID, body, err)
	}

	toolCalls, err := wireToolCallsToAdapter(upstreamRespBody.Choices[0].Message.ToolCalls)
	if err != nil {
		return nil, newUpstreamProtocolError(
			upstreamResp.StatusCode,
			requestID,
			body,
			failure.Wrap(
				failure.CodeAdapterDecodeResponseFailed,
				err,
				failure.WithMessage("openai adapter decode chat completion tool_calls"),
			),
		)
	}

	finishReason := upstreamFinishReason(upstreamRespBody.Choices[0])
	meta := adapter.UpstreamMetadata{
		StatusCode: upstreamResp.StatusCode,
		RequestID:  requestID,
	}

	choice := upstreamRespBody.Choices[0]
	return &ChatResponse{
		ID:                upstreamRespBody.ID,
		Model:             upstreamRespBody.Model,
		Content:           wireMessageContentString(choice.Message.Content),
		ReasoningContent:  choice.Message.ReasoningContent,
		ToolCalls:         toolCalls,
		FinishReason:      finishReason,
		Usage:             usage,
		Created:           upstreamRespBody.Created,
		ServiceTier:       upstreamRespBody.ServiceTier,
		SystemFingerprint: upstreamRespBody.SystemFingerprint,
		Refusal:           choice.Message.Refusal,
		Annotations:       cloneRawMessage(choice.Message.Annotations),
		Audio:             cloneRawMessage(choice.Message.Audio),
		Logprobs:          cloneRawMessage(choice.Logprobs),
		Upstream:          meta,
		Facts:             responseFactsNonStream(upstreamRespBody.ID, upstreamRespBody.Model, finishReason, usage, meta),
	}, nil
}

// StreamChatCompletions 调用上游 /chat/completions stream，并转换为统一 adapter chunk。
//
// 上游 [DONE] 只作为内部成功终态被截留，不直接 emit 给客户。调用方必须先持久化
// outcome 中的 immutable facts 并完成 settlement 或 durable recovery 接管，再写出客户 [DONE]。
func (a *Adapter) StreamChatCompletions(ctx context.Context, ch channel.Runtime, req ChatRequest, emit func(ChatStreamChunk) error) (adapter.StreamOutcome, error) {
	if emit == nil {
		return adapter.StreamOutcome{}, failure.New(
			failure.CodeAdapterEmitFailed,
			failure.WithMessage("openai adapter stream emit is nil"),
		)
	}

	if ch.Origin == "" {
		return adapter.StreamOutcome{}, failure.New(
			failure.CodeAdapterChannelInvalid,
			failure.WithMessage("openai adapter channel base url is empty"),
		)
	}

	// 响应头预算只约束「上游开始响应」，不约束流本体：长补全会合法地流式数分钟。
	// 首字预算与它同起点（§11.2），首个有效生成 Token 之后才交给 idle 看门狗。
	streamCtx, timeouts := adapter.StreamTimeoutContext(ctx, adapter.StreamTimeoutConfig{
		ResponseHeader: ch.ResponseTimeout,
		FirstToken:     ch.FirstTokenTimeout,
		Idle:           adapter.StreamIdleTimeout(),
	})
	defer timeouts.Cancel()

	url, err := adapter.BuildUpstreamURL(ch.Origin, adapter.OperationPathChatCompletions)
	if err != nil {
		return adapter.StreamOutcome{}, err
	}

	buf, err := encodeRequestBody(req, true)
	if err != nil {
		return adapter.StreamOutcome{}, failure.Wrap(
			failure.CodeAdapterEncodeRequestFailed,
			err,
			failure.WithMessage("openai adapter encode stream chat completion request"),
		)
	}

	streamCtx = adapter.WithAttemptTransportTrace(streamCtx)
	request, err := http.NewRequestWithContext(streamCtx, http.MethodPost, url, buf)
	if err != nil {
		return adapter.StreamOutcome{}, failure.Wrap(
			failure.CodeAdapterCreateRequestFailed,
			err,
			failure.WithMessage("openai adapter create stream chat completion request"),
		)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", ch.APIKey))

	adapter.MarkTransportStarted(streamCtx)
	upstreamResp, err := a.client.Do(request)
	ctxCause := context.Cause(streamCtx)
	timeouts.HeadersReceived()
	if upstreamResp != nil {
		adapter.MarkResponseHeadersReceived(streamCtx, adapter.UpstreamMetadata{
			StatusCode: upstreamResp.StatusCode,
			RequestID:  upstreamResp.Header.Get(upstreamRequestIDHeader),
		})
	}
	if err != nil {
		return adapter.StreamOutcome{}, newUpstreamSendErrorWithContextCause(err, ctxCause, "send stream chat completion request")
	}
	defer upstreamResp.Body.Close()

	if upstreamResp.StatusCode < http.StatusOK || upstreamResp.StatusCode >= http.StatusMultipleChoices {
		return adapter.StreamOutcome{}, newUpstreamStatusError(upstreamResp, "upstream stream")
	}

	meta := adapter.UpstreamMetadata{
		StatusCode: upstreamResp.StatusCode,
		RequestID:  upstreamResp.Header.Get(upstreamRequestIDHeader),
	}
	var responseID string
	var upstreamModel string
	var rawFinish string
	var finalUsage *adapter.ChatUsage
	terminalReceived := false

	streamReader := adaptersse.NewReader(upstreamResp.Body, adaptersse.Config{
		MaxLineBytes:  maxOpenAIStreamEventBytes,
		MaxEventBytes: maxOpenAIStreamEventBytes,
		OnActivity:    timeouts.ResetIdle,
	})

	for streamReader.Next() {
		payload := bytes.TrimSpace(streamReader.Event().Data)
		if bytes.Equal(payload, []byte("[DONE]")) {
			terminalReceived = true
			break
		}

		var streamResp chatCompletionStreamResponse
		if err := json.Unmarshal(payload, &streamResp); err != nil {
			return streamOutcome(responseID, upstreamModel, rawFinish, finalUsage, meta), failure.Wrap(
				failure.CodeAdapterDecodeResponseFailed,
				err,
				failure.WithMessage("openai adapter decode stream chunk"),
			)
		}

		chunks, err := streamChunksFromResponse(streamResp, meta)
		if err != nil {
			return streamOutcome(responseID, upstreamModel, rawFinish, finalUsage, meta), err
		}

		for _, chunk := range chunks {
			if FirstTokenPayload(chunk) != "" {
				timeouts.FirstToken()
				adapter.MarkFirstTokenEligible(streamCtx)
			}
			if chunk.ID != "" {
				responseID = chunk.ID
			}
			if chunk.Model != "" {
				upstreamModel = chunk.Model
			}
			if chunk.FinishReason != nil {
				rawFinish = *chunk.FinishReason
			}
			if chunk.Usage != nil {
				usage := *chunk.Usage
				finalUsage = &usage
			}

			if err := emit(chunk); err != nil {
				return streamOutcome(responseID, upstreamModel, rawFinish, finalUsage, meta), failure.Wrap(
					failure.CodeAdapterEmitFailed,
					err,
					failure.WithMessage("openai adapter send stream chunk"),
				)
			}
		}
	}

	if err := streamReader.Err(); err != nil {
		return streamOutcome(responseID, upstreamModel, rawFinish, finalUsage, meta),
			newUpstreamStreamReadError(err, context.Cause(streamCtx), "openai adapter read stream event")
	}

	outcome := streamOutcome(responseID, upstreamModel, rawFinish, finalUsage, meta)
	if !terminalReceived {
		return outcome, newUpstreamStreamIncompleteError("openai adapter stream ended before [DONE]")
	}

	return outcome, nil
}

// streamChunksFromResponse 把上游单个 stream SSE event 转成 0..N 个内部 chunk。
//
// 这是 OpenAI 协议族的基线流式翻译：跳过纯空 delta（心跳/占位），其余每个 choice delta 产出一个
// 内容 chunk；若 event 携带 final usage，再追加一个仅含 usage 的内部控制 chunk（附 upstream 元信息）。
func streamChunksFromResponse(streamResp chatCompletionStreamResponse, meta adapter.UpstreamMetadata) ([]ChatStreamChunk, error) {
	chunks := make([]ChatStreamChunk, 0, len(streamResp.Choices)+1)

	for _, choice := range streamResp.Choices {
		if isSkippableStreamDelta(choice) {
			continue
		}

		reasoning := ""
		if choice.Delta.ReasoningContent != nil {
			reasoning = *choice.Delta.ReasoningContent
		}

		chunks = append(chunks, ChatStreamChunk{
			ID:                streamResp.ID,
			Model:             streamResp.Model,
			Role:              choice.Delta.Role,
			Content:           choice.Delta.Content,
			ReasoningContent:  stringPtrOrNil(reasoning),
			ToolCalls:         cloneRawMessage(choice.Delta.ToolCalls),
			FinishReason:      choice.FinishReason,
			Created:           streamResp.Created,
			ServiceTier:       streamResp.ServiceTier,
			SystemFingerprint: streamResp.SystemFingerprint,
			Index:             choice.Index,
			Logprobs:          cloneRawMessage(choice.Logprobs),
			Refusal:           choice.Delta.Refusal,
			FunctionCall:      cloneRawMessage(choice.Delta.FunctionCall),
		})
	}

	if streamResp.Usage != nil {
		usage, err := chatUsageFromOpenAI(streamResp.Usage)
		if err != nil {
			return nil, err
		}

		upstream := meta
		chunks = append(chunks, ChatStreamChunk{
			ID:       streamResp.ID,
			Model:    streamResp.Model,
			Usage:    &usage,
			Upstream: &upstream,
		})
	}

	return chunks, nil
}

// isSkippableStreamDelta 判断 choice delta 是否为纯空增量（无任何可承载内容、元信息或终止原因）。
func isSkippableStreamDelta(choice chatStreamChoice) bool {
	return choice.Delta.Role == "" &&
		choice.Delta.Content == "" &&
		(choice.Delta.ReasoningContent == nil || *choice.Delta.ReasoningContent == "") &&
		len(choice.Delta.ToolCalls) == 0 &&
		(choice.Delta.Refusal == nil || *choice.Delta.Refusal == "") &&
		len(choice.Delta.FunctionCall) == 0 &&
		len(choice.Logprobs) == 0 &&
		choice.FinishReason == nil
}

// chatUsageFromOpenAI 将 OpenAI usage DTO 转成 adapter 内部 usage DTO。
// 非流式成功响应和 stream final usage 都必须提供完整 usage，避免缺字段被当成 0 元请求。
func chatUsageFromOpenAI(usage *chatCompletionUsage) (adapter.ChatUsage, error) {
	if usage == nil {
		return adapter.ChatUsage{}, failure.Wrap(
			failure.CodeAdapterInvalidResponse,
			ErrChatUnreliableUsage,
			failure.WithMessage("openai adapter missing chat completion usage"),
		)
	}

	if usage.PromptTokens == nil || usage.CompletionTokens == nil || usage.TotalTokens == nil {
		return adapter.ChatUsage{}, failure.Wrap(
			failure.CodeAdapterInvalidResponse,
			ErrChatUnreliableUsage,
			failure.WithMessage("openai adapter missing required chat completion usage token fields"),
		)
	}

	if *usage.PromptTokens < 0 || *usage.CompletionTokens < 0 || *usage.TotalTokens < 0 {
		return adapter.ChatUsage{}, failure.Wrap(
			failure.CodeAdapterInvalidResponse,
			ErrChatUnreliableUsage,
			failure.WithMessage("openai adapter invalid chat completion usage token counts"),
		)
	}

	if *usage.TotalTokens != *usage.PromptTokens+*usage.CompletionTokens {
		return adapter.ChatUsage{}, failure.Wrap(
			failure.CodeAdapterInvalidResponse,
			ErrChatUnreliableUsage,
			failure.WithMessage("openai adapter inconsistent chat completion usage token counts"),
		)
	}

	return adapter.ChatUsage{
		PromptTokens:     *usage.PromptTokens,
		CompletionTokens: *usage.CompletionTokens,
		TotalTokens:      *usage.TotalTokens,
		CachedTokens:     usage.PromptTokensDetails.CachedTokens,
		CacheWriteTokens: usage.PromptTokensDetails.CacheWrite(),
		ReasoningTokens:  usage.CompletionTokensDetails.ReasoningTokens,
	}, nil
}
