package responses

import (
	"encoding/json"
	"strings"

	"github.com/ThankCat/unio-api/internal/core/capability"
)

// Responses 内置工具与 custom 工具的 type 值（function/namespace 已在 tools.go 定义）。
const (
	toolTypeCustom            = "custom"
	toolTypeWebSearch         = "web_search"
	toolTypeWebSearchPreview  = "web_search_preview"
	toolTypeFileSearch        = "file_search"
	toolTypeCodeInterpreter   = "code_interpreter"
	toolTypeComputerUse       = "computer_use"
	toolTypeComputerPreview   = "computer_use_preview"
	toolTypeImageGeneration   = "image_generation"
	toolTypeMCP               = "mcp"
	includeEncryptedContentID = "encrypted_content"
)

// RequiredCapabilities 推断本次 OpenAI Responses 请求所需的能力集，供 routing capability 闸门消费。
//
// 它是 app 层抽取协议信号 + core 层 capability.Infer 的稳定入口；service 在调用 routing 前调用，
// 把结果挂到 ChatRouteRequest，避免在 service 重复协议解析。
func RequiredCapabilities(req ResponsesRequest) capability.Set {
	return capability.Infer(capabilitySignals(req))
}

// RequestLimits 抽取本次请求的「带值」能力约束（如 reasoning.effort 档位），供 routing capability 闸门判定 limited 超限。
//
// 与 RequiredCapabilities 同源（共用 capabilitySignals），service 在调用 routing 前一并取出挂到 ChatRouteRequest。
func RequestLimits(req ResponsesRequest) capability.RequestLimits {
	return capability.InferLimits(capabilitySignals(req))
}

// capabilitySignals 从 OpenAI Responses 请求 DTO 抽取协议无关的 capability 信号。
//
// 只读输入、不改请求。Responses 与 Chat 字段形态差异：input 是 item 数组（含 message
// content parts），工具是扁平形态且含 Codex 专属 namespace（MCP）分组。
// 内置工具/有状态字段即便本阶段 adapter 出站 Drop（GAP-11-001/004），仍按客户请求如实推断，
// 供 observe 阶段闸门与审计观察。未建模/未识别字段不置位任何信号。
func capabilitySignals(req ResponsesRequest) capability.RequestSignals {
	signals := capability.RequestSignals{}

	if req.StreamEnabled() {
		signals.Stream = true
	}

	for _, item := range req.Input.Items {
		image, audio, file := scanItemModalities(item)
		signals.HasImageInput = signals.HasImageInput || image
		signals.HasAudioInput = signals.HasAudioInput || audio
		signals.HasFileInput = signals.HasFileInput || file
		if item.EncryptedContent != nil && strings.TrimSpace(*item.EncryptedContent) != "" {
			signals.EncryptedContent = true
		}
	}
	if includeRequestsEncryptedContent(req.Include) {
		signals.EncryptedContent = true
	}

	scanTools(req.Tools, &signals)
	if req.ParallelToolCalls != nil && *req.ParallelToolCalls {
		signals.ParallelToolCalls = true
	}
	if toolChoiceRequired(req.ToolChoice) {
		signals.ToolChoiceRequired = true
	}

	if req.Reasoning != nil {
		if req.Reasoning.Effort != nil {
			effort := strings.TrimSpace(*req.Reasoning.Effort)
			if effort != "" && !strings.EqualFold(effort, "none") {
				signals.ReasoningEffort = true
				signals.ReasoningEffortLevel = effort
			}
		}
		if req.Reasoning.Summary != nil && strings.TrimSpace(*req.Reasoning.Summary) != "" {
			signals.ReasoningSummary = true
		}
	}

	if req.Text != nil {
		switch textFormatType(req.Text.Format) {
		case "json_object":
			signals.ResponseFormatJSONObject = true
		case "json_schema":
			signals.ResponseFormatJSONSchema = true
		}
	}

	if req.PromptCacheKey != nil || req.PromptCacheRetention != nil {
		signals.PromptCache = true
	}
	if req.ServiceTier != nil && strings.TrimSpace(*req.ServiceTier) != "" {
		signals.ServiceTier = true
	}
	if req.Store != nil && *req.Store {
		signals.ServerStateStore = true
	}
	if req.Background != nil && *req.Background {
		signals.ServerStateBackground = true
	}

	return signals
}

// scanItemModalities 解析 input item 的 content union，识别多模态输入 part。
func scanItemModalities(item ResponseInputItem) (image, audio, file bool) {
	if len(item.Content) == 0 {
		return false, false, false
	}

	var parts []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(item.Content, &parts); err != nil {
		return false, false, false
	}

	for _, part := range parts {
		switch part.Type {
		case contentPartInputImage:
			image = true
		case contentPartInputAudio:
			audio = true
		case contentPartInputFile:
			file = true
		}
	}

	return image, audio, file
}

// includeRequestsEncryptedContent 判断 include 是否请求推理项 encrypted_content（如 reasoning.encrypted_content）。
func includeRequestsEncryptedContent(include []string) bool {
	for _, entry := range include {
		if strings.Contains(entry, includeEncryptedContentID) {
			return true
		}
	}

	return false
}

// scanTools 把 Responses 工具映射成能力信号。
//
// function → tools.function；custom → tools.custom；namespace（Codex MCP 分组）与 mcp → 内置 MCP；
// 其余内置工具按 type 映射到对应 builtin 能力 key。未识别 type 不置位。
func scanTools(tools []ResponsesTool, signals *capability.RequestSignals) {
	for _, tool := range tools {
		switch tool.Type {
		case toolTypeFunction:
			signals.HasFunctionTool = true
		case toolTypeCustom:
			signals.HasCustomTool = true
		case toolTypeNamespace, toolTypeMCP:
			signals.BuiltinMCP = true
		case toolTypeWebSearch, toolTypeWebSearchPreview:
			signals.BuiltinWebSearch = true
		case toolTypeFileSearch:
			signals.BuiltinFileSearch = true
		case toolTypeCodeInterpreter:
			signals.BuiltinCodeInterpreter = true
		case toolTypeComputerUse, toolTypeComputerPreview:
			signals.BuiltinComputerUse = true
		case toolTypeImageGeneration:
			signals.BuiltinImageGeneration = true
		}
	}
}

// toolChoiceRequired 判断 tool_choice 是否强制至少调用一个工具。
//
// 字符串形态 "required" 直接命中；对象形态 {"type":"required"|"any"|"tool"} 与 mapResponsesRequestToChat
// 的 tool_choice 归一（→ Chat "required"）保持一致，避免对象形态的强制工具请求漏置 ToolChoiceRequired 信号，
// 进而让 capability 闸门判定与真实出站行为不一致（observe 审计偏差 / enforce 误判）。
func toolChoiceRequired(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}

	var choice string
	if err := json.Unmarshal(raw, &choice); err == nil {
		return choice == "required"
	}

	var obj struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	switch obj.Type {
	case "required", "any", "tool":
		return true
	default:
		return false
	}
}

// textFormatType 解析 text.format 对象的 type 字段。
func textFormatType(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return ""
	}

	return head.Type
}
