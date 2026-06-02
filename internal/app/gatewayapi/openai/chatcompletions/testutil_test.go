package chatcompletions

import "encoding/json"

// jsonContent 把字符串编码为 OpenAI chat completion content 字段使用的 json.RawMessage。
// 仅测试使用，避免每个 case 重复 json.Marshal 样板。
func jsonContent(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}
