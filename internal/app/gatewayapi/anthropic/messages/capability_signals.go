package messages

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/ThankCat/unio-api/internal/core/capability"
)

// RequiredCapabilities 推断本次 Anthropic Messages 请求所需的能力集，供 routing capability 闸门消费。
//
// 它是 app 层抽取协议信号 + core 层 capability.Infer 的稳定入口；service 在调用 routing 前调用，
// 把结果挂到 ChatRouteRequest，避免在 service 重复协议解析。
func RequiredCapabilities(req MessageRequest) capability.Set {
	return capability.Infer(capabilitySignals(req))
}

// RequestLimits 抽取本次请求的「带值」能力约束，供 routing capability 闸门判定 limited 超限。
//
// Anthropic 用 thinking budget 而非 effort 档位，当前无「带值」约束可抽取，恒返回零值；
// 保留导出以与 OpenAI 协议族 service 调用点保持一致调用形态。
func RequestLimits(req MessageRequest) capability.RequestLimits {
	return capability.InferLimits(capabilitySignals(req))
}

// capabilitySignals 从 Anthropic Messages 请求 DTO 抽取协议无关的 capability 信号。
//
// 只读输入、不改请求。Anthropic 与 OpenAI 是独立协议族，能力语义映射差异：
//   - thinking 启用/adaptive → reasoning.budget（Anthropic 用思考预算，不用 effort 档位）；
//   - custom（客户 function）工具 → tools.function；server tool 按 type 前缀映射内置工具；
//   - tool_choice any|tool → tools.choice_required（强制至少调用一个工具）；
//   - 无 include_usage 概念，流式 usage 始终随 message_delta 返回，不推断 stream.usage。
//
// 未建模/未识别字段不置位任何信号，交给 capability.Infer 得到 required capability 集合。
func capabilitySignals(req MessageRequest) capability.RequestSignals {
	signals := capability.RequestSignals{}

	if req.IsStream() {
		signals.Stream = true
	}

	for _, msg := range req.Messages {
		image, file := scanContentBlockModalities(msg.Content)
		signals.HasImageInput = signals.HasImageInput || image
		signals.HasFileInput = signals.HasFileInput || file
	}

	if thinkingEnabled(req.Thinking) {
		signals.ReasoningBudget = true
	}

	scanTools(req.Tools, &signals)

	if toolChoiceRequired(req.ToolChoice) {
		signals.ToolChoiceRequired = true
	}

	return signals
}

// scanContentBlockModalities 解析 content block 数组，识别图片/文件输入。
//
// content 为 string 形态或解析失败时不识别任何模态；document block 视为文件输入。
func scanContentBlockModalities(raw json.RawMessage) (image, file bool) {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 || data[0] != '[' {
		return false, false
	}

	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &blocks); err != nil {
		return false, false
	}

	for _, block := range blocks {
		switch block.Type {
		case "image":
			image = true
		case "document":
			file = true
		}
	}

	return image, file
}

// thinkingEnabled 判断 thinking 配置是否开启（enabled / adaptive）。
func thinkingEnabled(raw json.RawMessage) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		return false
	}

	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return false
	}

	return head.Type == "enabled" || head.Type == "adaptive"
}

// scanTools 解析 tools union，把客户工具与内置 server tool 映射成能力信号。
//
// 客户工具（type 缺省或 "custom"）映射为 function 工具；server tool 按 type 前缀映射到
// 已注册的内置能力 key；未建模 server tool（bash/text_editor/memory/tool_search/web_fetch）
// 既非 function 也无对应 v1 key，不置位（observe 阶段不为未建模能力造 cap）。
func scanTools(raw json.RawMessage, signals *capability.RequestSignals) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return
	}

	var tools []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		return
	}

	for _, tool := range tools {
		switch {
		case tool.Type == "" || tool.Type == "custom":
			signals.HasFunctionTool = true
		case strings.HasPrefix(tool.Type, "web_search"):
			signals.BuiltinWebSearch = true
		case strings.HasPrefix(tool.Type, "code_execution"):
			signals.BuiltinCodeInterpreter = true
		}
	}
}

// toolChoiceRequired 判断 tool_choice 是否强制调用工具（type 为 any 或 tool）。
func toolChoiceRequired(raw json.RawMessage) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		return false
	}

	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return false
	}

	return head.Type == "any" || head.Type == "tool"
}
