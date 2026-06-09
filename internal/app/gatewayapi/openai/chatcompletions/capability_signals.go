package chatcompletions

import (
	"encoding/json"
	"strings"

	"github.com/ThankCat/unio-api/internal/core/capability"
)

// chatToolTypeCustom 是 OpenAI Chat Completions custom 工具的 type 值（grammar/apply_patch 等）。
const chatToolTypeCustom = "custom"

// RequiredCapabilities 推断本次 OpenAI Chat 请求所需的能力集，供 routing capability 闸门消费。
//
// 它是 app 层抽取协议信号 + core 层 capability.Infer 的稳定入口；service 在调用 routing 前调用，
// 把结果挂到 ChatRouteRequest，避免在 service 重复协议解析。
func RequiredCapabilities(req ChatCompletionRequest) capability.Set {
	return capability.Infer(capabilitySignals(req))
}

// RequestLimits 抽取本次请求的「带值」能力约束（如 reasoning.effort 档位），供 routing capability 闸门判定 limited 超限。
//
// 与 RequiredCapabilities 同源（共用 capabilitySignals），service 在调用 routing 前一并取出挂到 ChatRouteRequest。
func RequestLimits(req ChatCompletionRequest) capability.RequestLimits {
	return capability.InferLimits(capabilitySignals(req))
}

// capabilitySignals 从 OpenAI Chat 请求 DTO 抽取协议无关的 capability 信号。
//
// 只读输入、不改请求；多模态识别复用 content part union 解析，未知/未建模字段不置位任何信号。
// 把信号交给 capability.Infer 得到 required capability 集合（见 core/capability/inference.go）。
func capabilitySignals(req ChatCompletionRequest) capability.RequestSignals {
	signals := capability.RequestSignals{}

	if req.Stream != nil && *req.Stream {
		signals.Stream = true
	}
	if req.StreamIncludeUsage() {
		signals.StreamUsage = true
	}

	for _, msg := range req.Messages {
		image, audio, file := scanContentModalities(msg.Content)
		signals.HasImageInput = signals.HasImageInput || image
		signals.HasAudioInput = signals.HasAudioInput || audio
		signals.HasFileInput = signals.HasFileInput || file
	}

	for _, modality := range req.Modalities {
		if strings.EqualFold(strings.TrimSpace(modality), "audio") {
			signals.AudioOutput = true
		}
	}

	for _, tool := range req.Tools {
		switch tool.Type {
		case "", "function":
			// OpenAI Chat 工具默认且主要是 function；type 省略按 function 处理。
			signals.HasFunctionTool = true
		case chatToolTypeCustom:
			signals.HasCustomTool = true
		}
	}
	if req.ParallelToolCalls != nil && *req.ParallelToolCalls {
		signals.ParallelToolCalls = true
	}
	if toolChoiceRequired(req.ToolChoice) {
		signals.ToolChoiceRequired = true
	}
	if len(req.WebSearchOptions) > 0 {
		// Chat Completions 内置联网搜索通过 web_search_options 字段触发，而非 tools[]。
		signals.BuiltinWebSearch = true
	}

	if req.ReasoningEffort != nil {
		if effort := strings.TrimSpace(*req.ReasoningEffort); effort != "" {
			signals.ReasoningEffort = true
			signals.ReasoningEffortLevel = effort
		}
	}

	if req.ResponseFormat != nil {
		switch req.ResponseFormat.Type {
		case "json_object":
			signals.ResponseFormatJSONObject = true
		case "json_schema":
			signals.ResponseFormatJSONSchema = true
		}
	}

	if req.PromptCacheKey != nil || req.PromptCacheRetention != nil {
		signals.PromptCache = true
	}
	if req.Logprobs != nil && *req.Logprobs {
		signals.Logprobs = true
	}
	if req.ServiceTier != nil && strings.TrimSpace(*req.ServiceTier) != "" {
		signals.ServiceTier = true
	}
	if req.Store != nil && *req.Store {
		signals.ServerStateStore = true
	}

	return signals
}

// scanContentModalities 解析 message content union，识别多模态输入 part。
//
// content 为 string 形态或解析失败时不识别任何模态；只认已建模的多模态 part type。
func scanContentModalities(content json.RawMessage) (image, audio, file bool) {
	if len(content) == 0 {
		return false, false, false
	}

	var parts []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return false, false, false
	}

	for _, part := range parts {
		switch part.Type {
		case contentPartTypeImageURL:
			image = true
		case contentPartTypeInputAudio:
			audio = true
		case contentPartTypeFile:
			file = true
		}
	}

	return image, audio, file
}

// toolChoiceRequired 判断 tool_choice 是否为字符串 "required"（强制调用工具）。
//
// tool_choice union 还有 none/auto 字符串与 {type:"function",...} 对象形态，
// 这些都不视为 required。
func toolChoiceRequired(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}

	var choice string
	if err := json.Unmarshal(raw, &choice); err != nil {
		return false
	}

	return choice == "required"
}
