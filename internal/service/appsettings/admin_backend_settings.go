package appsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// 本文件登记 admin_backend 域(admin 进程后端 / 渠道检测 worker 消费)的运行时配置。
// admin 后端每请求经 store 现读,并使用本地 3s 缓存,不走 applier。
// 渠道检测 worker 同样现读本域(可无 Redis,退化为 DB + 本地缓存)。

// admin_backend.channel_health_thresholds 主观健康分桶阈值已删除；
// 运营只看客观事实（成功率、失败次数、流式 TTFT、总耗时、熔断、容量、权重），不再按阈值分桶。

// AdminBackendChannelTestKey 是渠道检测/自动巡检的聚合配置(开关、间隔、探测超时、日志保留)。
const AdminBackendChannelTestKey = "admin_backend.channel_test"

// AdminBackendChannelModelDiscoveryKey 是上游模型发现 worker 的独立配置。
// 它只控制快照发现，不复用会修改 credential_valid 的渠道巡检开关。
const AdminBackendChannelModelDiscoveryKey = "admin_backend.channel_model_discovery"

// DefaultChannelTestProbeTimeoutSetting 是渠道检测超时的代码默认(60s)。
// 与迁移前 CHANNEL_TEST_PROBE_TIMEOUT_MAX 对齐:给慢上游足够响应时间,又不让坏渠道拖垮巡检。
const DefaultChannelTestProbeTimeoutSetting = 60 * time.Second

// DefaultChannelTestWorkerEnabledSetting 与迁移前 CHANNEL_TEST_WORKER_ENABLED 默认一致。
const DefaultChannelTestWorkerEnabledSetting = true

// DefaultChannelTestWorkerIntervalSetting 与迁移前 CHANNEL_TEST_WORKER_INTERVAL 默认一致(30m)。
const DefaultChannelTestWorkerIntervalSetting = 30 * time.Minute

// DefaultChannelTestLogRetentionSetting 与迁移前 CHANNEL_TEST_LOG_RETENTION_PER_CHANNEL 默认一致。
const DefaultChannelTestLogRetentionSetting = 200

// ---- 渠道检测 / 自动巡检(聚合) ----

// ChannelTestSettings 是渠道手动检测与自动巡检 worker 的聚合配置。
// ProbeTimeout 与 gateway.default_response_timeout_ms / channels.response_timeout_ms 完全正交。
type ChannelTestSettings struct {
	Enabled                bool
	Interval               time.Duration
	ProbeTimeout           time.Duration
	LogRetentionPerChannel int
}

// DefaultChannelTestSettings 与迁移前 env / 拆分 key 的默认一致。
func DefaultChannelTestSettings() ChannelTestSettings {
	return ChannelTestSettings{
		Enabled:                DefaultChannelTestWorkerEnabledSetting,
		Interval:               DefaultChannelTestWorkerIntervalSetting,
		ProbeTimeout:           DefaultChannelTestProbeTimeoutSetting,
		LogRetentionPerChannel: DefaultChannelTestLogRetentionSetting,
	}
}

type channelTestDoc struct {
	Enabled                bool  `json:"enabled"`
	IntervalMs             int64 `json:"interval_ms"`
	ProbeTimeoutMs         int64 `json:"probe_timeout_ms"`
	LogRetentionPerChannel int   `json:"log_retention_per_channel"`
}

func encodeChannelTestSettings(s ChannelTestSettings) json.RawMessage {
	raw, err := json.Marshal(channelTestDoc{
		Enabled:                s.Enabled,
		IntervalMs:             durationToMs(s.Interval),
		ProbeTimeoutMs:         durationToMs(s.ProbeTimeout),
		LogRetentionPerChannel: s.LogRetentionPerChannel,
	})
	if err != nil {
		panic(fmt.Sprintf("appsettings: encode channel test settings: %v", err))
	}
	return raw
}

// DecodeChannelTestSettings 解码并校验渠道巡检聚合配置(拒绝未知字段;时长/保留须 > 0)。
func DecodeChannelTestSettings(raw []byte) (ChannelTestSettings, error) {
	var doc channelTestDoc
	if err := strictUnmarshal(raw, &doc); err != nil {
		return ChannelTestSettings{}, err
	}
	if doc.IntervalMs <= 0 {
		return ChannelTestSettings{}, errors.New("interval_ms must be > 0")
	}
	if doc.ProbeTimeoutMs <= 0 {
		return ChannelTestSettings{}, errors.New("probe_timeout_ms must be > 0")
	}
	if doc.LogRetentionPerChannel <= 0 {
		return ChannelTestSettings{}, errors.New("log_retention_per_channel must be > 0")
	}
	return ChannelTestSettings{
		Enabled:                doc.Enabled,
		Interval:               msToDuration(doc.IntervalMs),
		ProbeTimeout:           msToDuration(doc.ProbeTimeoutMs),
		LogRetentionPerChannel: doc.LogRetentionPerChannel,
	}, nil
}

func channelTestDefinition() Definition {
	return Definition{
		Key:      AdminBackendChannelTestKey,
		Category: "admin_backend",
		Label:    "渠道巡检",
		Description: "渠道凭据检测与自动巡检的聚合配置:开关、巡检间隔、探测超时、每渠道日志保留条数。" +
			"开启后周期性对所有启用渠道发合成探测,据此翻 credential_valid(失效自动摘除、通过自动恢复)。" +
			"探测超时仅用于手动检测与自动巡检,与默认响应超时 / 渠道 response_timeout_ms 无关。" +
			"时长单位毫秒。保存后 admin 与 worker 约 3 秒内生效。",
		HotReload: true,
		Default:   encodeChannelTestSettings(DefaultChannelTestSettings()),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeChannelTestSettings(raw)
			return err
		},
	}
}

