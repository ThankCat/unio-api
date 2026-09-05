package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const (
	// maxResponsesStreamEventBytes 是单个上游 Responses SSE event 的读取上限。
	maxResponsesStreamEventBytes = 4 * 1024 * 1024
)

// Adapter 调用原生支持 OpenAI Responses API（POST /responses）的上游接口。
//
// 它是 Responses 直传的官方基线：请求 Body 直传上游，响应/SSE 事件原文透传，只抽取账务事实。
// 直接作为 adapter_key="openai-responses" 注册（OpenAI 官方或 codex 标准中转）。provider 专属方言
// （字段 drop / 错误形状差异）由对应 provider adapter 在调用 base 前后收口，不进入 base；
// Codex 订阅 wire 经 Wire 钩子复用同一实现（见 wire.go 与 openai/codex/responses 包）。
type Adapter struct {
	client *http.Client
	// proxyClientFor 按代理 URL 解析 client（bootstrap 注入 proxyclient.Resolver.ClientFor）；
	// 渠道级出站代理靠它，nil 表示不支持渠道代理（恒用 client）。
	proxyClientFor func(proxyURL string) *http.Client
	wire           Wire
}

// NewAdapter 创建官方 wire 的 Responses 直传 adapter。
func NewAdapter(client *http.Client) *Adapter {
	return NewAdapterWithWire(client, Wire{})
}

// NewAdapterWithWire 创建指定 wire 形态的 Responses 直传 adapter（Codex 订阅后端用）。
// SetProxyClientResolver 注入渠道代理的 client 解析（可选；wire.ClientFor 存在时优先级更高）。
func (a *Adapter) SetProxyClientResolver(clientFor func(proxyURL string) *http.Client) *Adapter {
	a.proxyClientFor = clientFor
	return a
}

func NewAdapterWithWire(client *http.Client, wire Wire) *Adapter {
	if client == nil {
		client = http.DefaultClient
	}
	return &Adapter{client: client, wire: wire}
}

var (
	_ ResponsesAdapter       = (*Adapter)(nil)
	_ StreamResponsesAdapter = (*Adapter)(nil)
)

