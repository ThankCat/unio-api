package responses

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
)

// responses_response_map.go 负责响应方向翻译：把内部 openai.ChatResponse 翻译成 Responses
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
func mapChatResponseToResponses(req gatewayapi.ResponsesRequest, chatResp openai.ChatResponse) gatewayapi.ResponsesResponse {
	status, incomplete := responseStatusFromFinish(chatResp.FinishReason)

	output := make([]gatewayapi.ResponseOutputItem, 0, 2+len(chatResp.ToolCalls))

	// reasoning：DeepSeek reasoning_content 是开源模型原始 CoT，落 reasoning item 的
	// content:[{type:"reasoning_text"}]（BRIDGE §4/§6 已冻结，非 summary_text）。
	if chatResp.ReasoningContent != nil && *chatResp.ReasoningContent != "" {
		output = append(output, gatewayapi.ResponseOutputItem{
			Type:    "reasoning",
			ID:      newResponsesID("rs"),
			Summary: []gatewayapi.ResponseOutputContent{},
			Content: []gatewayapi.ResponseOutputContent{{
				Type: "reasoning_text",
				Text: *chatResp.ReasoningContent,
			}},
		})
	}

	// message：assistant 文本与 refusal 合并进单条 message item 的 content parts。
	messageContent := make([]gatewayapi.ResponseOutputContent, 0, 2)
	if chatResp.Content != "" {
		messageContent = append(messageContent, gatewayapi.ResponseOutputContent{
			Type: "output_text",
			Text: chatResp.Content,
		})
	}
	if chatResp.Refusal != nil && *chatResp.Refusal != "" {
		messageContent = append(messageContent, gatewayapi.ResponseOutputContent{
			Type:    "refusal",
			Refusal: *chatResp.Refusal,
		})
	}
	if len(messageContent) > 0 {
		output = append(output, gatewayapi.ResponseOutputItem{
			Type:    "message",
			ID:      newResponsesID("msg"),
			Role:    "assistant",
			Status:  "completed",
			Content: messageContent,
		})
	}

	// function_call：每个工具调用一项顶层 item；MCP namespace 工具按 §3.3 拆回 namespace + name。
	for _, call := range chatResp.ToolCalls {
		namespace, name := splitNamespaceToolName(call.Function.Name)
		item := gatewayapi.ResponseOutputItem{
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

	resp := gatewayapi.ResponsesResponse{
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
		MaxOutputTokens:   req.MaxOutputTokens,
	}
	if resp.CreatedAt == 0 {
		resp.CreatedAt = time.Now().Unix()
	}

	return resp
}

// responseStatusFromFinish 把 Chat finish_reason 映射为 Responses status + incomplete_details（BRIDGE §4.1）。
func responseStatusFromFinish(finishReason string) (string, *gatewayapi.ResponsesIncompleteDetails) {
	switch finishReason {
	case "length":
		return "incomplete", &gatewayapi.ResponsesIncompleteDetails{Reason: "max_output_tokens"}
	case "content_filter":
		return "incomplete", &gatewayapi.ResponsesIncompleteDetails{Reason: "content_filter"}
	default:
		// stop / tool_calls / function_call / 空 → completed。
		return "completed", nil
	}
}

// mapResponsesUsage 把内部 ChatUsage 渲染成 Responses usage（BRIDGE §5，仅供客户读取，不作账务）。
func mapResponsesUsage(u adapter.ChatUsage) *gatewayapi.ResponsesUsage {
	out := &gatewayapi.ResponsesUsage{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
		TotalTokens:  u.TotalTokens,
	}
	if u.CachedTokens > 0 {
		out.InputTokensDetails = &gatewayapi.ResponsesInputTokensDetails{CachedTokens: u.CachedTokens}
	}
	if u.ReasoningTokens > 0 {
		out.OutputTokensDetails = &gatewayapi.ResponsesOutputTokensDetails{ReasoningTokens: u.ReasoningTokens}
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
