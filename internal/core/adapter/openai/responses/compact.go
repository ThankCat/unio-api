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

// ErrCompactUnsupported 表示上游确实不提供原生 /responses/compact endpoint（404/405）——上游未处理、
// 无成本：service 据此安全回落 SyntheticCompact（chat 摘要按真实 token 计费），避免 Codex 断链。
var ErrCompactUnsupported = errors.New("openai responses adapter native compact unsupported")

// ErrCompactMissingUsage 表示原生 /responses/compact 返回 2xx（上游很可能已处理并计费），但响应缺少
// 可计费 usage 或无法解析。与 ErrCompactUnsupported（404/405，无成本）本质不同：此情形上游可能已产生
// 成本，调用方绝不能静默回落 Synthetic 再调一次（会「双调上游、只收一次费」白嫖），应记 risk_exposure
// 并报错（P0-3）。它沿 *adapter.UpstreamError(server_error) 上抛，便于 lifecycle 记账与 handler 映射 502。
var ErrCompactMissingUsage = errors.New("openai responses adapter native compact returned 2xx without billable usage")

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

	raw, exceeded, err := adapter.ReadUpstreamBodyLimited(upstreamResp.Body)
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

	// 上游 2xx 但 body 超限无法完整解析：很可能已处理并计费，不静默回落白嫖，按缺 usage 记 risk_exposure。
	if exceeded {
		return nil, compactMissingUsageError(meta, "openai responses adapter compact response body exceeds limit")
	}

	var parsed wireResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// 上游已返回 2xx（很可能已处理并计费）却无法解析：不静默回落白嫖，按缺 usage 记 risk_exposure。
		return nil, compactMissingUsageError(meta, "openai responses adapter decode compact response")
	}
	if parsed.Error != nil {
		return nil, newUpstreamStreamError(meta, parsed.Error.Code, parsed.Error.Message)
	}

	chatUsage, ok := chatUsageFromWire(parsed.Usage)
	if !ok {
		// 上游 2xx 但缺可计费 usage（很可能已产生成本）：不静默回落白嫖，记 risk_exposure 并报错。
		return nil, compactMissingUsageError(meta, "openai responses adapter compact response missing usage")
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

// compactMissingUsageError 构造「原生 compact 返回 2xx 但拿不到可计费 usage」的结构化上游错误。
//
// 用 server_error 上游分类承载（handler 据此映射 502 upstream_error，不透传上游 body），cause 携带稳定
// failure.Code 与 ErrCompactMissingUsage sentinel，便于 lifecycle 沿链 errors.Is 判定为「有成本无 usage」
// 并记 risk_exposure（区别于真 404/405 的无成本回落）。
func compactMissingUsageError(meta adapter.UpstreamMetadata, detail string) error {
	return adapter.NewUpstreamError(
		adapter.UpstreamErrorServer,
		meta,
		failure.Wrap(
			failure.CodeAdapterInvalidResponse,
			ErrCompactMissingUsage,
			failure.WithMessage(detail),
		),
	)
}
