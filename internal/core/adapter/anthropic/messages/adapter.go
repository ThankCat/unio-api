package messages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	adaptersse "github.com/ThankCat/unio-gateway/internal/core/adapter/sse"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const (
	// maxAnthropicStreamEventBytes 是单个上游 Anthropic SSE event 的读取上限。
	maxAnthropicStreamEventBytes = 4 * 1024 * 1024

	// anthropicVersionHeaderValue 是 adapter 调用上游时声明的 Anthropic 版本。
	anthropicVersionHeaderValue = "2023-06-01"
)

// Adapter 调用 Anthropic Messages 上游接口（POST <base>/v1/messages）。
//
// 它是 Anthropic 协议族的通用实现：负责 wire 编码、HTTP、响应解析、SSE 翻译与 usage/ResponseFacts。
// provider 专属规则（Reject、tokenizer）由 internal/core/adapter/anthropic/<provider> 组合本类型实现。
type Adapter struct {
	client *http.Client
}

// NewAdapter 创建 Anthropic-compatible adapter。
func NewAdapter(client *http.Client) *Adapter {
	if client == nil {
		client = http.DefaultClient
	}
	return &Adapter{client: client}
}

// Messages 调用上游非流式 /v1/messages，并转换为协议族内部响应与 facts。
func (a *Adapter) Messages(ctx context.Context, ch channel.Runtime, req MessageRequest) (*MessageResponse, error) {
	if ch.BaseURL == "" {
		return nil, failure.New(
			failure.CodeAdapterChannelInvalid,
			failure.WithMessage("anthropic adapter channel base url is empty"),
		)
	}

	if ch.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ch.Timeout)
		defer cancel()
	}

	req.Stream = false
	httpResp, err := a.do(ctx, ch, req, false)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return nil, newUpstreamStatusError(httpResp, "messages")
	}

	body, exceeded, err := adapter.ReadUpstreamBodyLimited(httpResp.Body)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterReadStreamFailed,
			err,
			failure.WithMessage("anthropic adapter read messages response body"),
		)
	}
	if exceeded {
		return nil, failure.New(
			failure.CodeAdapterResponseTooLarge,
			failure.WithMessage("anthropic adapter messages response body exceeds limit"),
			failure.WithField("limit_bytes", adapter.MaxUpstreamResponseBytes()),
		)
	}

	requestID := upstreamRequestID(httpResp.Header)
	var wire messagesResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, newUpstreamProtocolError(
			httpResp.StatusCode,
			requestID,
			body,
			failure.Wrap(
				failure.CodeAdapterDecodeResponseFailed,
				err,
				failure.WithMessage("anthropic adapter decode messages response"),
			),
		)
	}

	if wire.ID == "" || len(wire.Content) == 0 {
		return nil, newUpstreamProtocolError(
			httpResp.StatusCode,
			requestID,
			body,
			failure.New(
				failure.CodeAdapterInvalidResponse,
				failure.WithMessage("anthropic adapter empty messages response"),
			),
		)
	}

	usage := messageUsageFromWire(wire.Usage)
	meta := adapter.UpstreamMetadata{
		StatusCode: httpResp.StatusCode,
		RequestID:  requestID,
	}
	rawReason := derefString(wire.StopReason)

	return &MessageResponse{
		ID:           wire.ID,
		Model:        wire.Model,
		Role:         wire.Role,
		Content:      wire.Content,
		StopReason:   wire.StopReason,
		StopSequence: wire.StopSequence,
		Usage:        usage,
		Upstream:     meta,
		Facts:        ResponseFactsNonStream(wire.ID, wire.Model, rawReason, usage, meta),
	}, nil
}

