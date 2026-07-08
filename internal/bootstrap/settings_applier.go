package bootstrap

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/ratelimit"
	"github.com/ThankCat/unio-api/internal/service/appsettings"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// settingsApplyInterval 是 applier 从 SettingsStore 拉取最新配置并推给消费方的周期。
// admin 改动经 Redis 传播,叠加此周期后生效延迟上限约 5s(用户已确认可接受)。
const settingsApplyInterval = 5 * time.Second

// settingsReader 抽象 applier 依赖的配置读取能力(由 *appsettings.SettingsStore 实现,便于单测注入)。
type settingsReader interface {
	Raw(ctx context.Context, key string) json.RawMessage
}

// settingsApplier 把运行时配置的最新值周期性推给 gateway 各热路径消费方。
//
// 为什么轮询推送而不是每请求现读 SettingsStore:这 6 组每请求都要用,直接现读会给热路径加
// 锁竞争;applier 把「读配置」与「用配置」解耦,热路径只读消费方自身的 atomic/锁内字段,零额外开销。
// 推送全部经由各消费方的线程安全写入口,只替换标量/小结构,不动进行中的计数与状态机。
type settingsApplier struct {
	store  settingsReader
	logger *slog.Logger

	breaker  *lifecycle.ChannelCircuitBreaker
	guard    *ratelimit.Guard
	cooldown *lifecycle.ChannelCooldownRegistry
	gate     *lifecycle.ChannelCredentialGate
	router   *routing.Router
}

// run 周期性拉取并推送,直到 ctx 取消(随 app shutdown 退出)。
func (a *settingsApplier) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.safeApply(ctx)
		}
	}
}

// safeApply 单轮推送;panic 只中断本轮并记日志,不杀死 applier goroutine。
func (a *settingsApplier) safeApply(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			a.logger.ErrorContext(ctx, "settings applier panic recovered", slog.Any("panic", r))
		}
	}()
	a.applyOnce(ctx)
}

// applyOnce 读取 6 组配置的当前生效值并推给消费方。
//
// 解码失败(理论不可达:写入口已校验)只记 warn 并保持该项当前值,不回退默认——
// 避免 Redis/DB 数据损坏导致配置突然跳回默认造成行为突变。
func (a *settingsApplier) applyOnce(ctx context.Context) {
	if cb, err := appsettings.DecodeCircuitBreakerSettings(a.store.Raw(ctx, appsettings.GatewayCircuitBreakerKey)); err == nil {
		a.breaker.SetConfig(lifecycle.ChannelCircuitBreakerConfig{
			Window:       cb.Window,
			MinRequests:  cb.MinRequests,
			FailureRatio: cb.FailureRatio,
			OpenDuration: cb.OpenDuration,
		})
		a.breaker.SetEnabled(cb.Enabled)
	} else {
		a.warnDecode(ctx, appsettings.GatewayCircuitBreakerKey, err)
	}

	if rl, err := appsettings.DecodeRateLimitDefaultsSettings(a.store.Raw(ctx, appsettings.GatewayRateLimitDefaultsKey)); err == nil {
		a.guard.SetDefaults(ratelimit.DefaultLimits{RPM: rl.RPM, TPM: rl.TPM, RPD: rl.RPD})
		a.guard.SetFailOpen(rl.FailOpen())
	} else {
		a.warnDecode(ctx, appsettings.GatewayRateLimitDefaultsKey, err)
	}

	if d, err := appsettings.DecodePositiveMsSetting(a.store.Raw(ctx, appsettings.GatewayStreamIdleTimeoutKey)); err == nil {
		adapter.SetStreamIdleTimeout(d)
	} else {
		a.warnDecode(ctx, appsettings.GatewayStreamIdleTimeoutKey, err)
	}

	if cd, err := appsettings.DecodeChannelCooldownSettings(a.store.Raw(ctx, appsettings.GatewayChannelCooldownKey)); err == nil {
		a.cooldown.SetCooldown(cd.Cooldown, cd.Cap)
	} else {
		a.warnDecode(ctx, appsettings.GatewayChannelCooldownKey, err)
	}

	if n, err := appsettings.DecodePositiveIntSetting(a.store.Raw(ctx, appsettings.GatewayCredential401ThresholdKey)); err == nil {
		a.gate.SetThreshold(n)
	} else {
		a.warnDecode(ctx, appsettings.GatewayCredential401ThresholdKey, err)
	}

	if d, err := appsettings.DecodePositiveMsSetting(a.store.Raw(ctx, appsettings.GatewayDefaultChannelTimeoutKey)); err == nil {
		a.router.SetDefaultTimeout(d)
	} else {
		a.warnDecode(ctx, appsettings.GatewayDefaultChannelTimeoutKey, err)
	}
}

func (a *settingsApplier) warnDecode(ctx context.Context, key string, err error) {
	a.logger.WarnContext(ctx, "settings applier: decode failed, keeping current value",
		slog.String("key", key), slog.String("error", err.Error()))
}
