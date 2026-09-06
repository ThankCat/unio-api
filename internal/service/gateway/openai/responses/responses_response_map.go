package responses

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/openai/responses/dto"
)

// responses_response_map.go 负责响应方向翻译：把内部 chatcompletionsadapter.ChatResponse 翻译成 Responses
// 非流式响应对象（BRIDGE §4/§4.1/§5）。账务与翻译无关：settlement 只消费 adapter 同次解析的
// ResponseFacts，本文件只把公开 ChatResponse 渲染成 Responses 形状供 Codex/SDK 读取。
//
// output item 顺序固定为 reasoning → message → function_call（BRIDGE §4）。

// mcpNamespacePrefix 是 Codex MCP namespace 工具名的固定前缀（BRIDGE §3.3）。
const mcpNamespacePrefix = "mcp" + namespaceToolSeparator

// mapChatResponseToResponses 把内部 ChatResponse 翻译为 Responses 非流式响应对象。
//
// model 回显客户模型名（req.Model，方案 A）；id 新生成 resp_*，上游 chat id 仅记入审计事实，
// 不作为对外响应 id。created_at 优先透传上游 Created，缺失时回退本地时间，保持形状有值。
func mapChatResponseToResponses(req dto.ResponsesRequest, chatResp chatcompletionsadapter.ChatResponse) dto.ResponsesResponse {
	status, incomplete := responseStatusFromFinish(chatResp.FinishReason)

	output := make([]dto.ResponseOutputItem, 0, 2+len(chatResp.ToolCalls))

	// reasoning：DeepSeek reasoning_content 是开源模型原始 CoT，落 reasoning item 的
	// content:[{type:"reasoning_text"}]（BRIDGE §4/§6 已冻结，非 summary_text）。
	if chatResp.ReasoningContent != nil && *chatResp.ReasoningContent != "" {
		reasoningItem := dto.ResponseOutputItem{
			Type:    "reasoning",
			ID:      newResponsesID("rs"),
			Summary: []dto.ResponseOutputContent{},
		}
		// 按客户端索取的形态承载：请求 summary 的客户（Codex）只认 summary_text，
		// 收到 raw reasoning_text 会丢弃整个 item。
		if requestWantsReasoningSummary(req) {
			reasoningItem.Summary = []dto.ResponseOutputContent{{
				Type: "summary_text",
				Text: *chatResp.ReasoningContent,
			}}
		} else {
			reasoningItem.Content = []dto.ResponseOutputContent{{
				Type: "reasoning_text",
				Text: *chatResp.ReasoningContent,
			}}
		}
		// 无状态跨轮思维链回放载体（U1）：客户按 reasoning.encrypted_content 原样回传，Unio 解码后
		// 在工具调用轮回灌 DeepSeek（避免 400）。仅在客户显式请求该 include 或无状态(store=false)时附带，
		// 不给不需要的客户引入额外字段。
		if requestWantsEncryptedReasoning(req) {
			carrier := encodeReasoningCarrier(*chatResp.ReasoningContent)
			reasoningItem.EncryptedContent = &carrier
		}
		output = append(output, reasoningItem)
	}

	// message：assistant 文本与 refusal 合并进单条 message item 的 content parts。
	messageContent := make([]dto.ResponseOutputContent, 0, 2)
	if chatResp.Content != "" {
		messageContent = append(messageContent, dto.ResponseOutputContent{
			Type: "output_text",
			Text: chatResp.Content,
		})
	}
	if chatResp.Refusal != nil && *chatResp.Refusal != "" {
		messageContent = append(messageContent, dto.ResponseOutputContent{
			Type:    "refusal",
			Refusal: *chatResp.Refusal,
		})
	}
	if len(messageContent) > 0 {
		output = append(output, dto.ResponseOutputItem{
			Type:    "message",
			ID:      newResponsesID("msg"),
			Role:    "assistant",
			Status:  "completed",
			Content: messageContent,
		})
	}

	// function_call：每个工具调用一项顶层 item；MCP namespace 工具按 §3.3 拆回 namespace + name。
	// 请求里声明为 custom 的工具需还原回 custom_tool_call 形态，否则 Codex 认不出 apply_patch。
	customTools := customToolNames(req.Tools)
	for _, call := range chatResp.ToolCalls {
		if _, isCustom := customTools[call.Function.Name]; isCustom {
			output = append(output, customToolCallOutputItem(call))
			continue
		}
		namespace, name := splitNamespaceToolName(call.Function.Name)
		item := dto.ResponseOutputItem{
			Type:      "function_call",
			ID:        newResponsesID("fc"),
			CallID:    call.ID,
			Name:      name,
			Arguments: call.Function.Arguments,
			Status:    "completed",
		}
		if namespace != "" {
			item.Namespace = namespace
		}
		output = append(output, item)
	}

	resp := dto.ResponsesResponse{
		ID:                newResponsesID("resp"),
		Object:            "response",
		CreatedAt:         chatResp.Created,
		Model:             req.Model,
		Status:            status,
		Output:            output,
		Usage:             mapResponsesUsage(chatResp.Usage),
		IncompleteDetails: incomplete,
		ParallelToolCalls: req.ParallelToolCalls,
		Temperature:       req.Temperature,
		TopP:              req.TopP,
		MaxOutputTokens:   responsesIntPtr(req.MaxOutputTokens),
	}
	if resp.CreatedAt == 0 {
		resp.CreatedAt = time.Now().Unix()
	}

	return resp
}

