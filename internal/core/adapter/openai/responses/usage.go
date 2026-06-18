package responses

import (
	"sort"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/core/usage"
)

// usageMappingVersionResponses 标记 Responses usage→facts 映射规则版本，用于历史账务复算与回归。
const usageMappingVersionResponses = "openai.responses.v1"

// Responses 终态响应 output 数组里「工具调用类」item 的 type → 协议无关能力 key 映射。
//
// 这是 tools.* 强证据的精确来源：响应里出现某个工具调用 item，才证明该能力真被用到（区别于请求侧
// 仅声明工具）。内置工具调用 item（web_search_call 等）也如实记录用于审计；校正侧是否据此作强证据由
// calibration 的强证据键集合裁定，adapter 只忠实埋点。
var responsesOutputToolCapability = map[string]capability.Key{
	"function_call":         capability.KeyToolsFunction,
	"custom_tool_call":      capability.KeyToolsCustom,
	"web_search_call":       capability.KeyToolsBuiltinWebSearch,
	"file_search_call":      capability.KeyToolsBuiltinFileSearch,
	"code_interpreter_call": capability.KeyToolsBuiltinCodeInterpreter,
	"computer_call":         capability.KeyToolsBuiltinComputerUse,
	"image_generation_call": capability.KeyToolsBuiltinImageGeneration,
	"mcp_call":              capability.KeyToolsBuiltinMCP,
}

// wireResponse 是上游 /responses 响应体（及流式终态事件内 response 对象）中 adapter 关心的最小子集。
//
// 只解析账务/审计需要的字段；其余字段对 adapter 透明，由 Raw / Data 原文透传给客户（零转换）。
type wireResponse struct {
	ID                string                 `json:"id"`
	Model             string                 `json:"model"`
	Status            string                 `json:"status"`
	Usage             *wireUsage             `json:"usage"`
	IncompleteDetails *wireIncompleteDetails `json:"incomplete_details"`
	Error             *wireError             `json:"error"`
	Output            []wireOutputItem       `json:"output"`
}

// wireOutputItem 是终态响应 output 数组里的一个输出项；只解析 type 用于工具命中埋点。
//
// 其余字段（content/arguments 等）对 adapter 透明，由 Raw / Data 原文透传给客户（零转换）。
type wireOutputItem struct {
	Type string `json:"type"`
}

// wireUsage 是上游 Responses usage 对象。
type wireUsage struct {
	InputTokens         int64                  `json:"input_tokens"`
	OutputTokens        int64                  `json:"output_tokens"`
	TotalTokens         int64                  `json:"total_tokens"`
	InputTokensDetails  *wireInputTokenDetail  `json:"input_tokens_details"`
	OutputTokensDetails *wireOutputTokenDetail `json:"output_tokens_details"`
}

type wireInputTokenDetail struct {
	CachedTokens int64 `json:"cached_tokens"`
}

type wireOutputTokenDetail struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

type wireIncompleteDetails struct {
	Reason string `json:"reason"`
}

// wireError 是上游 Responses 原生 error 对象（response.error / 顶层 error）。
type wireError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// chatUsageFromWire 把上游 Responses usage 归一为 adapter 内部 usage DTO。
//
// 与桥接侧 mapResponsesUsage 反向：input/output/total + cached/reasoning 分解项一一对应。
// usage 为 nil 时返回 (零值, false)，由调用方按「缺失 usage」处理，绝不当成 0 元请求。
func chatUsageFromWire(u *wireUsage) (adapter.ChatUsage, bool) {
	if u == nil {
		return adapter.ChatUsage{}, false
	}

	result := adapter.ChatUsage{
		PromptTokens:     int(u.InputTokens),
		CompletionTokens: int(u.OutputTokens),
		TotalTokens:      int(u.TotalTokens),
	}
	if u.InputTokensDetails != nil {
		result.CachedTokens = int(u.InputTokensDetails.CachedTokens)
	}
	if u.OutputTokensDetails != nil {
		result.ReasoningTokens = int(u.OutputTokensDetails.ReasoningTokens)
	}
	return result, true
}

// responsesFinishClass 把 Responses status + incomplete 原因映射为协议无关的稳定 FinishClass。
//
// completed → stop；incomplete 按 reason 分流（max_output_tokens→length，content_filter→content_filter）；
// 其余归 other。原始 finish 语义串另存 FinishFacts.RawReason 供审计。
func responsesFinishClass(status, incompleteReason string) adapter.FinishClass {
	switch status {
	case "completed":
		return adapter.FinishStop
	case "incomplete":
		switch incompleteReason {
		case "max_output_tokens":
			return adapter.FinishLength
		case "content_filter":
			return adapter.FinishContentFilter
		default:
			return adapter.FinishOther
		}
	default:
		return adapter.FinishOther
	}
}

// responsesRawFinish 构造写入 FinishFacts.RawReason 的原始 finish 语义串。
func responsesRawFinish(status, incompleteReason string) string {
	if status == "incomplete" && incompleteReason != "" {
		return incompleteReason
	}
	return status
}

// usedCapabilitiesFromOutput 从终态响应 output 解析「真正被调用」的工具能力 key（升序去重）。
//
// 每个工具调用类 output item（function_call / custom_tool_call / web_search_call 等）映射到对应能力
// key；当单次响应出现 ≥2 个客户工具调用（function/custom）时追加 tools.parallel（并行工具调用强证据）。
// 返回 nil 表示本次响应未命中任何工具，由校正侧回退 finish_class（粗粒度）。
func usedCapabilitiesFromOutput(output []wireOutputItem) []string {
	if len(output) == 0 {
		return nil
	}

	hit := make(map[capability.Key]struct{})
	clientToolCalls := 0
	for _, item := range output {
		key, ok := responsesOutputToolCapability[item.Type]
		if !ok {
			continue
		}
		hit[key] = struct{}{}
		if key == capability.KeyToolsFunction || key == capability.KeyToolsCustom {
			clientToolCalls++
		}
	}
	if clientToolCalls >= 2 {
		hit[capability.KeyToolsParallel] = struct{}{}
	}
	if len(hit) == 0 {
		return nil
	}

	out := make([]string, 0, len(hit))
	for k := range hit {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

// responsesFacts 用 Responses 直传语义构造协议无关的 ResponseFacts。
func responsesFacts(resp wireResponse, u adapter.ChatUsage, meta adapter.UpstreamMetadata, source usage.Source) adapter.ResponseFacts {
	incompleteReason := ""
	if resp.IncompleteDetails != nil {
		incompleteReason = resp.IncompleteDetails.Reason
	}
	return adapter.ResponseFacts{
		UpstreamProtocol:   "openai",
		UpstreamResponseID: resp.ID,
		UpstreamModel:      resp.Model,
		Finish: adapter.FinishFacts{
			Class:     responsesFinishClass(resp.Status, incompleteReason),
			RawReason: responsesRawFinish(resp.Status, incompleteReason),
		},
		Usage:               u.ToUsageFacts(),
		UsageSource:         source,
		UsageMappingVersion: usageMappingVersionResponses,
		Metadata:            meta,
		UsedCapabilities:    usedCapabilitiesFromOutput(resp.Output),
	}
}
