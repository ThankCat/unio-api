package appsettings

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// RuntimeControlRestorer 是 Admin/Worker 启动恢复关键 setting control 所需的最小能力。
type RuntimeControlRestorer interface {
	RouteRateLimitControl() breakerstore.ControlTarget
	ChannelRateLimitControl() breakerstore.ControlTarget
	GlobalConcurrencyControl() breakerstore.ControlTarget
	SettingControl(settingKey string) breakerstore.ControlTarget
	RestoreMissingControl(ctx context.Context, target breakerstore.ControlTarget, revision int64, payload string) (bool, error)
	ReadControl(ctx context.Context, target breakerstore.ControlTarget, expectedRevision int64) (breakerstore.ControlSnapshot, error)
}

var runtimeControlSettingKeys = [...]string{
	GatewayRouteRateLimitDefaultsKey,
	GatewayChannelRateLimitDefaultsKey,
	GatewayConcurrencyDefaultsKey,
	GatewayCircuitBreakerKey,
	GatewayRoutingBalanceKey,
}

// RuntimeControlRestoreObserver receives one validated critical setting control after recovery.
// The callback contains only the fixed setting key and its revision, never the setting payload.
type RuntimeControlRestoreObserver func(settingKey string, revision int64, restored bool)

// RestoreCriticalRuntimeControls 从 PostgreSQL 当前事实仅补齐缺失的 Redis control，并严格核对已有 control。
// 它绝不覆盖 stale/ahead/pending control；这些情况必须先由 durable operation reconciler 收口。
func RestoreCriticalRuntimeControls(ctx context.Context, settings *SettingsStore, controls RuntimeControlRestorer) error {
	return RestoreCriticalRuntimeControlsObserved(ctx, settings, controls, nil)
}

// RestoreCriticalRuntimeControlsObserved is RestoreCriticalRuntimeControls with an optional
// observability callback invoked only after strict active revision and payload validation.
func RestoreCriticalRuntimeControlsObserved(
	ctx context.Context,
	settings *SettingsStore,
	controls RuntimeControlRestorer,
	observe RuntimeControlRestoreObserver,
) error {
	if settings == nil || controls == nil {
		return failure.New(failure.CodeGatewayBreakerStoreUnavailable, failure.WithMessage("appsettings: runtime control restorer unavailable"))
	}
	for _, key := range runtimeControlSettingKeys {
		record, err := settings.Record(ctx, key)
		if err != nil {
			return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("appsettings: read runtime control setting"))
		}
		payload, err := canonicalRuntimeSetting(key, record.Value)
		if err != nil {
			return failure.Wrap(failure.CodeConfigInvalid, err, failure.WithMessage("appsettings: invalid runtime control setting"))
		}
		target := runtimeControlTarget(controls, key)
		restored, err := controls.RestoreMissingControl(ctx, target, record.Revision, string(payload))
		if err != nil {
			return err
		}
		snapshot, err := controls.ReadControl(ctx, target, record.Revision)
		if err != nil {
			return err
		}
		activePayload, payloadErr := canonicalRuntimeSetting(key, json.RawMessage(snapshot.ActivePayload))
		if snapshot.SyncState != "active" || snapshot.PendingRevision != 0 || snapshot.ActiveRevision != record.Revision ||
			payloadErr != nil || !bytes.Equal(activePayload, payload) {
			return failure.New(
				failure.CodeConfigInvalid,
				failure.WithMessage("appsettings: runtime control requires reconciliation"),
				failure.WithField("setting_key", key),
			)
		}
		if observe != nil {
			observe(key, record.Revision, restored)
		}
	}
	return nil
}

// CanonicalRuntimeSettingPayload 严格解码并规范化五个 P4 关键 setting，供 durable reconciler
// 按 PostgreSQL 当前事实重建 Redis control。其它 key 一律拒绝。
func CanonicalRuntimeSettingPayload(key string, raw json.RawMessage) (json.RawMessage, error) {
	return canonicalRuntimeSetting(key, raw)
}