// StreamMessages 调用上游流式 /v1/messages，解析 Anthropic SSE 并按原生事件回调 emit。
//
// 上游 message_stop 只作为内部成功终态被截留，不直接 emit 给客户。调用方必须先持久化
// outcome 中的 immutable facts 并完成 settlement 或 durable recovery 接管，再写出客户
// message_stop。
func (a *Adapter) StreamMessages(ctx context.Context, ch channel.Runtime, req MessageRequest, emit func(MessageStreamEvent) error) (adapter.StreamOutcome, error) {
	if emit == nil {
		return adapter.StreamOutcome{}, failure.New(
			failure.CodeAdapterEmitFailed,
			failure.WithMessage("anthropic adapter stream emit is nil"),
		)
	}
	if ch.BaseURL == "" {
		return adapter.StreamOutcome{}, failure.New(
			failure.CodeAdapterChannelInvalid,
			failure.WithMessage("anthropic adapter channel base url is empty"),
		)
	}

	// 渠道 timeout 只约束「上游开始响应(拿到响应头)」,不约束流本体:长补全会合法地流式数分钟,
	// 绝不能被渠道 timeout 当绝对截止时间罩住整段读流而掐断。流总时长由客户端断开(父 ctx)兜底。
	streamCtx, headersReceived, resetIdle, cancel := adapter.StreamTimeoutContext(ctx, ch.Timeout, adapter.StreamIdleTimeout())
	defer cancel()

	req.Stream = true
	httpResp, err := a.do(streamCtx, ch, req, true)
	headersReceived()
	if err != nil {
		return adapter.StreamOutcome{}, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return adapter.StreamOutcome{}, newUpstreamStatusError(httpResp, "messages stream")
	}

	meta := adapter.UpstreamMetadata{
		StatusCode: httpResp.StatusCode,
		RequestID:  upstreamRequestID(httpResp.Header),
	}
	var state messageStreamState
	terminalReceived := false

	reader := adaptersse.NewReader(httpResp.Body, adaptersse.Config{
		MaxLineBytes:  maxAnthropicStreamEventBytes,
		MaxEventBytes: maxAnthropicStreamEventBytes,
		OnActivity:    resetIdle,
	})

	for reader.Next() {
		ev := reader.Event()
		eventType := ev.Type
		if eventType == "" {
			eventType = eventTypeFromData(ev.Data)
		}

		finalUsage, err := state.consume(eventType, ev.Data)
		if err != nil {
			return state.outcome(meta), err
		}

		if eventType == "message_stop" {
			terminalReceived = true
			break
		}

		chunk := MessageStreamEvent{
			Type: eventType,
			Data: cloneRaw(ev.Data),
		}

		// message_delta 携带终态 usage 与 stop_reason，作为流式结算事实来源。
		// 对外仍透出原生 message_delta；内部额外挂上合并后的 usage，供 lifecycle 收口。
		if finalUsage {
			usage := state.usageFacts()
			chunk.Usage = &usage
			upstream := meta
			chunk.Upstream = &upstream
		}

		if err := emit(chunk); err != nil {
			return state.outcome(meta), failure.Wrap(
				failure.CodeAdapterEmitFailed,
				err,
				failure.WithMessage("anthropic adapter send stream event"),
			)
		}
	}

	if err := reader.Err(); err != nil {
		return state.outcome(meta),
			newUpstreamStreamReadError(err, context.Cause(streamCtx), "anthropic adapter read stream event")
	}

	outcome := state.outcome(meta)
	if !terminalReceived {
		return outcome, newUpstreamStreamIncompleteError("anthropic adapter stream ended before message_stop")
	}

	return outcome, nil
}

