package chatcompletions

import (
	"encoding/json"
	"strings"
)

// 权威首字判定（Chat Completions）。
//
// 判定必须是 chunk 的纯函数，而不是解析时写进 chunk 的字段：字段版本要求每个构造 chunk 的地方
// 都记得盖章，一旦漏盖，chunk 会被判为「非首字」并被无限暂存在首字前缓冲里，客户最终收到空流——
// 失败模式是静默丢内容，而不是少一个指标。纯函数让「谁消费谁计算」，漏不掉。

type firstTokenToolCall struct {
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type firstTokenFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// FirstTokenPayload 返回 chunk 中会改变最终模型输出的生成负载。
//
// 返回非空即「算首字」，同时它就是 partial settlement 估算 output token 的可见文本。
// 长度大于零即有效，空格等真实生成字符也算，因此绝不对结果做 TrimSpace。
//
// 算首字：非空 content / reasoning_content / refusal / 工具或函数的名称与参数。
// 不算首字：role-only、纯 ID/model/index、logprobs-only、usage-only、finish-only、空 delta。
func FirstTokenPayload(chunk ChatStreamChunk) string {
	var payload strings.Builder
	payload.WriteString(chunk.Content)
	if chunk.ReasoningContent != nil {
		payload.WriteString(*chunk.ReasoningContent)
	}
	if chunk.Refusal != nil {
		payload.WriteString(*chunk.Refusal)
	}

	var toolCalls []firstTokenToolCall
	if len(chunk.ToolCalls) > 0 && json.Unmarshal(chunk.ToolCalls, &toolCalls) == nil {
		for _, toolCall := range toolCalls {
			payload.WriteString(toolCall.Function.Name)
			payload.WriteString(toolCall.Function.Arguments)
		}
	}

	var functionCall firstTokenFunctionCall
	if len(chunk.FunctionCall) > 0 && json.Unmarshal(chunk.FunctionCall, &functionCall) == nil {
		payload.WriteString(functionCall.Name)
		payload.WriteString(functionCall.Arguments)
	}

	return payload.String()
}
