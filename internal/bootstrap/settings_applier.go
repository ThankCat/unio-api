package bootstrap

import (
	"context"
	"encoding/json"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// settingsApplyInterval 是 applier 从 SettingsStore 拉取最新配置并推给消费方的周期。
// admin 改动经 Redis 传播,叠加此周期后生效延迟上限约 5s(用户已确认可接受)。
const settingsApplyInterval = 5 * time.Second

// settingsReader 抽象 applier 依赖的配置读取能力(由 *appsettings.SettingsStore 实现,便于单测注入)。
type settingsReader interface {
	Raw(ctx context.Context, key string) json.RawMessage
}

type channel429CooldownPolicyTarget interface {
	SetChannel429CooldownPolicy(defaultCooldown, cap time.Duration)
}

// capacityWaitTarget 是全池并发短等预算的消费方（每个协议 service 的 AttemptRunner，§9.4）。
type capacityWaitTarget interface {
	SetCapacityWaitTimeout(d time.Duration)
}

// settingsApplier 把运行时配置的最新值周期性推给 gateway 各热路径消费方。
//
// 这里只处理非准入类本机配置。circuit breaker、rate/concurrency defaults 与 routing balance
// 均由 Redis committed runtime control 驱动，禁止从普通 settings cache 推送本机副本。
type settingsApplier struct {
	store  settingsReader
	logger *zap.Logger

	gate         *lifecycle.ChannelCredentialGate
	router       *routing.Router
	sticky       *lifecycle.StickyRouter
	channel429   channel429CooldownPolicyTarget
	capacityWait []capacityWaitTarget
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
			a.logger.Error("settings applier panic recovered", zap.Any("panic", r))
		}
	}()
	a.applyOnce(ctx)
}

// applyOnce 读取非准入类配置的当前生效值并推给消费方。
//
// 解码失败(理论不可达:写入口已校验)只记 warn 并保持该项当前值,不回退默认——
// 避免 Redis/DB 数据损坏导致配置突然跳回默认造成行为突变。
func (a *settingsApplier) applyOnce(ctx context.Context) {
	if d, err := appsettings.DecodePositiveMsSetting(a.store.Raw(ctx, appsettings.GatewayStreamIdleTimeoutKey)); err == nil {
		adapter.SetStreamIdleTimeout(d)
	} else {
		a.warnDecode(ctx, appsettings.GatewayStreamIdleTimeoutKey, err)
	}

	if a.channel429 != nil {
		if cooldown, err := appsettings.DecodeChannelCooldownSettings(a.store.Raw(ctx, appsettings.GatewayChannelCooldownKey)); err == nil {
			a.channel429.SetChannel429CooldownPolicy(cooldown.Cooldown, cooldown.Cap)
		} else {
			a.warnDecode(ctx, appsettings.GatewayChannelCooldownKey, err)
		}
	}

	if n, err := appsettings.DecodePositiveIntSetting(a.store.Raw(ctx, appsettings.GatewayCredential401ThresholdKey)); err == nil {
		a.gate.SetThreshold(n)
	} else {
		a.warnDecode(ctx, appsettings.GatewayCredential401ThresholdKey, err)
	}

	if d, err := appsettings.DecodePositiveMsSetting(a.store.Raw(ctx, appsettings.GatewayDefaultResponseTimeoutKey)); err == nil {
		a.router.SetDefaultResponseTimeout(d)
	} else {
		a.warnDecode(ctx, appsettings.GatewayDefaultResponseTimeoutKey, err)
	}

	if d, err := appsettings.DecodePositiveMsSetting(a.store.Raw(ctx, appsettings.GatewayDefaultFirstTokenTimeoutKey)); err == nil {
		a.router.SetDefaultFirstTokenTimeout(d)
	} else {
		a.warnDecode(ctx, appsettings.GatewayDefaultFirstTokenTimeoutKey, err)
	}

	if a.sticky != nil {
		if st, err := appsettings.DecodeRoutingStickySettings(a.store.Raw(ctx, appsettings.GatewayRoutingStickyKey)); err == nil {
			a.sticky.SetConfig(st.EnabledDefault, st.TTL)
		} else {
			a.warnDecode(ctx, appsettings.GatewayRoutingStickyKey, err)
		}
	}

	if len(a.capacityWait) > 0 {
		if d, err := appsettings.DecodeNonNegativeMsSetting(
			a.store.Raw(ctx, appsettings.GatewayCapacityWaitTimeoutKey),
		); err == nil {
			for _, target := range a.capacityWait {
				target.SetCapacityWaitTimeout(d)
			}
		} else {
			a.warnDecode(ctx, appsettings.GatewayCapacityWaitTimeoutKey, err)
		}
	}
}

func (a *settingsApplier) warnDecode(_ context.Context, key string, err error) {
	a.logger.Warn("settings applier: decode failed, keeping current value",
		zap.String("key", key), zap.String("error", err.Error()))
}
