package deepseek

import (
	"encoding/json"

	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// rejectUnsupportedRequest 在调用 DeepSeek 上游前，拒绝 DeepSeek 无法保持语义的请求字段。
//
// 依据 2026-06-01 对 DeepSeek OpenAI endpoint 的黑盒冻结，以下字段 DeepSeek 会直接返回 400。
// 与其把请求透传给上游再拿到无定位信息的上游错误，不如在 adapter 内前置拒绝并给出可定位 param：
//   - response_format.type=json_schema（DeepSeek "This response_format type is unavailable now"）
//   - tools[].type=custom（DeepSeek "unknown variant `custom`, expected `function`"）
//   - n>1（DeepSeek "Invalid n value (currently only n = 1 is supported)"）
func rejectUnsupportedRequest(req openai.ChatRequest) error {
	if req.ResponseFormat != nil && req.ResponseFormat.Type == "json_schema" {
		return unsupportedRequest("response_format", "response_format json_schema is not supported by this model")
	}

	for _, tool := range req.Tools {
		if tool.Type == "custom" {
			return unsupportedRequest("tools", "custom tools are not supported by this model")
		}
	}

	if n, ok := extensionNumber(req.Extensions, "n"); ok && n > 1 {
		return unsupportedRequest("n", "only n=1 is supported by this model")
	}

	// audio 输出与 modalities 含非 text：DeepSeek 黑盒返回 200 但只产出文本，
	// 静默忽略会让客户误以为拿到 audio/多模态输出，因此前置 Reject 而非透传。
	if _, ok := req.Extensions["audio"]; ok {
		return unsupportedRequest("audio", "audio output is not supported by this model")
	}

	if modalities, ok := req.Extensions["modalities"]; ok && hasNonTextModality(modalities) {
		return unsupportedRequest("modalities", "only text modality is supported by this model")
	}

	return nil
}

// hasNonTextModality 判断 modalities 列表是否包含 text 以外的值。
func hasNonTextModality(raw json.RawMessage) bool {
	var modalities []string
	if err := json.Unmarshal(raw, &modalities); err != nil {
		// 结构无法解析时按存在非 text 处理（保守拒绝），交由上层给出可定位错误。
		return true
	}

	for _, modality := range modalities {
		if modality != "text" {
			return true
		}
	}

	return false
}

// unsupportedRequest 构造稳定的"请求字段不被支持"错误；param 用于 HTTP 层定位到具体字段。
func unsupportedRequest(param, message string) error {
	return failure.New(
		failure.CodeAdapterRequestUnsupported,
		failure.WithMessage(message),
		failure.WithField("param", param),
	)
}

// extensionNumber 从 vendor 扩展里读取数值（如 n）；缺失或非数值返回 (0,false)。
func extensionNumber(extensions map[string]json.RawMessage, key string) (float64, bool) {
	raw, ok := extensions[key]
	if !ok {
		return 0, false
	}

	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}

	return value, true
}
