// Package responses 实现 Gateway 的 OpenAI Responses API（POST /v1/responses）service 层
// 桥接编排（DEC-014：responses-to-chat）。
//
// 本文件只负责请求方向翻译：把 ingress 的 Responses DTO 翻译成内部
// openai.ChatRequest 契约，复用既有 OpenAI adapter / routing / lifecycle / settlement，
// 不新增上游 Responses adapter。字段语义映射（Pass/Adapt/Drop/Reject）以
// docs/chapters/phase-11-openai-responses-api/RESPONSES_CHAT_BRIDGE.md 为准。
//
// 职责边界（BRIDGE §1）：桥接层只做协议结构翻译，能映射进 ChatRequest 契约的字段一律 Adapt
// 进契约；provider（DeepSeek）能力裁剪由 adapter 出站 dropUnsupported 负责，桥接层不重复硬 Drop。
// 本文件的 Drop 仅用于 **契约无承载字段** 的 Responses 专属字段（如 previous_response_id / include /
// truncation / background / Codex 专属 client_metadata）。
package responses

import (
	"encoding/json"
	"strings"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
)

// requestTranslation 记录 Responses→Chat 翻译的副作用，供 service 层写入请求审计。
//
// DroppedFields 只收录“契约无承载、本阶段桥接层 Drop”的 Responses 专属字段名；
// provider 能力裁剪（adapter 出站 Drop）不在此记录。
type requestTranslation struct {
	DroppedFields []string
}

func (t *requestTranslation) drop(field string) {
	t.DroppedFields = append(t.DroppedFields, field)
}

// input item 判别类型（与 ingress validation 常量对齐，BRIDGE §2）。
const (
	itemTypeMessage            = "message"
	itemTypeFunctionCall       = "function_call"
	itemTypeFunctionCallOutput = "function_call_output"
	itemTypeReasoning          = "reasoning"
	itemTypeItemReference      = "item_reference"
	itemTypeCompaction         = "compaction"
)

// namespaceToolSeparator 是 Codex MCP namespace 工具拍平后的名称分隔符（BRIDGE §3.3 方案 B）。
const namespaceToolSeparator = "__"

// mapResponsesRequestToChat 把 Responses 请求翻译为内部 openai.ChatRequest。
//
// upstreamModel 是 routing 解析出的上游模型名（方案 A，DEC-014）；客户模型名只用于 routing。
func mapResponsesRequestToChat(req gatewayapi.ResponsesRequest, upstreamModel string) (openai.ChatRequest, requestTranslation) {
	var tr requestTranslation

	chat := openai.ChatRequest{
		Model:                upstreamModel,
		Messages:             buildChatMessages(req),
		Temperature:          req.Temperature,
		TopP:                 req.TopP,
		MaxCompletionTokens:  req.MaxOutputTokens,
		User:                 req.User,
		ParallelToolCalls:    req.ParallelToolCalls,
		Metadata:             cloneRawMessage(req.Metadata),
		Store:                req.Store,
		ServiceTier:          req.ServiceTier,
		PromptCacheKey:       req.PromptCacheKey,
		PromptCacheRetention: req.PromptCacheRetention,
		SafetyIdentifier:     req.SafetyIdentifier,
		Tools:                mapResponsesToolsToChat(req.Tools, &tr),
		ToolChoice:           mapResponsesToolChoice(req.ToolChoice),
	}

	// reasoning.effort → reasoning_effort（summary 不是 ChatRequest 字段，影响是否发 reasoning 事件，见 §6）。
	// Responses reasoning 是 opt-in：缺省/null 表达显式非 reasoning 意图，置 ReasoningDisabled，
	// 由 provider adapter 关闭私有思考模式（DeepSeek 出站 thinking:disabled），避免 Codex effort=none
	// 的非 reasoning run 仍触发上游 thinking（额外成本+CoT，BRIDGE §6）。
	if req.Reasoning == nil {
		chat.ReasoningDisabled = true
	} else if req.Reasoning.Effort != nil {
		chat.ReasoningEffort = req.Reasoning.Effort
	}

	// text.format → response_format；text.verbosity → verbosity。
	if req.Text != nil {
		if rf := mapResponsesTextFormat(req.Text.Format); rf != nil {
			chat.ResponseFormat = rf
		}
		if req.Text.Verbosity != nil {
			chat.Verbosity = req.Text.Verbosity
		}
	}

	recordContractlessDrops(req, &tr)

	return chat, tr
}

