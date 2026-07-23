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
// （字段 drop / 错误形状差异）由对应 provider adapter 在调用 base 前后收口，不进入 base。
type Adapter struct {
	client *http.Client
}

// NewAdapter 创建 Responses 直传 adapter。
func NewAdapter(client *http.Client) *Adapter {
	if client == nil {
		client = http.DefaultClient
	}
	return &Adapter{client: client}
}

var (
	_ ResponsesAdapter       = (*Adapter)(nil)
	_ StreamResponsesAdapter = (*Adapter)(nil)
)

// CreateResponse 调用上游 POST /responses（非流式），透传响应原文并解析账务事实。
func (a *Adapter) CreateResponse(ctx context.Context, ch channel.Runtime, req Request) (*Response, error) {
	if ch.BaseURL == "" {
		return nil, failure.New(
			failure.CodeAdapterChannelInvalid,
			failure.WithMessage("openai responses adapter channel base url is empty"),
		)
	}

	if ch.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ch.Timeout)
		defer cancel()
	}

	httpReq, err := a.newUpstreamRequest(ctx, ch, req, false)
	if err != nil {
		return nil, err
	}

	adapter.MarkTransportStarted(ctx)
	upstreamResp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, newUpstreamSendError(err, "send responses request")
	}
	defer upstreamResp.Body.Close()

	if upstreamResp.StatusCode < http.StatusOK || upstreamResp.StatusCode >= http.StatusMultipleChoices {
		return nil, newUpstreamStatusError(upstreamResp, "upstream")
	}

	raw, exceeded, err := adapter.ReadUpstreamBodyLimited(upstreamResp.Body)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterReadStreamFailed,
			err,
			failure.WithMessage("openai responses adapter read response body"),
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
		return nil, failure.New(
			failure.CodeAdapterInvalidResponse,
			failure.WithMessage("openai responses adapter missing usage in response"),
		)
	}

	facts := responsesFacts(parsed, chatUsage, meta, usage.SourceUpstreamResponse)
	return &Response{
		Raw:        raw,
		ResponseID: parsed.ID,
		Model:      parsed.Model,
		Usage:      chatUsage,
		Upstream:   meta,
		Facts:      facts,
	}, nil
}

// newUpstreamRequest 构造打到 <base>/responses 的上游 HTTP 请求。
//
// stream=true 时附 Accept: text/event-stream。请求体直传 req.Body（service 已置 model/stream）。
func (a *Adapter) newUpstreamRequest(ctx context.Context, ch channel.Runtime, req Request, stream bool) (*http.Request, error) {
	if len(req.Body) == 0 {
		return nil, failure.New(
			failure.CodeAdapterEncodeRequestFailed,
			failure.WithMessage("openai responses adapter request body is empty"),
		)
	}

	url, err := adapter.BuildUpstreamURL(ch.BaseURL, adapter.OperationPathResponses)
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
	return httpReq, nil
}
