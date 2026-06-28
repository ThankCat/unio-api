package responses

import (
	"encoding/json"
	"sort"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-api/internal/core/adapter"
	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
)

// responses_stream.go 负责流式方向翻译：把内部 chatcompletionsadapter.ChatStreamChunk 序列（SSE delta）翻译成
// Responses 命名事件序列（response.created → output_item.added → *.delta → output_item.done →
// response.completed），并维护单调 sequence_number（BRIDGE §6）。
//
// 本文件只做协议形状翻译，是纯翻译层：不读取请求 ctx、不结算、不计费。账务由 service 编排层用
// adapter 同次解析的 ResponseFacts 收口（与非流式一致）。
//
// 生成的事件序列（与真实 OpenAI Responses 顺序一致）：
//
//	response.created
//	response.output_item.added
//	response.content_part.added                ← 文本增量前建立活跃 content part
//	response.output_text.delta / response.reasoning_text.delta / response.function_call_arguments.delta
//	response.output_text.done / response.reasoning_text.done
//	response.content_part.done                 ← 收口活跃 content part
//	response.output_item.done                  ← 每个 item 的最终权威载体（Codex 以此为准）
//	response.completed
//
// content_part.added/*_text.done/content_part.done 由 P0 真实 codex（v0.142.3）测试补齐：缺失这些事件时
// codex 报 "OutputTextDelta without active item" 错误。function_call 走 arguments delta，无 content part。

// streamEncoder 维护一次流式响应的事件状态机。
//
// 调用约定：每个上游内容 chunk 调一次 Handle（usage / id-only chunk 由 service 编排层在调用前过滤），
// 流正常结束后调一次 Complete。created 事件在首个内容 chunk 处惰性发出，保证「首字节前失败仍可 fallback」
// 与非流式 emitted 语义一致（BRIDGE §6）。
type streamEncoder struct {
	emit func(gatewayapi.ResponsesStreamEvent) error

	responseID string
	createdAt  int64
	model      string

	parallelToolCalls *bool
	temperature       *float64
	topP              *float64
	maxOutputTokens   *int

	// emitReasoningCarrier 控制流式 reasoning item 是否附带 encrypted_content 回放载体（U1）；
	// 与非流式 requestWantsEncryptedReasoning 同一判定，保证两路对客户形态一致。
	emitReasoningCarrier bool

	seq     int64
	started bool

	nextOutputIndex int

	reasoning *streamItemState
	message   *streamItemState
	tools     []*streamToolState
	toolByIdx map[int]*streamToolState
}

// streamItemState 累积 reasoning / message item 的输出索引与文本。
// refusal 仅 message item 使用：上游 refusal 增量累积后随 output_item.done 落 refusal content part。
// partOpen 记录该 item 的文本 content part（message=output_text / reasoning=reasoning_text）是否已发出
// response.content_part.added，用于在收尾时配对发出 *_text.done + content_part.done（Codex 0.142.3 要求
// 文本增量前必须有「活跃 content part」，否则报 OutputTextDelta without active item）。
type streamItemState struct {
	id          string
	outputIndex int
	text        string
	refusal     string
	partOpen    bool
}

// streamToolState 累积单个 function_call item 的标识与分片参数。
type streamToolState struct {
	id          string
	outputIndex int
	callID      string
	name        string
	arguments   string
}

// streamToolCallDelta 是上游 chat tool_calls 流式分片的形状（OpenAI 增量：按 index 聚合）。
type streamToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// newStreamEncoder 基于请求回显字段构造事件状态机。
func newStreamEncoder(req gatewayapi.ResponsesRequest, responseID string, createdAt int64, emit func(gatewayapi.ResponsesStreamEvent) error) *streamEncoder {
	return &streamEncoder{
		emit:                 emit,
		responseID:           responseID,
		createdAt:            createdAt,
		model:                req.Model,
		parallelToolCalls:    req.ParallelToolCalls,
		temperature:          req.Temperature,
		topP:                 req.TopP,
		maxOutputTokens:      responsesIntPtr(req.MaxOutputTokens),
		emitReasoningCarrier: requestWantsEncryptedReasoning(req),
		toolByIdx:            map[int]*streamToolState{},
	}
}

// Started 表示是否已发出 response.created；service 编排层用它判断 emitted（首字节后不再 fallback）。
func (e *streamEncoder) Started() bool { return e.started }

