package chatcompletions

import (
	"encoding/json"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/core/sessionhint"
)

// chatContentHint 在显式会话信号（prompt_cache_key / session-id 头）缺失时，
// 以消息前缀的定长哈希兜底会话身份（第三节内容派生兜底）。
// 只取前两条消息：多轮对话共享同一开头（system + 首条用户消息），取更多只会让
// 「同会话追加了新消息」的请求彼此分裂。哈希在 sessionhint 内完成，原文不出本函数。
func chatContentHint(req gatewayapi.ChatCompletionRequest) sessionhint.Hint {
	parts := make([]string, 0, 2)
	for index, message := range req.Messages {
		if index >= 2 {
			break
		}
		raw, err := json.Marshal(message)
		if err != nil {
			return sessionhint.Hint{}
		}
		parts = append(parts, string(raw))
	}
	return sessionhint.ContentDerivedHint(parts...)
}
