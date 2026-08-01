package responses

import (
	"encoding/json"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/responses"
	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	responsesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
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
// 同一 authorization/attempt 计账），混合候选池也能在「客户帧写出前」互相 fallback。恰好设置其一。
type responsesStreamCarrier struct {
	chat   *chatcompletionsadapter.ChatStreamChunk
	direct *responsesadapter.StreamChunk
}

// responsesStreamCarrierMeta 把统一载体归一为协议无关的流式元信息。
//
// 直传普通 chunk 对客户可见；成功终态由 lifecycle 抑制到 settlement 后再交付。桥接沿用 chat 语义
// （usage 控制 chunk 抑制 emit）。
func responsesStreamCarrierMeta(c responsesStreamCarrier) lifecycle.StreamChunkMeta {
	if c.direct != nil {
		// 首字判定与可见文本同源（见 adapter 侧 FirstTokenPayload 的说明）。
		firstTokenPayload := responsesadapter.FirstTokenPayload(*c.direct)
		return lifecycle.StreamChunkMeta{
			ID:           c.direct.ResponseID,
			FinishReason: c.direct.FinishReason,
			Usage:        c.direct.Usage,
			// 成功终态必须等 durable settlement 收口后再交付。service 在 adapter 回调中保存原始
			// terminal，并由 Finish 原样写出；失败终态仍走普通 emit/error 路径。
			SuppressEmit:       isDirectResponsesSuccessTerminal(c.direct.EventType),
			FirstTokenEligible: firstTokenPayload != "",
			VisibleText:        firstTokenPayload,
			ProtocolEventType:  c.direct.EventType,
			TokenKind:          responsesTokenKind(c.direct.EventType),
			Classification:     responsesEventClassification(c.direct.EventType, firstTokenPayload),
		}
	}

	chunk := c.chat
	firstTokenPayload := chatcompletionsadapter.FirstTokenPayload(*chunk)
	meta := lifecycle.StreamChunkMeta{
		ID:                 chunk.ID,
		Usage:              chunk.Usage,
		SuppressEmit:       chunk.Usage != nil,
		FirstTokenEligible: firstTokenPayload != "",
		VisibleText:        firstTokenPayload,
		ProtocolEventType:  "chat.completion.chunk",
		TokenKind:          bridgeChatTokenKind(*chunk),
		Classification:     bridgeChatClassification(*chunk, firstTokenPayload),
	}
	if chunk.FinishReason != nil {
		meta.FinishReason = *chunk.FinishReason
	}
	return meta
}

func responsesTokenKind(eventType string) string {
	switch eventType {
	case "response.output_text.delta":
		return "text"
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		return "reasoning"
	case "response.refusal.delta":
		return "refusal"
	case "response.function_call_arguments.delta", "response.output_item.added":
		return "tool_call"
	default:
		return ""
	}
}

func responsesEventClassification(eventType string, firstTokenPayload string) string {
	if firstTokenPayload != "" {
		return "effective_token"
	}
	switch eventType {
	case "response.created", "response.queued", "response.in_progress":
		return "lifecycle"
	case "response.completed", "response.incomplete", "response.failed", "response.error":
		return "terminal"
	case "ping", "heartbeat":
		return "heartbeat"
	default:
		return "empty_generation"
	}
}

func bridgeChatTokenKind(chunk chatcompletionsadapter.ChatStreamChunk) string {
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

func bridgeChatClassification(chunk chatcompletionsadapter.ChatStreamChunk, firstTokenPayload string) string {
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

func isDirectResponsesSuccessTerminal(eventType string) bool {
	return eventType == "response.completed" || eventType == "response.incomplete"
}

// emitDirectStreamEvent 把上游 responses 事件（改写 model 回显后）原文透传给客户 SSE。
func emitDirectStreamEvent(emit func(gatewayapi.ResponsesStreamEvent) error, clientModel string, chunk responsesadapter.StreamChunk) error {
	data := rewriteResponsesModel(chunk.Data, clientModel)
	return emit(gatewayapi.RawResponsesStreamEvent(chunk.EventType, data))
}