// Handle 消费单个上游内容 chunk，发出对应的增量命名事件。
//
// 调用方必须已过滤 usage chunk 与纯 id chunk；本方法只处理 reasoning / 文本 / tool_call 增量。
func (e *streamEncoder) Handle(chunk chatcompletionsadapter.ChatStreamChunk) error {
	if err := e.ensureStarted(); err != nil {
		return err
	}

	if chunk.ReasoningContent != nil && *chunk.ReasoningContent != "" {
		if err := e.handleReasoningDelta(*chunk.ReasoningContent); err != nil {
			return err
		}
	}

	if chunk.Content != "" {
		if err := e.handleTextDelta(chunk.Content); err != nil {
			return err
		}
	}

	if chunk.Refusal != nil && *chunk.Refusal != "" {
		if err := e.handleRefusalDelta(*chunk.Refusal); err != nil {
			return err
		}
	}

	if len(chunk.ToolCalls) > 0 {
		if err := e.handleToolCallDeltas(chunk.ToolCalls); err != nil {
			return err
		}
	}

	return nil
}

// Complete 在流式正常结束后收尾：发出每个 item 的 output_item.done，再发 response.completed。
//
// finishReason 决定终态（length/content_filter → incomplete，其余 → completed）；usage 仅供客户读取。
func (e *streamEncoder) Complete(finishReason string, usage *adapter.ChatUsage) error {
	if err := e.ensureStarted(); err != nil {
		return err
	}

	output, err := e.closeItems()
	if err != nil {
		return err
	}

	status, incomplete := responseStatusFromFinish(finishReason)
	resp := e.snapshot(status, output)
	resp.IncompleteDetails = incomplete
	if usage != nil {
		resp.Usage = mapResponsesUsage(*usage)
	}

	completedType := gatewayapi.EventResponseCompleted
	if status == "incomplete" {
		completedType = gatewayapi.EventResponseIncomplete
	}
	return e.emitEvent(gatewayapi.ResponsesStreamEvent{
		Type:     completedType,
		Response: &resp,
	})
}

// ensureStarted 惰性发出 response.created（首个内容 chunk 触发），output 为空、status=in_progress。
func (e *streamEncoder) ensureStarted() error {
	if e.started {
		return nil
	}
	e.started = true
	resp := e.snapshot("in_progress", []gatewayapi.ResponseOutputItem{})
	return e.emitEvent(gatewayapi.ResponsesStreamEvent{
		Type:     gatewayapi.EventResponseCreated,
		Response: &resp,
	})
}

func (e *streamEncoder) handleReasoningDelta(delta string) error {
	if e.reasoning == nil {
		e.reasoning = &streamItemState{id: newResponsesID("rs"), outputIndex: e.takeOutputIndex()}
		item := gatewayapi.ResponseOutputItem{
			Type:    "reasoning",
			ID:      e.reasoning.id,
			Summary: []gatewayapi.ResponseOutputContent{},
		}
		if err := e.emitItemAdded(e.reasoning.outputIndex, item); err != nil {
			return err
		}
		// 文本增量前先发出 content_part.added，建立 Codex 解析所需的「活跃 content part」。
		if err := e.emitContentPartAdded(e.reasoning.id, e.reasoning.outputIndex, gatewayapi.ResponseOutputContent{Type: "reasoning_text"}); err != nil {
			return err
		}
		e.reasoning.partOpen = true
	}
	e.reasoning.text += delta
	return e.emitEvent(gatewayapi.ResponsesStreamEvent{
		Type:         gatewayapi.EventReasoningTextDelta,
		ItemID:       e.reasoning.id,
		OutputIndex:  intPtr(e.reasoning.outputIndex),
		ContentIndex: intPtr(0),
		Delta:        delta,
	})
}

// ensureMessageItem 惰性创建 assistant message item 并发出 output_item.added（首个 text/refusal 增量触发）。
func (e *streamEncoder) ensureMessageItem() error {
	if e.message != nil {
		return nil
	}
	e.message = &streamItemState{id: newResponsesID("msg"), outputIndex: e.takeOutputIndex()}
	return e.emitItemAdded(e.message.outputIndex, gatewayapi.ResponseOutputItem{
		Type:   "message",
		ID:     e.message.id,
		Role:   "assistant",
		Status: "in_progress",
	})
}

func (e *streamEncoder) handleTextDelta(delta string) error {
	if err := e.ensureMessageItem(); err != nil {
		return err
	}
	// 首个文本增量前惰性发出 content_part.added，建立 Codex 解析所需的「活跃 content part」。
	// 惰性发出（而非 message item 创建时）确保「仅 refusal、无文本」的流不会落空的 output_text part。
	if !e.message.partOpen {
		if err := e.emitContentPartAdded(e.message.id, e.message.outputIndex, gatewayapi.ResponseOutputContent{Type: "output_text"}); err != nil {
			return err
		}
		e.message.partOpen = true
	}
	e.message.text += delta
	return e.emitEvent(gatewayapi.ResponsesStreamEvent{
		Type:         gatewayapi.EventOutputTextDelta,
		ItemID:       e.message.id,
		OutputIndex:  intPtr(e.message.outputIndex),
		ContentIndex: intPtr(0),
		Delta:        delta,
	})
}

