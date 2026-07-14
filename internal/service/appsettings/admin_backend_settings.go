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

// AdminBackendChannelTestProbeTimeoutKey 是渠道检测(手动/自动巡检)探测超时在 app_settings 中的 key。
// 与 gateway.default_channel_timeout_ms / channels.timeout_ms(用户请求上游超时)完全正交。
const AdminBackendChannelTestProbeTimeoutKey = "admin_backend.channel_test_probe_timeout_ms"

// DefaultChannelTestProbeTimeoutSetting 是渠道检测超时的代码默认(60s)。
// 与迁移前 CHANNEL_TEST_PROBE_TIMEOUT_MAX 对齐:给慢上游足够响应时间,又不让坏渠道拖垮巡检。
const DefaultChannelTestProbeTimeoutSetting = 60 * time.Second

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

func channelTestProbeTimeoutDefinition() Definition {
	return Definition{
		Key:      AdminBackendChannelTestProbeTimeoutKey,
		Category: "admin_backend",
		Label:    "渠道检测超时",
		Description: "手动「检测渠道」与自动巡检 worker 向真实上游发探测请求时的超时。" +
			"单位毫秒。与「默认渠道超时」/ 渠道行 timeout_ms(用户请求上游超时)无关——检测专用,互不影响。" +
			"保存后 admin 与 worker 约 3 秒内生效。",
		HotReload: true,
		Default:   encodeMsSetting(DefaultChannelTestProbeTimeoutSetting),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodePositiveMsSetting(raw)
			return err
		},
	}
}

// AdminBackendChannelTestProbeTimeout 读取当前生效的渠道检测超时。
// store 为 nil(如单测)或解码失败时回默认。
func AdminBackendChannelTestProbeTimeout(ctx context.Context, store *SettingsStore) time.Duration {
	if store == nil {
		return DefaultChannelTestProbeTimeoutSetting
	}
	d, err := DecodePositiveMsSetting(store.Raw(ctx, AdminBackendChannelTestProbeTimeoutKey))
	if err != nil {
		return DefaultChannelTestProbeTimeoutSetting
	}
	return d
}