// AdminBackendChannelTest 读取当前生效的渠道巡检聚合配置。
// store 为 nil(如单测)或解码失败时回默认。
func AdminBackendChannelTest(ctx context.Context, store *SettingsStore) ChannelTestSettings {
	if store == nil {
		return DefaultChannelTestSettings()
	}
	s, err := DecodeChannelTestSettings(store.Raw(ctx, AdminBackendChannelTestKey))
	if err != nil {
		return DefaultChannelTestSettings()
	}
	return s
}

// AdminBackendChannelTestProbeTimeout 读取探测超时(聚合配置的便捷访问器,供 channeltest 单字段消费)。
func AdminBackendChannelTestProbeTimeout(ctx context.Context, store *SettingsStore) time.Duration {
	return AdminBackendChannelTest(ctx, store).ProbeTimeout
}

// AdminBackendChannelTestWorkerEnabled 读取巡检开关(聚合配置的便捷访问器)。
func AdminBackendChannelTestWorkerEnabled(ctx context.Context, store *SettingsStore) bool {
	return AdminBackendChannelTest(ctx, store).Enabled
}

// AdminBackendChannelTestWorkerInterval 读取巡检间隔(聚合配置的便捷访问器)。
func AdminBackendChannelTestWorkerInterval(ctx context.Context, store *SettingsStore) time.Duration {
	return AdminBackendChannelTest(ctx, store).Interval
}

// AdminBackendChannelTestLogRetention 读取日志保留条数(聚合配置的便捷访问器)。
func AdminBackendChannelTestLogRetention(ctx context.Context, store *SettingsStore) int {
	return AdminBackendChannelTest(ctx, store).LogRetentionPerChannel
}

// ChannelModelDiscoverySettings 是渠道上游模型发现的热更新配置。
type ChannelModelDiscoverySettings struct {
	Enabled             bool
	Interval            time.Duration
	Timeout             time.Duration
	RetentionPerChannel int
}

// DefaultChannelModelDiscoverySettings 返回发现 worker 的保守默认值。
func DefaultChannelModelDiscoverySettings() ChannelModelDiscoverySettings {
	return ChannelModelDiscoverySettings{
		Enabled: true, Interval: 6 * time.Hour, Timeout: 15 * time.Second, RetentionPerChannel: 30,
	}
}

type channelModelDiscoveryDoc struct {
	Enabled             bool  `json:"enabled"`
	IntervalMs          int64 `json:"interval_ms"`
	TimeoutMs           int64 `json:"timeout_ms"`
	RetentionPerChannel int   `json:"retention_per_channel"`
}

func encodeChannelModelDiscoverySettings(s ChannelModelDiscoverySettings) json.RawMessage {
	raw, err := json.Marshal(channelModelDiscoveryDoc{
		Enabled: s.Enabled, IntervalMs: durationToMs(s.Interval), TimeoutMs: durationToMs(s.Timeout),
		RetentionPerChannel: s.RetentionPerChannel,
	})
	if err != nil {
		panic(fmt.Sprintf("appsettings: encode channel model discovery settings: %v", err))
	}
	return raw
}

// DecodeChannelModelDiscoverySettings 解码并校验模型发现配置。
func DecodeChannelModelDiscoverySettings(raw []byte) (ChannelModelDiscoverySettings, error) {
	var doc channelModelDiscoveryDoc
	if err := strictUnmarshal(raw, &doc); err != nil {
		return ChannelModelDiscoverySettings{}, err
	}
	if doc.IntervalMs <= 0 {
		return ChannelModelDiscoverySettings{}, errors.New("interval_ms must be > 0")
	}
	if doc.TimeoutMs <= 0 {
		return ChannelModelDiscoverySettings{}, errors.New("timeout_ms must be > 0")
	}
	if doc.RetentionPerChannel <= 0 {
		return ChannelModelDiscoverySettings{}, errors.New("retention_per_channel must be > 0")
	}
	return ChannelModelDiscoverySettings{
		Enabled: doc.Enabled, Interval: msToDuration(doc.IntervalMs), Timeout: msToDuration(doc.TimeoutMs),
		RetentionPerChannel: doc.RetentionPerChannel,
	}, nil
}

func channelModelDiscoveryDefinition() Definition {
	defaults := DefaultChannelModelDiscoverySettings()
	return Definition{
		Key: AdminBackendChannelModelDiscoveryKey, Category: "admin_backend", Label: "渠道模型发现",
		Description: "周期读取启用渠道的上游模型列表并保存成功快照。发现失败不会修改 credential_valid、渠道、模型或绑定状态。时长单位毫秒，保存后约 3 秒内生效。",
		HotReload:   true, Default: encodeChannelModelDiscoverySettings(defaults),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeChannelModelDiscoverySettings(raw)
			return err
		},
	}
}

// AdminBackendChannelModelDiscovery 读取当前生效的上游模型发现配置。
func AdminBackendChannelModelDiscovery(ctx context.Context, store *SettingsStore) ChannelModelDiscoverySettings {
	if store == nil {
		return DefaultChannelModelDiscoverySettings()
	}
	settings, err := DecodeChannelModelDiscoverySettings(store.Raw(ctx, AdminBackendChannelModelDiscoveryKey))
	if err != nil {
		return DefaultChannelModelDiscoverySettings()
	}
	return settings
}