// CreateResponse 调用上游 POST /responses（非流式），透传响应原文并解析账务事实。
//
// wire.ForceStreaming（Codex：上游对 stream=false 直接 400）时改走流式出站，
// 聚合终态 response 对象还原成非流式响应——客户端拿到的 JSON 与官方非流式同形。
func (a *Adapter) CreateResponse(ctx context.Context, ch channel.Runtime, req Request) (*Response, error) {
	if ch.Origin == "" {
		return nil, failure.New(
			failure.CodeAdapterChannelInvalid,
			failure.WithMessage("openai responses adapter channel base url is empty"),
		)
	}
	if a.wire.ForceStreaming {
		return a.createResponseViaStream(ctx, ch, req)
	}
	var err error
	if req, err = a.normalizeRequest(req, false); err != nil {
		return nil, err
	}

	// 非流式：response_timeout_ms 覆盖连接、响应头、完整响应体与 adapter 解析（§11.1）。
	// 首字预算不参与非流式。
	if ch.ResponseTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ch.ResponseTimeout)
		defer cancel()
	}

	ctx = adapter.WithAttemptTransportTrace(ctx)
	httpReq, err := a.newUpstreamRequest(ctx, ch, req, false)
	if err != nil {
		return nil, err
	}

	adapter.MarkTransportStarted(ctx)
	upstreamResp, err := a.httpClient(ch).Do(httpReq)
	if upstreamResp != nil {
		adapter.MarkResponseHeadersReceived(ctx, adapter.UpstreamMetadata{
			StatusCode: upstreamResp.StatusCode,
			RequestID:  upstreamResp.Header.Get(upstreamRequestIDHeader),
		})
	}
	if err != nil {
		return nil, newUpstreamSendError(err, "send responses request")
	}
	defer upstreamResp.Body.Close()

	if upstreamResp.StatusCode < http.StatusOK || upstreamResp.StatusCode >= http.StatusMultipleChoices {
		return nil, a.upstreamStatusError(upstreamResp, "upstream")
	}

	raw, exceeded, err := adapter.ReadUpstreamBodyLimited(upstreamResp.Body)
	if err != nil {
		return nil, newUpstreamBodyReadError(
			err, context.Cause(ctx), "openai responses adapter read response body",
		)
	}
	if exceeded {
		return nil, failure.New(
			failure.CodeAdapterResponseTooLarge,
			failure.WithMessage("openai responses adapter response body exceeds limit"),
			failure.WithField("limit_bytes", adapter.MaxUpstreamResponseBytes()),
		)
	}

	meta := adapter.UpstreamMetadata{
		StatusCode: upstreamResp.StatusCode,
		RequestID:  upstreamResp.Header.Get(upstreamRequestIDHeader),
	}

	var parsed wireResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterDecodeResponseFailed,
			err,
			failure.WithMessage("openai responses adapter decode response"),
		)
	}

	// 上游用 200 包裹的协议错误（status=failed 或带 error 对象）必须映射成结构化上游错误，
	// 否则会被当成可计费成功响应。
	if parsed.Error != nil {
		return nil, newUpstreamStreamError(meta, parsed.Error.Code, parsed.Error.Message)
	}
	if parsed.Status == "failed" {
		return nil, newUpstreamStreamError(meta, "", "upstream responses status failed")
	}

	chatUsage, ok := chatUsageFromWire(parsed.Usage)
	if !ok {
		return nil, responsesUnreliableUsageError(meta, "openai responses adapter missing or invalid usage in response")
	}

	facts := responsesFacts(parsed, chatUsage, meta, usage.SourceUpstreamResponse)
	a.applyHeaderFacts(upstreamResp.Header, &facts)
	a.finalizeFacts(req, &facts)
	return &Response{
		Raw:        raw,
		ResponseID: parsed.ID,
		Model:      parsed.Model,
		Usage:      chatUsage,
		Upstream:   meta,
		Facts:      facts,
	}, nil
}

// createResponseViaStream 以流式出站满足只收流式的 wire，聚合终态事件还原非流式响应。
//
// 上游 4xx/429 等结构化错误原样上抛（限流/吊销的健康反馈语义不变）；超时采用流式三段预算
// （响应头/首字/空闲），比非流式的单段全程预算宽松，但对「上游只接受流式」的通道这是唯一口径。
func (a *Adapter) createResponseViaStream(ctx context.Context, ch channel.Runtime, req Request) (*Response, error) {
	var (
		terminal    *wireResponse
		terminalRaw json.RawMessage
		outputItems []json.RawMessage
	)
	collect := func(chunk StreamChunk) error {
		switch chunk.EventType {
		case eventOutputItemDone:
			// 逐项终态的 item 原文。Codex 订阅后端的 completed 事件 output 恒为空数组
			//（真机实测 2026-09-03），内容只在过程事件里——收集备用，终态缺 output 时回填。
			var probe struct {
				Item json.RawMessage `json:"item"`
			}
			if err := json.Unmarshal(chunk.Data, &probe); err == nil && len(probe.Item) > 0 {
				outputItems = append(outputItems, probe.Item)
			}
		case eventResponseCompleted, eventResponseIncomplete:
			env := decodeEnvelope(chunk.Data)
			if env == nil || env.Response == nil {
				return nil
			}
			resp := *env.Response
			terminal = &resp
			// 客户端期待的非流式 JSON = 终态事件的 response 子对象原文。
			var probe struct {
				Response json.RawMessage `json:"response"`
			}
			if err := json.Unmarshal(chunk.Data, &probe); err == nil && len(probe.Response) > 0 {
				terminalRaw = probe.Response
			}
		}
		return nil
	}

	outcome, err := a.StreamResponse(ctx, ch, req, collect)
	if err != nil {
		return nil, err
	}
	if terminal == nil || len(terminalRaw) == 0 || outcome.Facts == nil {
		return nil, newUpstreamStreamIncompleteError(
			"openai responses adapter aggregated stream missed terminal response",
		)
	}
	chatUsage, ok := chatUsageFromWire(terminal.Usage)
	if !ok {
		return nil, responsesUnreliableUsageError(
			adapter.UpstreamMetadata{StatusCode: http.StatusOK},
			"openai responses adapter aggregated stream missing usage",
		)
	}
	return &Response{
		Raw:        backfillAggregatedOutput(terminalRaw, outputItems),
		ResponseID: terminal.ID,
		Model:      terminal.Model,
		Usage:      chatUsage,
		Upstream:   adapter.UpstreamMetadata{StatusCode: http.StatusOK},
		Facts:      *outcome.Facts,
	}, nil
}

