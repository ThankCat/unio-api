package appsettings

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRegistryCategoryMatchesKeyPrefix 断言域约定:每个 key 的 Category 与其前缀一致
// (batch2 §2:域 = Category = key 首个 "." 之前的部分,防手误注册错域)。
func TestRegistryCategoryMatchesKeyPrefix(t *testing.T) {
	for _, def := range DefaultRegistry().List() {
		prefix, _, ok := strings.Cut(def.Key, ".")
		if !ok {
			t.Errorf("key %q has no domain prefix", def.Key)
			continue
		}
		if def.Category != prefix {
			t.Errorf("key %q category = %q, want %q (domain prefix)", def.Key, def.Category, prefix)
		}
	}
}

// TestAdminDomainSettingsRegistered 验证 admin 域配置注册齐全且默认值过自身校验。
func TestAdminDomainSettingsRegistered(t *testing.T) {
	reg := DefaultRegistry()
	for _, key := range []string{
		AdminBackendChannelHealthKey,
		AdminBackendChannelTestProbeTimeoutKey,
		AdminFrontendDashboardThresholdsKey,
	} {
		def, ok := reg.Get(key)
		if !ok {
			t.Fatalf("key %q not registered", key)
		}
		if !def.HotReload {
			t.Errorf("key %q must be hot reloadable", key)
		}
		if err := def.Validate(def.Default); err != nil {
			t.Errorf("key %q default fails own validation: %v", key, err)
		}
	}
}

func TestChannelTestProbeTimeoutDefault(t *testing.T) {
	if DefaultChannelTestProbeTimeoutSetting != 60*time.Second {
		t.Fatalf("default probe timeout = %v, want 60s", DefaultChannelTestProbeTimeoutSetting)
	}
	if got := AdminBackendChannelTestProbeTimeout(context.Background(), nil); got != DefaultChannelTestProbeTimeoutSetting {
		t.Fatalf("nil store = %v, want default", got)
	}
	if !strings.HasSuffix(AdminBackendChannelTestProbeTimeoutKey, "_ms") {
		t.Errorf("duration key %q must end with _ms", AdminBackendChannelTestProbeTimeoutKey)
	}
}

