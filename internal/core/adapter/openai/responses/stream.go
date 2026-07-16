package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	adaptersse "github.com/ThankCat/unio-gateway/internal/core/adapter/sse"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// Responses 流式生命周期事件名（adapter 只据此识别终态/封套事件；其余事件原文透传）。
const (
	eventResponseCreated    = "response.created"
	eventResponseInProgress = "response.in_progress"
	eventResponseQueued     = "response.queued"
	eventResponseCompleted  = "response.completed"
	eventResponseIncomplete = "response.incomplete"
	eventResponseFailed     = "response.failed"
	eventOutputItemDone     = "response.output_item.done"
	eventOutputItemAdded    = "response.output_item.added"
	eventError              = "error"
)

// wireStreamEnvelope 是流式事件 data 的最小封套：用于识别 type、抽取 response 与流级 error。
type wireStreamEnvelope struct {
	Type     string        `json:"type"`
	Response *wireResponse `json:"response"`
	Code     string        `json:"code"`
	Message  string        `json:"message"`
}

// StreamResponse 调用上游 POST /responses（流式），逐 SSE 事件原文透传给 gateway。
//
// 直传零转换：上游本身就是 Responses 命名事件流，adapter 不重组事件，只
//   - 把每个事件（event 名 + data 原文）经 emit 透传；
//   - 从终态事件（completed/incomplete）解析 usage / id / finish → 账务事实；
//   - 把上游内联终态错误（response.failed / error 事件）映射成结构化上游错误。
//
// 与 chat StreamChatCompletions 一致：首个 emit 前失败可由共享循环 fallback；emit 后失败只能中断。
func (a *Adapter) StreamResponse(ctx context.Context, ch channel.Runtime, req Request, emit func(StreamChunk) error) (adapter.StreamOutcome, error) {
	if emit == nil {
		return adapter.StreamOutcome{}, failure.New(
			failure.CodeAdapterEmitFailed,
			failure.WithMessage("openai responses adapter stream emit is nil"),
		)
	}
	if ch.BaseURL == "" {
		return adapter.StreamOutcome{}, failure.New(
			failure.CodeAdapterChannelInvalid,
			failure.WithMessage("openai responses adapter channel base url is empty"),
		)
	}

	// 渠道 timeout 只约束「上游开始响应(拿到响应头)」,不约束流本体:Responses 流在长任务
	// (图像生成等)期间会先回 200 再静默数分钟才吐事件,绝不能被渠道 timeout 当绝对截止时间掐断。
	streamCtx, headersReceived, resetIdle, cancel := adapter.StreamTimeoutContext(ctx, ch.Timeout, adapter.StreamIdleTimeout())
	defer cancel()

	httpReq, err := a.newUpstreamRequest(streamCtx, ch, req, true)
	if err != nil {
		return adapter.StreamOutcome{}, err
	}

	upstreamResp, err := a.client.Do(httpReq)
	ctxCause := context.Cause(streamCtx)
	headersReceived()
	if err != nil {
		return adapter.StreamOutcome{}, newUpstreamSendErrorWithContextCause(err, ctxCause, "send stream responses request")
	}
	defer upstreamResp.Body.Close()

	if upstreamResp.StatusCode < http.StatusOK || upstreamResp.StatusCode >= http.StatusMultipleChoices {
		return adapter.StreamOutcome{}, newUpstreamStatusError(upstreamResp, "upstream stream")
	}

	meta := adapter.UpstreamMetadata{
		StatusCode: upstreamResp.StatusCode,
		RequestID:  upstreamResp.Header.Get(upstreamRequestIDHeader),
	}

	var (
		terminalResp *wireResponse
		finalUsage   *adapter.ChatUsage
		responseID   string
		terminalSeen bool
	)

	reader := adaptersse.NewReader(upstreamResp.Body, adaptersse.Config{
		MaxLineBytes:  maxResponsesStreamEventBytes,
		MaxEventBytes: maxResponsesStreamEventBytes,
		OnActivity:    resetIdle,
	})

	buildOutcome := func() adapter.StreamOutcome {
		if terminalResp == nil || finalUsage == nil {
			return adapter.StreamOutcome{}
		}
		facts := responsesFacts(*terminalResp, *finalUsage, meta, usage.SourceUpstreamStream)
		return adapter.StreamOutcome{Facts: &facts}
	}

	for reader.Next() {
		ev := reader.Event()
		data := sanitizeEventData(bytes.TrimSpace(ev.Data))

		// 个别中转会在 Responses 流尾追加 chat 风格 [DONE] 哨兵：截留为内部成功终态，不透传给客户。
		if bytes.Equal(data, []byte("[DONE]")) {
			terminalSeen = true
			break
		}
		if len(data) == 0 {
			continue
		}

		eventType := ev.Type
		if eventType == "" {
			eventType = peekEventType(data)
		}

		chunk := StreamChunk{EventType: eventType, Data: data}

		var streamErr error
		switch eventType {
		case eventResponseCreated, eventResponseInProgress, eventResponseQueued:
			if env := decodeEnvelope(data); env != nil && env.Response != nil {
				if env.Response.ID != "" {
					responseID = env.Response.ID
				}
			}
		case eventResponseCompleted, eventResponseIncomplete:
			if env := decodeEnvelope(data); env != nil && env.Response != nil {
				resp := *env.Response
				terminalResp = &resp
				if resp.ID != "" {
					responseID = resp.ID
				}
				if u, ok := chatUsageFromWire(resp.Usage); ok {
					finalUsage = &u
					chunk.Usage = &u
				}
				chunk.ResponseID = responseID
				incompleteReason := ""
				if resp.IncompleteDetails != nil {
					incompleteReason = resp.IncompleteDetails.Reason
				}
				chunk.FinishReason = responsesRawFinish(resp.Status, incompleteReason)
			}
			terminalSeen = true
		case eventResponseFailed:
			env := decodeEnvelope(data)
			code, message := "", "upstream responses failed"
			if env != nil && env.Response != nil && env.Response.Error != nil {
				code = env.Response.Error.Code
				if env.Response.Error.Message != "" {
					message = env.Response.Error.Message
				}
			}
			// P2-10：内联透传给客户端前重建为脱敏最小信封（去 base_url 等基础设施细节），仍保留 Codex 所需 error.code/message 形状。
			chunk.Data = sanitizedResponsesFailedEvent(responseID, code, message)
			streamErr = newUpstreamStreamError(meta, code, message)
		case eventError:
			env := decodeEnvelope(data)
			code, message := "", "upstream responses stream error"
			if env != nil {
				code = env.Code
				if env.Message != "" {
					message = env.Message
				}
			}
			// P2-10：同上，error 事件重建为脱敏最小信封后再透传。
			chunk.Data = sanitizedResponsesErrorEvent(code, message)
			streamErr = newUpstreamStreamError(meta, code, message)
		}

		// 终态错误事件也先原文透传（Codex 据 response.failed/error 映射 ApiError），再以结构化错误中断。
		if emitErr := emit(chunk); emitErr != nil {
			return buildOutcome(), failure.Wrap(
				failure.CodeAdapterEmitFailed,
				emitErr,
				failure.WithMessage("openai responses adapter emit stream event"),
			)
		}

		if streamErr != nil {
			return buildOutcome(), streamErr
		}
		if terminalSeen {
			break
		}
	}

	if err := reader.Err(); err != nil {
		return buildOutcome(),
			newUpstreamStreamReadError(err, context.Cause(streamCtx), "openai responses adapter read stream event")
	}

	outcome := buildOutcome()
	if !terminalSeen {
		return outcome, newUpstreamStreamIncompleteError("openai responses adapter stream ended before terminal event")
	}
	return outcome, nil
}

