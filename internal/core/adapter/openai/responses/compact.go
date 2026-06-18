package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/core/usage"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// ErrCompactUnsupported 表示上游不提供可用的原生 /responses/compact（404/405），或压缩响应缺少可
// 计费 usage / 无法解析：service 据此回落 SyntheticCompact（chat 摘要按真实 token 计费），避免 Codex 断链。
var ErrCompactUnsupported = errors.New("openai responses adapter native compact unsupported")

var _ ResponsesCompactAdapter = (*Adapter)(nil)

// CompactResponse 调用上游 POST /responses/compact（非流式），透传压缩结果原文并解析账务事实。
//
// 与 CreateResponse 同口径透传：请求体直传上游、响应体原文返回；区别在于把「上游无原生 compact」
// （404/405）与「响应不含可计费 usage / 无法解析」收敛成 ErrCompactUnsupported，供 service 回落 Synthetic。
func (a *Adapter) CompactResponse(ctx context.Context, ch channel.Runtime, req Request) (*Response, error) {
	if ch.BaseURL == "" {
		return nil, failure.New(
			failure.CodeAdapterChannelInvalid,
			failure.WithMessage("openai responses adapter channel base url is empty"),
		)
	}
	if len(req.Body) == 0 {
		return nil, failure.New(
			failure.CodeAdapterEncodeRequestFailed,
			failure.WithMessage("openai responses adapter compact request body is empty"),
		)
	}

	if ch.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ch.Timeout)
		defer cancel()
	}

	url := strings.TrimRight(ch.BaseURL, "/") + "/responses/compact"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(req.Body))
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterCreateRequestFailed,
			err,
			failure.WithMessage("openai responses adapter create compact request"),
		)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", ch.APIKey))

	upstreamResp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, newUpstreamSendError(err, "send responses compact request")
	}
	defer upstreamResp.Body.Close()

	// 上游不提供原生 compact endpoint（404/405）：收敛为 ErrCompactUnsupported，由 service 回落 Synthetic。
	if upstreamResp.StatusCode == http.StatusNotFound || upstreamResp.StatusCode == http.StatusMethodNotAllowed {
		return nil, failure.Wrap(
			failure.CodeAdapterRequestUnsupported,
			ErrCompactUnsupported,
			failure.WithMessage(fmt.Sprintf("upstream does not support native /responses/compact (status %d)", upstreamResp.StatusCode)),
		)
	}
	if upstreamResp.StatusCode < http.StatusOK || upstreamResp.StatusCode >= http.StatusMultipleChoices {
		return nil, newUpstreamStatusError(upstreamResp, "compact")
	}

	raw, err := readAllLimited(upstreamResp.Body, maxResponsesStreamEventBytes)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterReadStreamFailed,
			err,
			failure.WithMessage("openai responses adapter read compact response body"),
		)
	}

	meta := adapter.UpstreamMetadata{
		StatusCode: upstreamResp.StatusCode,
		RequestID:  upstreamResp.Header.Get(upstreamRequestIDHeader),
	}

	var parsed wireResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// 压缩响应无法解析：保守回落 Synthetic 而非中断 Codex。
		return nil, failure.Wrap(
			failure.CodeAdapterRequestUnsupported,
			ErrCompactUnsupported,
			failure.WithMessage("openai responses adapter decode compact response"),
		)
	}
	if parsed.Error != nil {
		return nil, newUpstreamStreamError(meta, parsed.Error.Code, parsed.Error.Message)
	}

	chatUsage, ok := chatUsageFromWire(parsed.Usage)
	if !ok {
		// 无可计费 usage：回落 Synthetic（chat 摘要按真实 token 计费），避免无依据结算。
		return nil, failure.Wrap(
			failure.CodeAdapterRequestUnsupported,
			ErrCompactUnsupported,
			failure.WithMessage("openai responses adapter compact response missing usage"),
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
