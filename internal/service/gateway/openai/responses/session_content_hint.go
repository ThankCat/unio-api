package responses

import (
	"github.com/ThankCat/unio-gateway/internal/core/sessionhint"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/openai/responses/dto"
)

// responsesContentHint 在显式会话信号缺失时以请求前缀哈希兜底会话身份（第三节内容派生兜底）。
// instructions + input 原文前缀共同构成身份：同一会话的多轮请求共享同样的开头。
func responsesContentHint(req dto.ResponsesRequest) sessionhint.Hint {
	parts := make([]string, 0, 2)
	if req.Instructions != nil && *req.Instructions != "" {
		parts = append(parts, *req.Instructions)
	}
	if len(req.Input.Raw) > 0 {
		parts = append(parts, string(req.Input.Raw))
	}
	return sessionhint.ContentDerivedHint(parts...)
}
