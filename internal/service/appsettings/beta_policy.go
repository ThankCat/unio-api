// Package appsettings 承载「管理端可编辑、后端运行时读」的全局设置(app_settings 表)。
//
// 首个使用者是 Anthropic beta 转发策略:gateway 进程通过 AnthropicBetaProvider(带 TTL 缓存)
// 读取最新策略并注入 adapter;admin 进程通过 GetAnthropicBetaPolicy / SetAnthropicBetaPolicy 读写。
// 两进程独立:admin 写库,gateway 在 TTL 内自动刷新到最新值(无需重启)。
package appsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	messagesadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
)

// AnthropicBetaPolicyKey 是 app_settings 中 Anthropic beta 策略的 key。
const AnthropicBetaPolicyKey = "anthropic.beta_policy"

// Store 是 appsettings 依赖的最小存储接口(由 *sqlc.Queries 实现)。
type Store interface {
	GetAppSetting(ctx context.Context, key string) ([]byte, error)
	UpsertAppSetting(ctx context.Context, arg sqlc.UpsertAppSettingParams) error
}

// betaPolicyDoc 是 beta 策略的持久化 JSON 形状。
type betaPolicyDoc struct {
	Mode string   `json:"mode"`
	List []string `json:"list"`
}

// ValidateBetaPolicy 校验一份 beta 策略是否合法(供 admin 写入前校验)。
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

func encodeBetaPolicy(policy messagesadapter.BetaPolicy) ([]byte, error) {
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

// GetAnthropicBetaPolicy 从库读取当前策略;未设置(无行)时返回内置默认。
func GetAnthropicBetaPolicy(ctx context.Context, store Store) (messagesadapter.BetaPolicy, error) {
	raw, err := store.GetAppSetting(ctx, AnthropicBetaPolicyKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return messagesadapter.DefaultBetaPolicy(), nil
		}
		return messagesadapter.BetaPolicy{}, err
	}
	return decodeBetaPolicy(raw)
}

// SetAnthropicBetaPolicy 校验并写入策略。
func SetAnthropicBetaPolicy(ctx context.Context, store Store, policy messagesadapter.BetaPolicy) error {
	if err := ValidateBetaPolicy(policy); err != nil {
		return err
	}
	raw, err := encodeBetaPolicy(policy)
	if err != nil {
		return err
	}
	return store.UpsertAppSetting(ctx, sqlc.UpsertAppSettingParams{
		Key:   AnthropicBetaPolicyKey,
		Value: raw,
	})
}

// AnthropicBetaProvider 是带 TTL 缓存的 beta 策略 provider,实现 messagesadapter.BetaPolicyProvider。
//
// gateway 进程注入它;每次读取若缓存过期则从库刷新一次(懒刷新)。库不可用时返回最近一次好值,
// 无历史好值则返回内置默认,并在两种情况下都推进过期时间以避免热路径反复打库。
type AnthropicBetaProvider struct {
	store  Store
	ttl    time.Duration
	logger *slog.Logger

	mu     sync.Mutex
	cached messagesadapter.BetaPolicy
	expiry time.Time
	loaded bool
}

// NewAnthropicBetaProvider 创建 provider;ttl<=0 时回退到 30s。
func NewAnthropicBetaProvider(store Store, ttl time.Duration, logger *slog.Logger) *AnthropicBetaProvider {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &AnthropicBetaProvider{store: store, ttl: ttl, logger: logger}
}

// BetaPolicy 返回当前生效策略(带 TTL 缓存)。
func (p *AnthropicBetaProvider) BetaPolicy(ctx context.Context) messagesadapter.BetaPolicy {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.loaded && time.Now().Before(p.expiry) {
		return p.cached
	}

	policy, err := GetAnthropicBetaPolicy(ctx, p.store)
	if err != nil {
		p.expiry = time.Now().Add(p.ttl) // 退避,避免热路径反复打库
		if p.loaded {
			p.logger.WarnContext(ctx, "anthropic beta policy refresh failed, using last good",
				slog.String("error", err.Error()))
			return p.cached
		}
		p.logger.WarnContext(ctx, "anthropic beta policy load failed, using default",
			slog.String("error", err.Error()))
		p.cached = messagesadapter.DefaultBetaPolicy()
		p.loaded = true
		return p.cached
	}

	p.cached = policy
	p.expiry = time.Now().Add(p.ttl)
	p.loaded = true
	return p.cached
}
