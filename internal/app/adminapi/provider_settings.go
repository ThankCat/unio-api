package adminapi

import (
	"context"
	"net/http"

	messagesadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/appsettings"
)

// ProviderSettingsService 定义 adminapi 读写全局 provider 设置所需的最小能力。
// 起步只有 Anthropic beta 转发策略;将来 OpenAI/Gemini 各自的配置在此扩方法即可。
type ProviderSettingsService interface {
	GetAnthropicBetaPolicy(ctx context.Context) (messagesadapter.BetaPolicy, error)
	SetAnthropicBetaPolicy(ctx context.Context, policy messagesadapter.BetaPolicy) error
}

// anthropicBetaPolicyDTO 是 Anthropic beta 策略的 admin API 请求/响应体。
//
// mode: passthrough(全透传)/ filter(黑名单)/ whitelist(白名单)。
// list: filter 模式当黑名单、whitelist 模式当白名单;passthrough 忽略。
type anthropicBetaPolicyDTO struct {
	Mode string   `json:"mode"`
	List []string `json:"list"`
}

type providerSettingsHandler struct {
	service ProviderSettingsService
}

func (h *providerSettingsHandler) getAnthropicBeta(w http.ResponseWriter, r *http.Request) {
	policy, err := h.service.GetAnthropicBetaPolicy(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, toAnthropicBetaPolicyDTO(policy))
}

func (h *providerSettingsHandler) putAnthropicBeta(w http.ResponseWriter, r *http.Request) {
	var req anthropicBetaPolicyDTO
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	policy := messagesadapter.BetaPolicy{
		Mode: messagesadapter.BetaMode(req.Mode),
		List: normalizeBetaList(req.List),
	}

	if err := appsettings.ValidateBetaPolicy(policy); err != nil {
		writeServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage(err.Error()),
			failure.WithField("field", "beta_policy"),
		))
		return
	}

	if err := h.service.SetAnthropicBetaPolicy(r.Context(), policy); err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toAnthropicBetaPolicyDTO(policy))
}

func toAnthropicBetaPolicyDTO(policy messagesadapter.BetaPolicy) anthropicBetaPolicyDTO {
	list := policy.List
	if list == nil {
		list = []string{}
	}
	return anthropicBetaPolicyDTO{Mode: string(policy.Mode), List: list}
}

// normalizeBetaList 去除空白项(前端标签输入可能留空),保持顺序。
func normalizeBetaList(list []string) []string {
	out := make([]string, 0, len(list))
	for _, b := range list {
		if b == "" {
			continue
		}
		out = append(out, b)
	}
	return out
}
