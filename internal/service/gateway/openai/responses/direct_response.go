package responses

import (
	"encoding/json"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
	responsesadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// direct_response.go 承载「上游 responses 直传」分流的 service 侧粘合：把 ingress ResponsesRequest
// 重放为上游请求体、把上游响应/事件原文（仅改写 model 回显）透传给客户，并提供与桥接共享一条流式
// 候选循环的统一 chunk 载体。账务/lifecycle 全部复用既有 AttemptRunner，与桥接零差异。

// encodeUpstreamResponsesBody 生成发往上游 /responses 的请求体：以客户原始请求体为基底零损耗重放，
// 仅改写 model（→ candidate upstream model）与 stream（→ 本次调用方式）。
//
// 无原始请求体时（如单测直接构造 ResponsesRequest）回退到 typed 重编码 + 合并 Extensions。
func encodeUpstreamResponsesBody(req gatewayapi.ResponsesRequest, upstreamModel string, stream bool) (json.RawMessage, error) {
	base := req.RawBody()
	if len(base) == 0 {
		encoded, err := json.Marshal(req)
		if err != nil {
			return nil, failure.Wrap(
				failure.CodeAdapterEncodeRequestFailed,
				err,
				failure.WithMessage("encode upstream responses request body"),
			)
		}
		base = encoded
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(base, &obj); err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterEncodeRequestFailed,
			err,
			failure.WithMessage("decode upstream responses request base"),
		)
	}
	if obj == nil {
		obj = make(map[string]json.RawMessage, len(req.Extensions)+2)
	}

	// typed 回退路径下 Extensions 因 json:"-" 不在 base 中，按缺失补回（raw 路径已包含，不覆盖）。
	for key, value := range req.Extensions {
		if _, exists := obj[key]; !exists {
			obj[key] = value
		}
	}

	modelBytes, err := json.Marshal(upstreamModel)
	if err != nil {
		return nil, failure.Wrap(failure.CodeAdapterEncodeRequestFailed, err, failure.WithMessage("encode upstream model"))
	}
	obj["model"] = modelBytes

	streamBytes, err := json.Marshal(stream)
	if err != nil {
		return nil, failure.Wrap(failure.CodeAdapterEncodeRequestFailed, err, failure.WithMessage("encode upstream stream flag"))
	}
	obj["stream"] = streamBytes

	body, err := json.Marshal(obj)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterEncodeRequestFailed,
			err,
			failure.WithMessage("encode upstream responses request body"),
		)
	}
	return body, nil
}

// rewriteResponsesModel 在上游响应/事件原文中把 model 回显改写为客户请求的模型名。
//
// 直传保真：只动 model 字段（顶层 model 与嵌套 response.model），不重排/丢弃其它字段；解析失败或无
// model 字段时原样返回（best-effort，绝不阻断流）。
func rewriteResponsesModel(data json.RawMessage, clientModel string) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return data
	}

	modelBytes, err := json.Marshal(clientModel)
	if err != nil {
		return data
	}

	changed := false
	if _, ok := obj["model"]; ok {
		obj["model"] = modelBytes
		changed = true
	}
	if respRaw, ok := obj["response"]; ok {
		var resp map[string]json.RawMessage
		if json.Unmarshal(respRaw, &resp) == nil {
			if _, ok := resp["model"]; ok {
				resp["model"] = modelBytes
				if encoded, err := json.Marshal(resp); err == nil {
					obj["response"] = encoded
					changed = true
				}
			}
		}
	}

	if !changed {
		return data
	}
	if encoded, err := json.Marshal(obj); err == nil {
		return encoded
	}
	return data
}

// responsesStreamCarrier 是流式分流的统一 chunk 载体：桥接候选产出 chat chunk，直传候选产出 responses 事件。
//
// 让 chat 桥接与 responses 直传两类候选共享同一条 AttemptRunner 流式 fallback 循环（同一资金关键链路、
// 同一 authorization/attempt 计账），混合候选池也能在「首字节前」互相 fallback。恰好设置其一。
type responsesStreamCarrier struct {
	chat   *chatcompletionsadapter.ChatStreamChunk
	direct *responsesadapter.StreamChunk
}

// responsesStreamCarrierMeta 把统一载体归一为协议无关的流式元信息。
//
// 直传 chunk 全程对客户可见（SuppressEmit=false）；桥接沿用 chat 语义（usage 控制 chunk 抑制 emit）。
func responsesStreamCarrierMeta(c responsesStreamCarrier) lifecycle.StreamChunkMeta {
	if c.direct != nil {
		return lifecycle.StreamChunkMeta{
			ID:           c.direct.ResponseID,
			FinishReason: c.direct.FinishReason,
			Usage:        c.direct.Usage,
			SuppressEmit: false,
			VisibleText:  directResponsesVisibleText(*c.direct),
		}
	}

	chunk := c.chat
	meta := lifecycle.StreamChunkMeta{
		ID:           chunk.ID,
		Usage:        chunk.Usage,
		SuppressEmit: chunk.Usage != nil,
		VisibleText:  chunk.Content,
	}
	if chunk.FinishReason != nil {
		meta.FinishReason = *chunk.FinishReason
	}
	return meta
}

// directResponsesVisibleText extracts customer-visible text deltas from raw Responses stream events.
//
// It is used only for partial stream settlement when final usage is missing; full billing still consumes
// adapter facts from the terminal event.
func directResponsesVisibleText(chunk responsesadapter.StreamChunk) string {
	switch chunk.EventType {
	case gatewayapi.EventOutputTextDelta,
		gatewayapi.EventReasoningTextDelta,
		gatewayapi.EventReasoningSummaryTextDelta,
		gatewayapi.EventFunctionCallArgsDelta:
	default:
		return ""
	}

	var payload struct {
		Delta string `json:"delta"`
	}
	if err := json.Unmarshal(chunk.Data, &payload); err != nil {
		return ""
	}
	return payload.Delta
}

// emitDirectStreamEvent 把上游 responses 事件（改写 model 回显后）原文透传给客户 SSE。
func emitDirectStreamEvent(emit func(gatewayapi.ResponsesStreamEvent) error, clientModel string, chunk responsesadapter.StreamChunk) error {
	data := rewriteResponsesModel(chunk.Data, clientModel)
	return emit(gatewayapi.RawResponsesStreamEvent(chunk.EventType, data))
}