// handleRefusalDelta 累积上游 refusal 增量到 message item。
//
// 与非流式 mapChatResponseToResponses 对齐：refusal 是 message item 内的 refusal content part。
// refusal 增量事件不在 v1 Codex 消费子集（见文件头），最终 refusal 文本随 output_item.done 权威载体
// 与 response.completed 一并下发，保证流式与非流式对客户呈现一致，不丢 content_filter/refusal 信息。
func (e *streamEncoder) handleRefusalDelta(delta string) error {
	if err := e.ensureMessageItem(); err != nil {
		return err
	}
	e.message.refusal += delta
	return nil
}

func (e *streamEncoder) handleToolCallDeltas(raw json.RawMessage) error {
	var deltas []streamToolCallDelta
	if err := json.Unmarshal(raw, &deltas); err != nil {
		// 上游分片不可解析时不阻断流：跳过该分片，最终以 output_item.done / completed 收口。
		return nil
	}
	for _, d := range deltas {
		tool := e.toolByIdx[d.Index]
		if tool == nil {
			tool = &streamToolState{id: newResponsesID("fc"), outputIndex: e.takeOutputIndex(), callID: d.ID, name: d.Function.Name}
			e.toolByIdx[d.Index] = tool
			e.tools = append(e.tools, tool)

			_, name := splitNamespaceToolName(tool.name)
			item := gatewayapi.ResponseOutputItem{
				Type:   "function_call",
				ID:     tool.id,
				CallID: tool.callID,
				Name:   name,
				Status: "in_progress",
			}
			if err := e.emitItemAdded(tool.outputIndex, item); err != nil {
				return err
			}
		} else {
			if d.ID != "" {
				tool.callID = d.ID
			}
			tool.name += d.Function.Name
		}

		if d.Function.Arguments != "" {
			tool.arguments += d.Function.Arguments
			if err := e.emitEvent(gatewayapi.ResponsesStreamEvent{
				Type:        gatewayapi.EventFunctionCallArgsDelta,
				ItemID:      tool.id,
				OutputIndex: intPtr(tool.outputIndex),
				Delta:       d.Function.Arguments,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// streamFinalItem 是桥接流式 encoder 终态 output 的一项（保留 output_index 供 SSE 与能力埋点）。
type streamFinalItem struct {
	outputIndex int
	item        gatewayapi.ResponseOutputItem
}

// closeItems 按 output_index 顺序发出每个 item 的收尾事件，并返回最终 output 数组。
//
// 每个 item 在 output_item.done 之前，先把曾经打开的文本 content part 配对收口：
// message → output_text.done + content_part.done(output_text)；reasoning → reasoning_text.done +
// content_part.done(reasoning_text)。这与真实 OpenAI Responses 事件顺序一致，满足 Codex 的活跃 part 校验。
func (e *streamEncoder) closeItems() ([]gatewayapi.ResponseOutputItem, error) {
	finals := e.collectFinalItems()
	output := make([]gatewayapi.ResponseOutputItem, 0, len(finals))
	for _, f := range finals {
		if err := e.emitItemContentDone(f); err != nil {
			return nil, err
		}
		item := f.item
		if err := e.emitEvent(gatewayapi.ResponsesStreamEvent{
			Type:        gatewayapi.EventOutputItemDone,
			OutputIndex: intPtr(f.outputIndex),
			Item:        &item,
		}); err != nil {
			return nil, err
		}
		output = append(output, f.item)
	}
	return output, nil
}

// emitItemContentDone 在 output_item.done 之前收口该 item 打开过的文本 content part。
func (e *streamEncoder) emitItemContentDone(f streamFinalItem) error {
	switch {
	case e.message != nil && f.item.ID == e.message.id && e.message.partOpen:
		if err := e.emitEvent(gatewayapi.ResponsesStreamEvent{
			Type:         gatewayapi.EventOutputTextDone,
			ItemID:       e.message.id,
			OutputIndex:  intPtr(f.outputIndex),
			ContentIndex: intPtr(0),
			Text:         e.message.text,
		}); err != nil {
			return err
		}
		return e.emitContentPartDone(e.message.id, f.outputIndex, gatewayapi.ResponseOutputContent{Type: "output_text", Text: e.message.text})
	case e.reasoning != nil && f.item.ID == e.reasoning.id && e.reasoning.partOpen:
		if err := e.emitEvent(gatewayapi.ResponsesStreamEvent{
			Type:         gatewayapi.EventReasoningTextDone,
			ItemID:       e.reasoning.id,
			OutputIndex:  intPtr(f.outputIndex),
			ContentIndex: intPtr(0),
			Text:         e.reasoning.text,
		}); err != nil {
			return err
		}
		return e.emitContentPartDone(e.reasoning.id, f.outputIndex, gatewayapi.ResponseOutputContent{Type: "reasoning_text", Text: e.reasoning.text})
	}
	return nil
}

func (e *streamEncoder) collectFinalItems() []streamFinalItem {
	finals := make([]streamFinalItem, 0, 2+len(e.tools))

	if e.reasoning != nil {
		reasoningItem := gatewayapi.ResponseOutputItem{
			Type:    "reasoning",
			ID:      e.reasoning.id,
			Summary: []gatewayapi.ResponseOutputContent{},
			Content: []gatewayapi.ResponseOutputContent{{Type: "reasoning_text", Text: e.reasoning.text}},
		}
		if e.emitReasoningCarrier && e.reasoning.text != "" {
			carrier := encodeReasoningCarrier(e.reasoning.text)
			reasoningItem.EncryptedContent = &carrier
		}
		finals = append(finals, streamFinalItem{e.reasoning.outputIndex, reasoningItem})
	}
	if e.message != nil {
		content := make([]gatewayapi.ResponseOutputContent, 0, 2)
		if e.message.text != "" {
			content = append(content, gatewayapi.ResponseOutputContent{Type: "output_text", Text: e.message.text})
		}
		if e.message.refusal != "" {
			content = append(content, gatewayapi.ResponseOutputContent{Type: "refusal", Refusal: e.message.refusal})
		}
		finals = append(finals, streamFinalItem{e.message.outputIndex, gatewayapi.ResponseOutputItem{
			Type:    "message",
			ID:      e.message.id,
			Role:    "assistant",
			Status:  "completed",
			Content: content,
		}})
	}
	for _, tool := range e.tools {
		namespace, name := splitNamespaceToolName(tool.name)
		item := gatewayapi.ResponseOutputItem{
			Type:      "function_call",
			ID:        tool.id,
			CallID:    tool.callID,
			Name:      name,
			Arguments: tool.arguments,
			Status:    "completed",
		}
		if namespace != "" {
			item.Namespace = namespace
		}
		finals = append(finals, streamFinalItem{tool.outputIndex, item})
	}

	sort.Slice(finals, func(i, j int) bool { return finals[i].outputIndex < finals[j].outputIndex })
	return finals
}

func (e *streamEncoder) emitItemAdded(outputIndex int, item gatewayapi.ResponseOutputItem) error {
	return e.emitEvent(gatewayapi.ResponsesStreamEvent{
		Type:        gatewayapi.EventOutputItemAdded,
		OutputIndex: intPtr(outputIndex),
		Item:        &item,
	})
}

// emitContentPartAdded 发出 response.content_part.added：在文本增量前建立活跃 content part。
func (e *streamEncoder) emitContentPartAdded(itemID string, outputIndex int, part gatewayapi.ResponseOutputContent) error {
	return e.emitEvent(gatewayapi.ResponsesStreamEvent{
		Type:         gatewayapi.EventContentPartAdded,
		ItemID:       itemID,
		OutputIndex:  intPtr(outputIndex),
		ContentIndex: intPtr(0),
		Part:         &part,
	})
}

// emitContentPartDone 发出 response.content_part.done：收口此前打开的文本 content part。
func (e *streamEncoder) emitContentPartDone(itemID string, outputIndex int, part gatewayapi.ResponseOutputContent) error {
	return e.emitEvent(gatewayapi.ResponsesStreamEvent{
		Type:         gatewayapi.EventContentPartDone,
		ItemID:       itemID,
		OutputIndex:  intPtr(outputIndex),
		ContentIndex: intPtr(0),
		Part:         &part,
	})
}

// snapshot 构造当前 response 对象快照（response.created / completed 复用）。
func (e *streamEncoder) snapshot(status string, output []gatewayapi.ResponseOutputItem) gatewayapi.ResponsesResponse {
	return gatewayapi.ResponsesResponse{
		ID:                e.responseID,
		Object:            "response",
		CreatedAt:         e.createdAt,
		Model:             e.model,
		Status:            status,
		Output:            output,
		ParallelToolCalls: e.parallelToolCalls,
		Temperature:       e.temperature,
		TopP:              e.topP,
		MaxOutputTokens:   e.maxOutputTokens,
	}
}

func (e *streamEncoder) emitEvent(ev gatewayapi.ResponsesStreamEvent) error {
	ev.SequenceNumber = e.seq
	e.seq++
	return e.emit(ev)
}

func (e *streamEncoder) takeOutputIndex() int {
	idx := e.nextOutputIndex
	e.nextOutputIndex++
	return idx
}

func intPtr(i int) *int { return &i }
