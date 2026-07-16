package appsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
)

// AnthropicBetaPolicyKey 是 app_settings 中 Anthropic beta 策略的 key。
const AnthropicBetaPolicyKey = "anthropic.beta_policy"

const anthropicBetaPolicyDescription = "Anthropic anthropic-beta 头转发策略。" +
	"mode=passthrough 全透传 / filter 黑名单(默认,仅拦 list 内) / whitelist 白名单(仅转 list 内)。" +
	"改后 gateway 秒级生效,无需重启。"

// betaPolicyDoc 是 beta 策略的持久化 JSON 形状。
type betaPolicyDoc struct {
	Mode string   `json:"mode"`
	List []string `json:"list"`
}

// betaPolicyDefinition 是 beta 策略在配置注册表中的登记项。
func betaPolicyDefinition() Definition {
	def, _ := encodeBetaPolicy(messagesadapter.DefaultBetaPolicy())
	return Definition{
		Key:         AnthropicBetaPolicyKey,
		Category:    "anthropic",
		Label:       "Anthropic beta 头转发策略",
		Description: anthropicBetaPolicyDescription,
		HotReload:   true,
		Default:     def,
		Validate:    validateBetaPolicyRaw,
	}
}

// ValidateBetaPolicy 校验一份 beta 策略是否合法。
func ValidateBetaPolicy(policy messagesadapter.BetaPolicy) error {
	switch policy.Mode {
	case messagesadapter.BetaModePassthrough,
		messagesadapter.BetaModeFilter,
		messagesadapter.BetaModeWhitelist:
	default:
		return fmt.Errorf("invalid beta mode %q (want passthrough|filter|whitelist)", policy.Mode)
	}
	for _, b := range policy.List {
		if b == "" {
			return errors.New("beta list must not contain empty token")
		}
	}
	return nil
}

func validateBetaPolicyRaw(raw json.RawMessage) error {
	_, err := decodeBetaPolicy(raw)
	return err
}

func encodeBetaPolicy(policy messagesadapter.BetaPolicy) (json.RawMessage, error) {
	list := policy.List
	if list == nil {
		list = []string{}
	}
	return json.Marshal(betaPolicyDoc{Mode: string(policy.Mode), List: list})
}

func decodeBetaPolicy(raw []byte) (messagesadapter.BetaPolicy, error) {
	var doc betaPolicyDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return messagesadapter.BetaPolicy{}, err
	}
	policy := messagesadapter.BetaPolicy{
		Mode: messagesadapter.BetaMode(doc.Mode),
		List: doc.List,
	}
	if err := ValidateBetaPolicy(policy); err != nil {
		return messagesadapter.BetaPolicy{}, err
	}
	return policy, nil
}

// BetaPolicyProvider 用 SettingsStore 实现 messagesadapter.BetaPolicyProvider(gateway 注入 adapter)。
type BetaPolicyProvider struct {
	store *SettingsStore
}

// NewBetaPolicyProvider 基于配置中枢创建 beta 策略 provider。
func NewBetaPolicyProvider(store *SettingsStore) *BetaPolicyProvider {
	return &BetaPolicyProvider{store: store}
}

// BetaPolicy 返回当前生效策略(经 SettingsStore 的本地/Redis/DB 多层读取,失败回退默认)。
func (p *BetaPolicyProvider) BetaPolicy(ctx context.Context) messagesadapter.BetaPolicy {
	raw := p.store.Raw(ctx, AnthropicBetaPolicyKey)
	if len(raw) == 0 {
		return messagesadapter.DefaultBetaPolicy()
	}
	policy, err := decodeBetaPolicy(raw)
	if err != nil {
		return messagesadapter.DefaultBetaPolicy()
	}
	return policy
}

// GetAnthropicBetaPolicy 读取当前策略(admin 侧读用;经 SettingsStore 生效值)。
func GetAnthropicBetaPolicy(ctx context.Context, store *SettingsStore) messagesadapter.BetaPolicy {
	return NewBetaPolicyProvider(store).BetaPolicy(ctx)
}

// SetAnthropicBetaPolicy 校验并写入策略(admin 侧写用;写 DB + 刷新 Redis/本地)。
func SetAnthropicBetaPolicy(ctx context.Context, store *SettingsStore, policy messagesadapter.BetaPolicy) error {
	raw, err := encodeBetaPolicy(policy)
	if err != nil {
		return err
	}
	return store.Set(ctx, AnthropicBetaPolicyKey, raw)
}