// do 构造并发送上游 HTTP 请求。
func (a *Adapter) do(ctx context.Context, ch channel.Runtime, req MessageRequest, stream bool) (*http.Response, error) {
	url := strings.TrimRight(ch.BaseURL, "/") + "/v1/messages"

	buf, err := encodeMessagesRequestBody(req)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterEncodeRequestFailed,
			err,
			failure.WithMessage("anthropic adapter encode messages request"),
		)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, buf)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterCreateRequestFailed,
			err,
			failure.WithMessage("anthropic adapter create messages request"),
		)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-api-key", ch.APIKey)
	request.Header.Set("anthropic-version", anthropicVersionHeaderValue)
	if len(req.AnthropicBeta) > 0 {
		request.Header.Set("anthropic-beta", strings.Join(req.AnthropicBeta, ", "))
	}
	if stream {
		request.Header.Set("Accept", "text/event-stream")
	}

	httpResp, err := a.client.Do(request)
	if err != nil {
		op := "send messages request"
		if stream {
			op = "send messages stream request"
		}
		return nil, newUpstreamSendErrorWithContextCause(err, context.Cause(ctx), op)
	}

	return httpResp, nil
}

// messageStreamState 累积 Anthropic stream 分散在 message_start 与 message_delta 的事实。
type messageStreamState struct {
	responseID        string
	upstreamModel     string
	rawReason         string
	usage             usageWire
	reliableUsageSeen bool
}

// consume 消费一个 Anthropic stream 控制事件，并报告本事件是否携带最终 usage。
func (s *messageStreamState) consume(eventType string, data []byte) (bool, error) {
	switch eventType {
	case "message_start":
		var env streamUsageEnvelope
		if err := json.Unmarshal(data, &env); err != nil {
			return false, failure.Wrap(
				failure.CodeAdapterDecodeResponseFailed,
				err,
				failure.WithMessage("anthropic adapter decode message_start event"),
			)
		}
		if env.Message == nil {
			return false, failure.New(
				failure.CodeAdapterInvalidResponse,
				failure.WithMessage("anthropic adapter message_start missing message"),
			)
		}
		if env.Message.ID == "" || env.Message.Model == "" {
			return false, failure.New(
				failure.CodeAdapterInvalidResponse,
				failure.WithMessage("anthropic adapter message_start missing message id or model"),
			)
		}
		s.responseID = env.Message.ID
		s.upstreamModel = env.Message.Model
		if env.Message.Usage != nil {
			mergeUsageWire(&s.usage, *env.Message.Usage)
		}

	case "message_delta":
		var env streamUsageEnvelope
		if err := json.Unmarshal(data, &env); err != nil {
			return false, failure.Wrap(
				failure.CodeAdapterDecodeResponseFailed,
				err,
				failure.WithMessage("anthropic adapter decode message_delta event"),
			)
		}
		if env.Delta != nil && env.Delta.StopReason != nil {
			s.rawReason = *env.Delta.StopReason
		}
		if env.Usage != nil {
			mergeUsageWire(&s.usage, *env.Usage)
			if s.usage.InputTokens != nil && s.usage.OutputTokens != nil {
				s.reliableUsageSeen = true
				return true, nil
			}
		}
	}

	return false, nil
}

// usageFacts 返回累积后的 Anthropic wire usage。
func (s *messageStreamState) usageFacts() MessageUsage {
	return messageUsageFromWire(s.usage)
}

// outcome 构造流式调用结束后交给 lifecycle 的最终事实。
func (s *messageStreamState) outcome(meta adapter.UpstreamMetadata) adapter.StreamOutcome {
	if !s.reliableUsageSeen {
		return adapter.StreamOutcome{}
	}

	facts := ResponseFactsStream(s.responseID, s.upstreamModel, s.rawReason, s.usageFacts(), meta)
	return adapter.StreamOutcome{Facts: &facts}
}

// eventTypeFromData 在上游未给出 event: 行时，从 data JSON 的 type 字段回退推断事件名。
func eventTypeFromData(data []byte) string {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &head); err != nil {
		return ""
	}
	return head.Type
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

var (
	_ MessagesAdapter        = (*Adapter)(nil)
	_ StreamMessagesAdapter  = (*Adapter)(nil)
	_ MessagesInputTokenizer = (*Adapter)(nil)
)
