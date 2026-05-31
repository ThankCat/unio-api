package gatewayapi

import "encoding/json"

// MarshalJSON 支持 OpenAI include_usage 中间 chunk 的 usage: null 语义。
func (r ChatCompletionStreamResponse) MarshalJSON() ([]byte, error) {
	type alias ChatCompletionStreamResponse
	aux := alias(r)

	if !r.EmitUsageAsNull {
		return json.Marshal(aux)
	}

	raw, err := json.Marshal(aux)
	if err != nil {
		return nil, err
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}

	obj["usage"] = json.RawMessage("null")
	return json.Marshal(obj)
}