// recordContractlessDrops 记录契约无承载字段的桥接层 Drop（BRIDGE §1）。
func recordContractlessDrops(req gatewayapi.ResponsesRequest, tr *requestTranslation) {
	if req.PreviousResponseID != nil {
		tr.drop("previous_response_id")
	}
	if len(req.Include) > 0 {
		tr.drop("include")
	}
	if req.Truncation != nil {
		tr.drop("truncation")
	}
	if req.Background != nil {
		tr.drop("background")
	}
	// 未显式建模的合法顶层字段（如 Codex 专属 client_metadata）：契约无承载，静默 Drop。
	for key := range req.Extensions {
		tr.drop(key)
	}
}

// buildChatMessages 把 instructions + input 组装成 Chat messages（BRIDGE §1 / §2）。
//
// instructions 注入顶部 system；input 是字符串时为单条 user message；input 是 item 数组时按
// §2 规则展开：连续 function_call 合并进同一条 assistant.tool_calls；function_call_output 生成
// 按 call_id 对齐的 tool message。
func buildChatMessages(req gatewayapi.ResponsesRequest) []openai.ChatMessage {
	msgs := make([]openai.ChatMessage, 0, len(req.Input.Items)+1)

	if req.Instructions != nil && strings.TrimSpace(*req.Instructions) != "" {
		msgs = append(msgs, openai.ChatMessage{Role: "system", Content: jsonString(*req.Instructions)})
	}

	if req.Input.Text != nil {
		return append(msgs, openai.ChatMessage{Role: "user", Content: jsonString(*req.Input.Text)})
	}

	// pendingToolCallIdx 指向上一条仅由连续 function_call 累积出的 assistant message，用于并行/连续
	// 工具调用合并；任何非 function_call item 都会打断累积（置 -1）。
	pendingToolCallIdx := -1
	for _, item := range req.Input.Items {
		itemType := item.Type
		if itemType == "" {
			itemType = itemTypeMessage
		}

		switch itemType {
		case itemTypeMessage:
			pendingToolCallIdx = -1
			msgs = append(msgs, buildMessageItem(item))

		case itemTypeFunctionCall:
			toolCall := openai.ChatToolCall{
				ID:   derefString(item.CallID),
				Type: "function",
				Function: openai.ChatToolCallFunction{
					Name:      functionCallName(item),
					Arguments: derefString(item.Arguments),
				},
			}
			if pendingToolCallIdx >= 0 {
				msgs[pendingToolCallIdx].ToolCalls = append(msgs[pendingToolCallIdx].ToolCalls, toolCall)
			} else {
				msgs = append(msgs, openai.ChatMessage{Role: "assistant", ToolCalls: []openai.ChatToolCall{toolCall}})
				pendingToolCallIdx = len(msgs) - 1
			}

		case itemTypeFunctionCallOutput:
			pendingToolCallIdx = -1
			msgs = append(msgs, openai.ChatMessage{
				Role:       "tool",
				ToolCallID: item.CallID,
				Content:    toolOutputContent(item.Output),
			})

		case itemTypeReasoning:
			// 跨轮 reasoning item best-effort：第一版不回灌 prior CoT 给 DeepSeek chat（非标准、易被拒）。
			// 保真度提升见 GAP-11-003 / TASK-11.08。
			pendingToolCallIdx = -1

		case itemTypeItemReference, itemTypeCompaction:
			// 无状态第一版：引用 server-side 历史 item / compaction 历史不还原（GAP-11-001）。
			pendingToolCallIdx = -1

		default:
			pendingToolCallIdx = -1
		}
	}

	return msgs
}

// functionCallName 还原 function_call item 的 Chat 工具名。
//
// MCP namespace 工具回传时带 namespace 字段：拍平为与出站 tools 一致的名称（BRIDGE §3.3 方案 B），
// 保证 function_call 与声明的 tools[] 名称对齐。
func functionCallName(item gatewayapi.ResponseInputItem) string {
	name := derefString(item.Name)
	if item.Namespace != nil && *item.Namespace != "" {
		return joinNamespaceToolName(*item.Namespace, name)
	}
	return name
}

// buildMessageItem 把 message input item 翻译成单条 Chat message。
func buildMessageItem(item gatewayapi.ResponseInputItem) openai.ChatMessage {
	return openai.ChatMessage{
		Role:    item.Role,
		Content: translateInputContent(item.Content),
	}
}