// sanitizeEventData 修复个别中转发出的畸形多行 data 帧。
//
// 合规上游每个 Responses 事件只用一行 data 承载一个紧凑 JSON 对象；但观测到某些中转
// （如 blackaicoding 经 Unio 透传）会在真正事件前多塞一行残片——例如先发 `data: {"type"`、
// 紧接着再发 `data: {完整事件}`。SSE 规范要求把同一事件的多行 data 用 \n 合并，于是聚合后变成
// `{"type"\n{完整事件}` 这样的非法 JSON。若放任不管：① 透传时 json.Marshal 校验失败 → emit 失败 →
// 整条流中断（客户端表现为 "stream disconnected"）；② 命中终态事件时 usage/finish 解析也会失败 → 丢计费。
//
// 处理策略：仅当「整体非法 JSON 且按 \n 切分后存在合法 JSON 段」时，取最长的合法 JSON 段当作真事件。
// 正常情况（整体已是合法 JSON，包括合法的多行 pretty JSON）原样返回，零影响、零转换。
func sanitizeEventData(data []byte) []byte {
	if len(data) == 0 || json.Valid(data) {
		return data
	}
	if bytes.IndexByte(data, '\n') < 0 {
		return data
	}

	var best []byte
	for _, seg := range bytes.Split(data, []byte("\n")) {
		seg = bytes.TrimSpace(seg)
		if len(seg) == 0 || !json.Valid(seg) {
			continue
		}
		if len(seg) > len(best) {
			best = seg
		}
	}
	if best != nil {
		return best
	}
	return data
}

// peekEventType 在 SSE 缺少 event 行时，从 data 的 type 字段回退识别事件类型。
func peekEventType(data []byte) string {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return ""
	}
	return probe.Type
}

// decodeEnvelope 解析封套事件 data；解析失败返回 nil（不阻断流，按未知事件原文透传）。
func decodeEnvelope(data []byte) *wireStreamEnvelope {
	var env wireStreamEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil
	}
	return &env
}
