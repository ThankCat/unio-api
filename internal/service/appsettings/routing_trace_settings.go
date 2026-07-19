package appsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const GatewayRoutingTraceKey = "gateway.routing_trace"

type RoutingTraceSettings struct {
	SampleRate       float64
	Retention        time.Duration
	CleanupInterval  time.Duration
	CleanupBatchSize int32
}

func DefaultRoutingTraceSettings() RoutingTraceSettings {
	return RoutingTraceSettings{
		SampleRate: 0.05, Retention: 7 * 24 * time.Hour,
		CleanupInterval: time.Hour, CleanupBatchSize: 1000,
	}
}

type routingTraceDoc struct {
	SampleRate        float64 `json:"sample_rate"`
	RetentionDays     int     `json:"retention_days"`
	CleanupIntervalMs int64   `json:"cleanup_interval_ms"`
	CleanupBatchSize  int32   `json:"cleanup_batch_size"`
}

func encodeRoutingTraceSettings(settings RoutingTraceSettings) json.RawMessage {
	doc := routingTraceDoc{
		SampleRate:        settings.SampleRate,
		RetentionDays:     int(settings.Retention / (24 * time.Hour)),
		CleanupIntervalMs: durationToMs(settings.CleanupInterval),
		CleanupBatchSize:  settings.CleanupBatchSize,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(fmt.Sprintf("appsettings: encode routing trace: %v", err))
	}
	return raw
}

func DecodeRoutingTraceSettings(raw []byte) (RoutingTraceSettings, error) {
	var doc routingTraceDoc
	if err := strictUnmarshal(raw, &doc); err != nil {
		return RoutingTraceSettings{}, err
	}
	if doc.SampleRate < 0 || doc.SampleRate > 1 {
		return RoutingTraceSettings{}, errors.New("sample_rate must be between 0 and 1")
	}
	if doc.RetentionDays <= 0 || doc.RetentionDays > 3650 {
		return RoutingTraceSettings{}, errors.New("retention_days must be between 1 and 3650")
	}
	if doc.CleanupIntervalMs <= 0 {
		return RoutingTraceSettings{}, errors.New("cleanup_interval_ms must be > 0")
	}
	if doc.CleanupBatchSize <= 0 || doc.CleanupBatchSize > 10000 {
		return RoutingTraceSettings{}, errors.New("cleanup_batch_size must be between 1 and 10000")
	}
	return RoutingTraceSettings{
		SampleRate:       doc.SampleRate,
		Retention:        time.Duration(doc.RetentionDays) * 24 * time.Hour,
		CleanupInterval:  msToDuration(doc.CleanupIntervalMs),
		CleanupBatchSize: doc.CleanupBatchSize,
	}, nil
}

func routingTraceDefinition() Definition {
	return Definition{
		Key: GatewayRoutingTraceKey, Category: "gateway", Label: "路由决策追踪",
		Description: "普通路由决策的稳定采样率、追踪保留天数和分批清理参数。异常决策始终保存，不受 sample_rate 影响。",
		HotReload:   true,
		Default:     encodeRoutingTraceSettings(DefaultRoutingTraceSettings()),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeRoutingTraceSettings(raw)
			return err
		},
	}
}

func GatewayRoutingTrace(ctx context.Context, store *SettingsStore) RoutingTraceSettings {
	if store == nil {
		return DefaultRoutingTraceSettings()
	}
	settings, err := DecodeRoutingTraceSettings(store.Raw(ctx, GatewayRoutingTraceKey))
	if err != nil {
		return DefaultRoutingTraceSettings()
	}
	return settings
}