func TestChannelHealthThresholdsRoundTrip(t *testing.T) {
	want := ChannelHealthThresholds{HealthyRate: 0.99, DegradedRate: 0.5}
	got, err := DecodeChannelHealthThresholds(encodeChannelHealthThresholds(want))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestChannelHealthThresholdsDefaultsMatchLegacyConstants(t *testing.T) {
	// 与迁移前散落各包的 0.95/0.80 对齐(迁移不改行为)。
	got := DefaultChannelHealthThresholds()
	if got.HealthyRate != 0.95 || got.DegradedRate != 0.80 {
		t.Fatalf("defaults = %+v, want 0.95/0.80", got)
	}
}

func TestChannelHealthThresholdsRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"degraded zero":       `{"healthy_rate":0.95,"degraded_rate":0}`,
		"degraded negative":   `{"healthy_rate":0.95,"degraded_rate":-0.1}`,
		"healthy above one":   `{"healthy_rate":1.5,"degraded_rate":0.8}`,
		"degraded >= healthy": `{"healthy_rate":0.8,"degraded_rate":0.9}`,
		"equal":               `{"healthy_rate":0.9,"degraded_rate":0.9}`,
		"unknown field":       `{"healthy_rate":0.95,"degraded_rate":0.8,"typo":1}`,
	}
	for name, raw := range cases {
		if _, err := DecodeChannelHealthThresholds([]byte(raw)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

// TestAdminBackendChannelHealthThresholdsNilStore 验证 nil store 回默认
// (admin service 单测传 nil 即走旧硬编码行为)。
func TestAdminBackendChannelHealthThresholdsNilStore(t *testing.T) {
	got := AdminBackendChannelHealthThresholds(context.Background(), nil)
	if got != DefaultChannelHealthThresholds() {
		t.Fatalf("nil store should yield defaults, got %+v", got)
	}
}

func TestDashboardThresholdsRoundTrip(t *testing.T) {
	want := DashboardThresholds{
		SuccessRateSLO:  0.99,
		SuccessRateWarn: 0.7,
		TTFTWarnMs:      1000,
		TTFTDangerMs:    2000,
		LatencyWarnMs:   3000,
		LatencyDangerMs: 9000,
		ProfitThinRate:  0.2,
	}
	got, err := DecodeDashboardThresholds(encodeDashboardThresholds(want))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestDashboardThresholdsDefaultsMatchLegacyFrontend(t *testing.T) {
	// 与迁移前 unio-admin metrics.ts 硬编码一致(0.95/0.80/5s/12s/15s/30s/0.1)。
	got := DefaultDashboardThresholds()
	want := DashboardThresholds{
		SuccessRateSLO:  0.95,
		SuccessRateWarn: 0.80,
		TTFTWarnMs:      5000,
		TTFTDangerMs:    12000,
		LatencyWarnMs:   15000,
		LatencyDangerMs: 30000,
		ProfitThinRate:  0.10,
	}
	if got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
}

func TestDashboardThresholdsRejectsInvalid(t *testing.T) {
	valid := `{"success_rate_slo":0.95,"success_rate_warn":0.8,"ttft_warn_ms":5000,"ttft_danger_ms":12000,"latency_warn_ms":15000,"latency_danger_ms":30000,"profit_thin_rate":0.1}`
	if _, err := DecodeDashboardThresholds([]byte(valid)); err != nil {
		t.Fatalf("valid doc rejected: %v", err)
	}
	cases := map[string]string{
		"warn >= slo":          `{"success_rate_slo":0.8,"success_rate_warn":0.9,"ttft_warn_ms":5000,"ttft_danger_ms":12000,"latency_warn_ms":15000,"latency_danger_ms":30000,"profit_thin_rate":0.1}`,
		"slo above one":        `{"success_rate_slo":1.1,"success_rate_warn":0.8,"ttft_warn_ms":5000,"ttft_danger_ms":12000,"latency_warn_ms":15000,"latency_danger_ms":30000,"profit_thin_rate":0.1}`,
		"ttft warn zero":       `{"success_rate_slo":0.95,"success_rate_warn":0.8,"ttft_warn_ms":0,"ttft_danger_ms":12000,"latency_warn_ms":15000,"latency_danger_ms":30000,"profit_thin_rate":0.1}`,
		"ttft warn >= danger":  `{"success_rate_slo":0.95,"success_rate_warn":0.8,"ttft_warn_ms":12000,"ttft_danger_ms":12000,"latency_warn_ms":15000,"latency_danger_ms":30000,"profit_thin_rate":0.1}`,
		"latency warn>=danger": `{"success_rate_slo":0.95,"success_rate_warn":0.8,"ttft_warn_ms":5000,"ttft_danger_ms":12000,"latency_warn_ms":30000,"latency_danger_ms":30000,"profit_thin_rate":0.1}`,
		"profit thin one":      `{"success_rate_slo":0.95,"success_rate_warn":0.8,"ttft_warn_ms":5000,"ttft_danger_ms":12000,"latency_warn_ms":15000,"latency_danger_ms":30000,"profit_thin_rate":1}`,
		"profit negative":      `{"success_rate_slo":0.95,"success_rate_warn":0.8,"ttft_warn_ms":5000,"ttft_danger_ms":12000,"latency_warn_ms":15000,"latency_danger_ms":30000,"profit_thin_rate":-0.1}`,
		"unknown field":        `{"success_rate_slo":0.95,"success_rate_warn":0.8,"ttft_warn_ms":5000,"ttft_danger_ms":12000,"latency_warn_ms":15000,"latency_danger_ms":30000,"profit_thin_rate":0.1,"typo":1}`,
		"string ms":            `{"success_rate_slo":0.95,"success_rate_warn":0.8,"ttft_warn_ms":"5s","ttft_danger_ms":12000,"latency_warn_ms":15000,"latency_danger_ms":30000,"profit_thin_rate":0.1}`,
	}
	for name, raw := range cases {
		if _, err := DecodeDashboardThresholds([]byte(raw)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

// TestDashboardThresholdsProfitThinZeroAllowed 0=关闭偏薄警示,是合法档位。
func TestDashboardThresholdsProfitThinZeroAllowed(t *testing.T) {
	raw := `{"success_rate_slo":0.95,"success_rate_warn":0.8,"ttft_warn_ms":5000,"ttft_danger_ms":12000,"latency_warn_ms":15000,"latency_danger_ms":30000,"profit_thin_rate":0}`
	if _, err := DecodeDashboardThresholds([]byte(raw)); err != nil {
		t.Fatalf("profit_thin_rate=0 should be valid: %v", err)
	}
}