// backfillAggregatedOutput 在终态 response 对象缺 output 内容时，用流中逐项终态原文回填。
//
// 官方语义的 completed 事件自带全量 output（此时零改动直返，绝不重排既有内容）；
// Codex 订阅后端的 completed 事件 output 恒为空数组，内容只存在于 response.output_item.done
// 的 item 里。回填只替换 output 一个键，其余字段原文保留（item 与 response 均为上游原文）。
func backfillAggregatedOutput(terminalRaw json.RawMessage, items []json.RawMessage) json.RawMessage {
	if len(items) == 0 {
		return terminalRaw
	}
	var probe struct {
		Output []json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(terminalRaw, &probe); err != nil || len(probe.Output) > 0 {
		return terminalRaw
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(terminalRaw, &payload); err != nil {
		return terminalRaw
	}
	outputRaw, err := json.Marshal(items)
	if err != nil {
		return terminalRaw
	}
	payload["output"] = outputRaw
	out, err := json.Marshal(payload)
	if err != nil {
		return terminalRaw
	}
	return out
}

// responsesUnreliableUsageError 构造「Responses 返回 2xx 但没有可靠 usage」的结构化上游错误。
func responsesUnreliableUsageError(meta adapter.UpstreamMetadata, detail string) error {
	return adapter.NewUpstreamError(
		adapter.UpstreamErrorServer,
		meta,
		failure.Wrap(
			failure.CodeAdapterInvalidResponse,
			ErrResponsesUnreliableUsage,
			failure.WithMessage(detail),
		),
	)
}

// newUpstreamRequest 构造打到 <base><responsesPath> 的上游 HTTP 请求。
//
// stream=true 时附 Accept: text/event-stream。请求体直传 req.Body（service 已置 model/stream）；
// wire 需要向上游表达会话身份时（Codex）在此经 bindSession 改写请求体。
func (a *Adapter) newUpstreamRequest(ctx context.Context, ch channel.Runtime, req Request, stream bool) (*http.Request, error) {
	if len(req.Body) == 0 {
		return nil, failure.New(
			failure.CodeAdapterEncodeRequestFailed,
			failure.WithMessage("openai responses adapter request body is empty"),
		)
	}
	if err := a.guardRequest(req); err != nil {
		return nil, err
	}
	req, err := a.bindSession(ctx, ch, req)
	if err != nil {
		return nil, err
	}

	url, err := adapter.BuildUpstreamURL(ch.Origin, a.responsesPath())
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(req.Body))
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterCreateRequestFailed,
			err,
			failure.WithMessage("openai responses adapter create request"),
		)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", ch.APIKey))
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	// 转发客户端 OpenAI-Beta 头(如 responses_multi_agent=v1)：上游据此启用 beta 能力。
	// 上游不支持时由上游报错，非我方 bug；直传路径忠实转发（DEC-013）。
	if beta := strings.TrimSpace(req.BetaHeader); beta != "" {
		httpReq.Header.Set("OpenAI-Beta", beta)
	}
	a.decorateRequest(httpReq, ch)
	return httpReq, nil
}
