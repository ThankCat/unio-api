package appsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// 本文件登记 admin_backend 域(admin 进程后端 / 渠道检测 worker 消费)的运行时配置。
// 域约定见 DESIGN-runtime-settings-batch2-domains.md §2:admin 后端每请求经 store 现读,
// 本地 3s 缓存即可满足生效时效(admin QPS 低,不走 applier)。
// 渠道检测 worker 同样现读本域(可无 Redis,退化为 DB + 本地缓存)。

// AdminBackendChannelHealthKey 是渠道健康分桶阈值在 app_settings 中的 key。
const AdminBackendChannelHealthKey = "admin_backend.channel_health_thresholds"

// AdminBackendChannelTestKey 是渠道检测/自动巡检的聚合配置(开关、间隔、探测超时、日志保留)。
const AdminBackendChannelTestKey = "admin_backend.channel_test"

// DefaultChannelTestProbeTimeoutSetting 是渠道检测超时的代码默认(60s)。
// 与迁移前 CHANNEL_TEST_PROBE_TIMEOUT_MAX 对齐:给慢上游足够响应时间,又不让坏渠道拖垮巡检。
const DefaultChannelTestProbeTimeoutSetting = 60 * time.Second

// DefaultChannelTestWorkerEnabledSetting 与迁移前 CHANNEL_TEST_WORKER_ENABLED 默认一致。
const DefaultChannelTestWorkerEnabledSetting = true

// DefaultChannelTestWorkerIntervalSetting 与迁移前 CHANNEL_TEST_WORKER_INTERVAL 默认一致(30m)。
const DefaultChannelTestWorkerIntervalSetting = 30 * time.Minute

// DefaultChannelTestLogRetentionSetting 与迁移前 CHANNEL_TEST_LOG_RETENTION_PER_CHANNEL 默认一致。
const DefaultChannelTestLogRetentionSetting = 200

// ChannelHealthThresholds 是渠道健康分桶阈值(按区间内 attempt 成功率):
// >= HealthyRate 为 healthy,>= DegradedRate 为 degraded,否则 unhealthy(无样本 no_data)。
// 纯运维展示分类,不影响路由/计费。
type ChannelHealthThresholds struct {
	HealthyRate  float64
	DegradedRate float64
}

// DefaultChannelHealthThresholds 与迁移前散落各包的 0.95/0.80 硬编码一致。
func DefaultChannelHealthThresholds() ChannelHealthThresholds {
	return ChannelHealthThresholds{HealthyRate: 0.95, DegradedRate: 0.80}
}

type channelHealthThresholdsDoc struct {
	HealthyRate  float64 `json:"healthy_rate"`
	DegradedRate float64 `json:"degraded_rate"`
}

func encodeChannelHealthThresholds(t ChannelHealthThresholds) json.RawMessage {
	raw, err := json.Marshal(channelHealthThresholdsDoc{
		HealthyRate:  t.HealthyRate,
		DegradedRate: t.DegradedRate,
	})
	if err != nil {
		panic(fmt.Sprintf("appsettings: encode channel health thresholds: %v", err))
	}
	return raw
}

// DecodeChannelHealthThresholds 解码并校验分桶阈值(拒绝未知字段;0 < degraded < healthy <= 1)。
func DecodeChannelHealthThresholds(raw []byte) (ChannelHealthThresholds, error) {
	var doc channelHealthThresholdsDoc
	if err := strictUnmarshal(raw, &doc); err != nil {
		return ChannelHealthThresholds{}, err
	}
	t := ChannelHealthThresholds{HealthyRate: doc.HealthyRate, DegradedRate: doc.DegradedRate}
	if t.DegradedRate <= 0 || t.HealthyRate > 1 || t.DegradedRate >= t.HealthyRate {
		return ChannelHealthThresholds{}, errors.New("thresholds must satisfy 0 < degraded_rate < healthy_rate <= 1")
	}
	return t, nil
}

func channelHealthThresholdsDefinition() Definition {
	return Definition{
		Key:      AdminBackendChannelHealthKey,
		Category: "admin_backend",
		Label:    "渠道健康分桶阈值",
		Description: "按区间内 attempt 成功率给渠道分桶:≥healthy_rate 为 healthy,≥degraded_rate 为 degraded," +
			"否则 unhealthy。仅影响后台健康展示/分布统计,不影响路由与计费。须满足 0 < degraded_rate < healthy_rate ≤ 1。" +
			"默认与前端「请求成功率」告警档位(admin_frontend.dashboard_thresholds)对齐,但两者主体不同、可独立调档。",
		HotReload: true,
		Default:   encodeChannelHealthThresholds(DefaultChannelHealthThresholds()),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeChannelHealthThresholds(raw)
			return err
		},
	}
}

// AdminBackendChannelHealthThresholds 读取当前生效的分桶阈值。
// store 为 nil(如单测)或解码失败时回默认——展示分类兜底默认无风险。
func AdminBackendChannelHealthThresholds(ctx context.Context, store *SettingsStore) ChannelHealthThresholds {
	if store == nil {
		return DefaultChannelHealthThresholds()
	}
	t, err := DecodeChannelHealthThresholds(store.Raw(ctx, AdminBackendChannelHealthKey))
	if err != nil {
		return DefaultChannelHealthThresholds()
	}
	return t
}

// ---- 渠道检测 / 自动巡检(聚合) ----

// ChannelTestSettings 是渠道手动检测与自动巡检 worker 的聚合配置。
// ProbeTimeout 与 gateway.default_channel_timeout_ms / channels.timeout_ms(用户请求上游超时)完全正交。
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
			"探测超时仅用于手动检测与自动巡检,与「默认渠道超时」/ 渠道行 timeout_ms(用户请求上游超时)无关。" +
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