// mapResponsesToolsToChat 把 Responses tools[] 翻译为 Chat 嵌套 function tools（BRIDGE §3.1）。
//
//   - function（扁平）→ 嵌套 function tool。
//   - namespace（Codex MCP 分组）→ 拍平内层 function 工具，名称用 <namespace><name>（方案 B）。
//   - 内置工具（web_search/image_generation/file_search/...）/ custom / local_shell：契约无 function
//     承载或本阶段不消费 → Drop 并记审计（GAP-11-002 / GAP-11-004）。
func mapResponsesToolsToChat(tools []gatewayapi.ResponsesTool, tr *requestTranslation) []openai.ChatTool {
	if len(tools) == 0 {
		return nil
	}

	out := make([]openai.ChatTool, 0, len(tools))
	for _, tool := range tools {
		switch {
		case tool.IsFunction():
			out = append(out, chatFunctionTool(tool.Name, tool.Description, tool.Parameters, tool.Strict))

		case tool.IsNamespace():
			for _, inner := range tool.Tools {
				if !inner.IsFunction() {
					tr.drop("tools." + tool.Name + "." + inner.Type)
					continue
				}
				out = append(out, chatFunctionTool(
					joinNamespaceToolName(tool.Name, inner.Name),
					inner.Description, inner.Parameters, inner.Strict,
				))
			}

		default:
			tr.drop("tools." + tool.Type)
		}
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// chatFunctionTool 构造 Chat 嵌套 function tool；parameters 缺省时补最小 object schema（BRIDGE §3.1）。
func chatFunctionTool(name, description string, parameters json.RawMessage, strict *bool) openai.ChatTool {
	params := cloneRawMessage(parameters)
	if len(params) == 0 {
		params = json.RawMessage(`{"type":"object"}`)
	}
	return openai.ChatTool{
		Type: "function",
		Function: openai.ChatFunctionTool{
			Name:        name,
			Description: description,
			Parameters:  params,
			Strict:      strict,
		},
	}
}

// joinNamespaceToolName 拍平 Codex MCP namespace 工具名（BRIDGE §3.3 方案 B：唯一可逆）。
// Codex namespace 名形如 "mcp__server__"（已含尾分隔符），直接拼接内层名；否则补分隔符。
func joinNamespaceToolName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	if strings.HasSuffix(namespace, namespaceToolSeparator) {
		return namespace + name
	}
	return namespace + namespaceToolSeparator + name
}

// mapResponsesToolChoice 归一 Responses tool_choice 到 Chat tool_choice（BRIDGE §3.2）。
//
// 字符串形态（auto/none/required）原样透传；对象形态按 type 归一。未知形态保守透传原始 JSON。
func mapResponsesToolChoice(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}

	// 字符串形态：auto / none / required 原样。
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return cloneRawMessage(raw)
	}

	var obj struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return cloneRawMessage(raw)
	}

	switch obj.Type {
	case "auto", "allowed_tools":
		return jsonString("auto")
	case "none":
		return jsonString("none")
	case "required", "any", "tool":
		return jsonString("required")
	case "function":
		name := obj.Name
		if name == "" && obj.Function != nil {
			name = obj.Function.Name
		}
		if name == "" {
			return jsonString("required")
		}
		return chatFunctionToolChoice(name)
	default:
		return cloneRawMessage(raw)
	}
}

// chatFunctionToolChoice 构造 Chat 具名 function tool_choice。
func chatFunctionToolChoice(name string) json.RawMessage {
	type fn struct {
		Name string `json:"name"`
	}
	type choice struct {
		Type     string `json:"type"`
		Function fn     `json:"function"`
	}
	out, _ := json.Marshal(choice{Type: "function", Function: fn{Name: name}})
	return out
}

// mapResponsesTextFormat 把 Responses text.format 翻译为 Chat response_format（BRIDGE §1 / §3）。
//
//   - {type:"text"|"json_object"} → 同名 type。
//   - {type:"json_schema", name, schema, strict} → {type:"json_schema", json_schema:{name,schema,strict}}。
//
// json_schema 细节（strict / 嵌套 schema 校验）在 TASK-11.08 进一步收口。
func mapResponsesTextFormat(format json.RawMessage) *openai.ChatResponseFormat {
	if len(format) == 0 {
		return nil
	}

	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(format, &head); err != nil || head.Type == "" {
		return nil
	}

	switch head.Type {
	case "json_schema":
		return &openai.ChatResponseFormat{Type: head.Type, JSONSchema: extractJSONSchema(format)}
	default:
		return &openai.ChatResponseFormat{Type: head.Type}
	}
}

// extractJSONSchema 把 Responses 扁平 json_schema format 的 schema 字段提进 Chat 的 json_schema 对象。
func extractJSONSchema(format json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(format, &fields); err != nil {
		return nil
	}
	delete(fields, "type")
	if len(fields) == 0 {
		return nil
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return nil
	}
	return out
}
