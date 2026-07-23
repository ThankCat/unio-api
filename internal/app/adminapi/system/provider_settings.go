package system

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	"github.com/go-chi/chi/v5"
)

// ProviderSettingsService 定义 adminapi 读写全局运行时配置所需的最小能力。
// 通用 List/SetRaw 驱动配置面板;beta 专用方法为便捷的 typed 入口。
type ProviderSettingsService interface {
	List(ctx context.Context) []appsettings.SettingItem
	SetRawWithResult(ctx context.Context, key string, value json.RawMessage) (appsettings.SettingWriteResult, error)
	GetAnthropicBetaPolicy(ctx context.Context) messagesadapter.BetaPolicy
	SetAnthropicBetaPolicy(ctx context.Context, policy messagesadapter.BetaPolicy) error
}

// settingItemDTO 是通用配置项响应:元数据 + 当前生效值 + 生效来源(redis/db/default)。
// 因 Redis 是跨进程实时源,此处的 value/source 即 gateway 将读到的值,可据此验证配置已传播。
type settingItemDTO struct {
	Key         string          `json:"key"`
	Category    string          `json:"category"`
	Label       string          `json:"label"`
	Description string          `json:"description"`
	HotReload   bool            `json:"hot_reload"`
	Default     json.RawMessage `json:"default"`
	Value       json.RawMessage `json:"value"`
	Source      string          `json:"source"`
	Revision    int64           `json:"revision"`

	RuntimeActiveRevision  int64  `json:"runtime_active_revision,omitempty"`
	RuntimePendingRevision int64  `json:"runtime_pending_revision,omitempty"`
	RuntimeSyncState       string `json:"runtime_sync_state,omitempty"`
}

func (h *providerSettingsHandler) listSettings(w http.ResponseWriter, r *http.Request) {
	items := h.service.List(r.Context())
	dtos := make([]settingItemDTO, 0, len(items))
	for _, it := range items {
		dtos = append(dtos, settingItemDTO{
			Key:         it.Key,
			Category:    it.Category,
			Label:       it.Label,
			Description: it.Description,
			HotReload:   it.HotReload,
			Default:     it.Default,
			Value:       it.Value,
			Source:      it.Source,
			Revision:    it.Revision,

			RuntimeActiveRevision:  it.RuntimeActiveRevision,
			RuntimePendingRevision: it.RuntimePendingRevision,
			RuntimeSyncState:       it.RuntimeSyncState,
		})
	}
	adminhttp.WriteData(w, http.StatusOK, dtos)
}

func (h *providerSettingsHandler) putSetting(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	var value json.RawMessage
	if err := httpx.DecodeJSON(w, r, &value); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := h.service.SetRawWithResult(r.Context(), key, value)
	if err != nil {
		if failure.CodeOf(err) != "" {
			adminhttp.WriteServiceError(w, err)
			return
		}
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage(err.Error()),
			failure.WithField("field", "value"),
		))
		return
	}
	adminhttp.WriteData(w, http.StatusOK, result)
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
	policy := h.service.GetAnthropicBetaPolicy(r.Context())
	adminhttp.WriteData(w, http.StatusOK, toAnthropicBetaPolicyDTO(policy))
}

func (h *providerSettingsHandler) putAnthropicBeta(w http.ResponseWriter, r *http.Request) {
	var req anthropicBetaPolicyDTO
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	policy := messagesadapter.BetaPolicy{
		Mode: messagesadapter.BetaMode(req.Mode),
		List: normalizeBetaList(req.List),
	}

	if err := appsettings.ValidateBetaPolicy(policy); err != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage(err.Error()),
			failure.WithField("field", "beta_policy"),
		))
		return
	}

	if err := h.service.SetAnthropicBetaPolicy(r.Context(), policy); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toAnthropicBetaPolicyDTO(policy))
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
