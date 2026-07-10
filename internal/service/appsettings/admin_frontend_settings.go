package appsettings

import (
	"encoding/json"
	"errors"
	"fmt"
)

// 本文件登记 admin_frontend 域(admin 前端消费)的运行时配置。
// 后端职责仅存储+校验+seed+面板下发,**没有 Go 消费方**——前端经 GET /admin/v1/settings
// 拉取该域 value 使用(见 unio-admin 的 useMetricThresholds hook)。
// 前端侧保留一份与本默认值同源同值的 fallback 常量(拉取失败回退);改默认须两处同步。

// AdminFrontendDashboardThresholdsKey 是仪表盘告警灯阈值在 app_settings 中的 key。
const AdminFrontendDashboardThresholdsKey = "admin_frontend.dashboard_thresholds"

// DashboardThresholds 是仪表盘/请求列表的着色档位(时长 int 毫秒,比率 (0,1] 浮点):
// 请求成功率 SLO 参考线与红黄灯、TTFT P95 红黄灯、完成延迟 P95 红黄灯、毛利率「偏薄」警示线。
type DashboardThresholds struct {
	SuccessRateSLO  float64 `json:"success_rate_slo"`
	SuccessRateWarn float64 `json:"success_rate_warn"`
	TTFTWarnMs      int64   `json:"ttft_warn_ms"`
	TTFTDangerMs    int64   `json:"ttft_danger_ms"`
	LatencyWarnMs   int64   `json:"latency_warn_ms"`
	LatencyDangerMs int64   `json:"latency_danger_ms"`
	ProfitThinRate  float64 `json:"profit_thin_rate"`
}

// DefaultDashboardThresholds 与迁移前前端 metrics.ts 硬编码一致。
func DefaultDashboardThresholds() DashboardThresholds {
	return DashboardThresholds{
		SuccessRateSLO:  0.95,
		SuccessRateWarn: 0.80,
		TTFTWarnMs:      5000,
		TTFTDangerMs:    12000,
		LatencyWarnMs:   15000,
		LatencyDangerMs: 30000,
		ProfitThinRate:  0.10,
	}
}

func encodeDashboardThresholds(t DashboardThresholds) json.RawMessage {
	raw, err := json.Marshal(t)
	if err != nil {
		panic(fmt.Sprintf("appsettings: encode dashboard thresholds: %v", err))
	}
	return raw
}

// DecodeDashboardThresholds 解码并校验告警灯阈值(拒绝未知字段;warn 必须低于 danger/slo 档)。
func DecodeDashboardThresholds(raw []byte) (DashboardThresholds, error) {
	var t DashboardThresholds
	if err := strictUnmarshal(raw, &t); err != nil {
		return DashboardThresholds{}, err
	}
	if t.SuccessRateWarn <= 0 || t.SuccessRateSLO > 1 || t.SuccessRateWarn >= t.SuccessRateSLO {
		return DashboardThresholds{}, errors.New("must satisfy 0 < success_rate_warn < success_rate_slo <= 1")
	}
	if t.TTFTWarnMs <= 0 || t.TTFTWarnMs >= t.TTFTDangerMs {
		return DashboardThresholds{}, errors.New("must satisfy 0 < ttft_warn_ms < ttft_danger_ms")
	}
	if t.LatencyWarnMs <= 0 || t.LatencyWarnMs >= t.LatencyDangerMs {
		return DashboardThresholds{}, errors.New("must satisfy 0 < latency_warn_ms < latency_danger_ms")
	}
	if t.ProfitThinRate < 0 || t.ProfitThinRate >= 1 {
		return DashboardThresholds{}, errors.New("profit_thin_rate must be within [0, 1)")
	}
	return t, nil
}

func dashboardThresholdsDefinition() Definition {
	return Definition{
		Key:      AdminFrontendDashboardThresholdsKey,
		Category: "admin_frontend",
		Label:    "仪表盘告警灯阈值",
		Description: "后台前端的着色档位:请求成功率 SLO/警戒(比率)、TTFT 与完成延迟的注意/异常线(毫秒,P95 口径)、" +
			"毛利率偏薄警示线(比率,0=关闭)。仅影响前端展示颜色与参考线,不影响任何后端行为。" +
			"前端拉取失败时回退代码内置默认。",
		HotReload: true,
		Default:   encodeDashboardThresholds(DefaultDashboardThresholds()),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeDashboardThresholds(raw)
			return err
		},
	}
}