// requestWantsReasoningSummary 判断客户端是否以 summary 形态索取思维链。
//
// OpenAI 协议下 raw reasoning content 需要 include:["reasoning.content"] 才会下发，
// 而 reasoning.summary 非空表示客户端只准备接收 summary 形态。实测 Codex v0.147 只发
// reasoning:{"summary":"auto"} 与 include:["reasoning.encrypted_content"]，此时若按 raw
// content 形态下发（content_part.added(reasoning_text) + reasoning_text.delta），Codex 的
// 事件状态机没有对应的活跃 item，会报 "ReasoningRawContentDelta without active item" 并
// 丢弃整个 reasoning item——后续轮次便无法回传 encrypted_content，桥接也就无法回灌
// reasoning_content，chat-only 上游（DeepSeek）随即以 400 拒绝整轮工具调用。
func requestWantsReasoningSummary(req dto.ResponsesRequest) bool {
	return req.Reasoning != nil && req.Reasoning.Summary != nil && strings.TrimSpace(*req.Reasoning.Summary) != ""
}

// requestWantsEncryptedReasoning 判断是否应在 reasoning item 附带 encrypted_content 回放载体。
//
// Codex 无状态会带 include:["reasoning.encrypted_content"] 且 store:false；满足其一即附带载体，
// 让客户能把思维链原样带回、在工具调用轮回灌 DeepSeek（U1）。
func requestWantsEncryptedReasoning(req dto.ResponsesRequest) bool {
	for _, inc := range req.Include {
		if inc == "reasoning.encrypted_content" {
			return true
		}
	}
	return req.Store != nil && !*req.Store
}

// responseStatusFromFinish 把 Chat finish_reason 映射为 Responses status + incomplete_details（BRIDGE §4.1）。
func responseStatusFromFinish(finishReason string) (string, *dto.ResponsesIncompleteDetails) {
	switch finishReason {
	case "length":
		return "incomplete", &dto.ResponsesIncompleteDetails{Reason: "max_output_tokens"}
	case "content_filter":
		return "incomplete", &dto.ResponsesIncompleteDetails{Reason: "content_filter"}
	default:
		// stop / tool_calls / function_call / 空 → completed。
		return "completed", nil
	}
}

// mapResponsesUsage 把内部 ChatUsage 渲染成 Responses usage（BRIDGE §5，仅供客户读取，不作账务）。
func mapResponsesUsage(u adapter.ChatUsage) *dto.ResponsesUsage {
	out := &dto.ResponsesUsage{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
		TotalTokens:  u.TotalTokens,
	}
	if u.CachedTokens > 0 {
		out.InputTokensDetails = &dto.ResponsesInputTokensDetails{CachedTokens: u.CachedTokens}
	}
	if u.ReasoningTokens > 0 {
		out.OutputTokensDetails = &dto.ResponsesOutputTokensDetails{ReasoningTokens: u.ReasoningTokens}
	}
	return out
}

// splitNamespaceToolName 把拍平的 Chat 工具名回译为 Responses function_call 的 namespace + name（BRIDGE §3.3）。
//
// 仅对 Codex MCP 约定前缀 "mcp__<server>__<tool>" 触发拆分，避免误伤普通 function 名中的 "__"。
// 不匹配时原样返回完整名、空 namespace（namespace 回译保真度定稿见 GAP-11-002 / TASK-11.08）。
func splitNamespaceToolName(flattened string) (namespace string, name string) {
	if !strings.HasPrefix(flattened, mcpNamespacePrefix) {
		return "", flattened
	}
	rest := flattened[len(mcpNamespacePrefix):]
	sep := strings.Index(rest, namespaceToolSeparator)
	if sep <= 0 {
		return "", flattened
	}
	server := rest[:sep]
	tool := rest[sep+len(namespaceToolSeparator):]
	if server == "" || tool == "" {
		return "", flattened
	}
	return mcpNamespacePrefix + server + namespaceToolSeparator, tool
}

// newResponsesID 生成对外响应 item ID（resp_/msg_/rs_/fc_）。
//
// 这是公开协议 id，不是数据库请求事实标识；rand 不可用时回退时间戳，保证形状始终有值。
func newResponsesID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
