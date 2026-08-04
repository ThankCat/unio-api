package appsettings

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestGatewaySettingsRegistered 验证 gateway 配置全部注册且默认值可通过自身校验。
func TestGatewaySettingsRegistered(t *testing.T) {
	reg := DefaultRegistry()
	keys := []string{
		GatewayCircuitBreakerKey,
		GatewayRouteRateLimitDefaultsKey,
		GatewayStreamIdleTimeoutKey,
		GatewayChannelCooldownKey,
		GatewayCredential401ThresholdKey,
		GatewayDefaultResponseTimeoutKey,
		GatewayDefaultFirstTokenTimeoutKey,
		GatewayCapacityWaitTimeoutKey,
		GatewayRoutingStickyKey,
		GatewayConcurrencyDefaultsKey,
		GatewayRoutingBalanceKey,
	}
	for _, key := range keys {
		def, ok := reg.Get(key)
		if !ok {
			t.Fatalf("key %q not registered", key)
		}
		if !def.HotReload {
			t.Errorf("key %q must be hot reloadable", key)
		}
		if def.Category != "gateway" {
			t.Errorf("key %q category = %q, want gateway", key, def.Category)
		}
		if def.Validate == nil {
			t.Fatalf("key %q has no validator", key)
		}
		if err := def.Validate(def.Default); err != nil {
			t.Errorf("key %q default fails own validation: %v", key, err)
		}
	}
}

func TestRoutingBalanceSettingsRoundTrip(t *testing.T) {
	want := RoutingBalanceSettings{
		CostWeightPct:                30,
		ConcurrencyWeightPct:         20,
		TTFTWeightPct:                20,
		ErrorRateWeightPct:           20,
		PriorityWeightPct:            10,
		TTFTWindow:                   20 * time.Minute,
		TTFTPenaltyUnit:              500 * time.Millisecond,
		TTFTPenaltyPointsPerUnit:     1.5,
		ErrorWindow:                  20 * time.Minute,
		ErrorPenaltyPointsPerPercent: 3,
	}
	got, err := DecodeRoutingBalanceSettings(encodeRoutingBalanceSettings(want))
	if err != nil || got != want {
		t.Fatalf("round trip got %+v err=%v, want %+v", got, err, want)
	}

	const validBody = `"ttft_window_ms":1800000,"ttft_penalty_unit_ms":1000,"ttft_penalty_points_per_unit":2.5,"error_window_ms":1800000,"error_penalty_points_per_percent":2.5`
	invalid := []string{
		// 权重之和不为 100。
		`{"cost_weight_pct":25,"concurrency_weight_pct":20,"ttft_weight_pct":25,"error_rate_weight_pct":20,"priority_weight_pct":5,` + validBody + `}`,
		// 负权重。
		`{"cost_weight_pct":-5,"concurrency_weight_pct":25,"ttft_weight_pct":25,"error_rate_weight_pct":30,"priority_weight_pct":25,` + validBody + `}`,
		// 窗口/惩罚单位必须为正。
		`{"cost_weight_pct":25,"concurrency_weight_pct":20,"ttft_weight_pct":25,"error_rate_weight_pct":20,"priority_weight_pct":10,"ttft_window_ms":0,"ttft_penalty_unit_ms":1000,"ttft_penalty_points_per_unit":2.5,"error_window_ms":1800000,"error_penalty_points_per_percent":2.5}`,
		`{"cost_weight_pct":25,"concurrency_weight_pct":20,"ttft_weight_pct":25,"error_rate_weight_pct":20,"priority_weight_pct":10,"ttft_window_ms":1800000,"ttft_penalty_unit_ms":0,"ttft_penalty_points_per_unit":2.5,"error_window_ms":1800000,"error_penalty_points_per_percent":2.5}`,
		`{"cost_weight_pct":25,"concurrency_weight_pct":20,"ttft_weight_pct":25,"error_rate_weight_pct":20,"priority_weight_pct":10,"ttft_window_ms":1800000,"ttft_penalty_unit_ms":1000,"ttft_penalty_points_per_unit":0,"error_window_ms":1800000,"error_penalty_points_per_percent":2.5}`,
		`{"cost_weight_pct":25,"concurrency_weight_pct":20,"ttft_weight_pct":25,"error_rate_weight_pct":20,"priority_weight_pct":10,"ttft_window_ms":1800000,"ttft_penalty_unit_ms":1000,"ttft_penalty_points_per_unit":2.5,"error_window_ms":0,"error_penalty_points_per_percent":2.5}`,
		`{"cost_weight_pct":25,"concurrency_weight_pct":20,"ttft_weight_pct":25,"error_rate_weight_pct":20,"priority_weight_pct":10,"ttft_window_ms":1800000,"ttft_penalty_unit_ms":1000,"ttft_penalty_points_per_unit":2.5,"error_window_ms":1800000,"error_penalty_points_per_percent":0}`,
		// 未知字段一律拒绝（无兼容分支）。
		`{"cost_weight_pct":25,"concurrency_weight_pct":20,"ttft_weight_pct":25,"error_rate_weight_pct":20,"priority_weight_pct":10,` + validBody + `,"bogus":1}`,
		// 旧 objective/legacy 结构必须被拒绝。
		`{"economic_weight_pct":45,"health_weight_pct":25,"capacity_weight_pct":20,"priority_weight_pct":10,"ttft_target_ms":2000,"ttft_weight":0.35,"ttft_ewma_alpha":0.2}`,
		`{"ttft_target_ms":2000,"ttft_weight":0.35,"cost_weight":0.9,"minimum_routing_factor":0.05,"ttft_ewma_alpha":0.2}`,
	}
	for _, raw := range invalid {
		if _, err := DecodeRoutingBalanceSettings([]byte(raw)); err == nil {
			t.Fatalf("invalid routing balance accepted: %s", raw)
		}
	}
}

// TestRoutingBalanceObjectiveDefaults 冻结 §14.6 五项默认权重与惩罚参数，并确认 canonical JSON 只发新字段。
func TestRoutingBalanceObjectiveDefaults(t *testing.T) {
	defaults := DefaultRoutingBalanceSettings()
	if defaults.CostWeightPct != 25 || defaults.ConcurrencyWeightPct != 20 ||
		defaults.TTFTWeightPct != 25 || defaults.ErrorRateWeightPct != 20 || defaults.PriorityWeightPct != 10 {
		t.Fatalf("unexpected objective defaults: %+v", defaults)
	}
	if defaults.TTFTWindow != 30*time.Minute || defaults.ErrorWindow != 30*time.Minute {
		t.Fatalf("unexpected sample windows: %+v", defaults)
	}
	if defaults.TTFTPenaltyUnit != time.Second || defaults.TTFTPenaltyPointsPerUnit != 2.5 ||
		defaults.ErrorPenaltyPointsPerPercent != 2.5 {
		t.Fatalf("unexpected penalty defaults: %+v", defaults)
	}
	raw := string(encodeRoutingBalanceSettings(defaults))
	for _, want := range []string{
		`"cost_weight_pct":25`, `"concurrency_weight_pct":20`, `"ttft_weight_pct":25`,
		`"error_rate_weight_pct":20`, `"priority_weight_pct":10`,
		`"ttft_window_ms":1800000`, `"error_window_ms":1800000`,
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("canonical routing balance JSON missing %s: %s", want, raw)
		}
	}
	// 旧字段一律不得出现（单一 canonical，无兼容）。
	for _, forbidden := range []string{"economic_weight_pct", "health_weight_pct", "capacity_weight_pct",
		"ttft_target_ms", "ttft_ewma_alpha", "cost_weight\"", "minimum_routing_factor"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("canonical routing balance JSON must not emit %s: %s", forbidden, raw)
		}
	}
}

// TestDurationKeysCarryMsSuffix 时长类标量 key 必须带 _ms 后缀,与值单位(毫秒)自证一致。
func TestDurationKeysCarryMsSuffix(t *testing.T) {
	for _, key := range []string{GatewayStreamIdleTimeoutKey, GatewayDefaultResponseTimeoutKey} {
		if !strings.HasSuffix(key, "_ms") {
			t.Errorf("duration key %q must end with _ms", key)
		}
	}
}

func TestConcurrencyDefaultsSettingsRoundTrip(t *testing.T) {
	want := ConcurrencyDefaultsSettings{KeyLimit: 5, ChannelLimit: 12}
	got, err := DecodeConcurrencyDefaultsSettings(encodeConcurrencyDefaultsSettings(want))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, want)
	}

	if _, err := DecodeConcurrencyDefaultsSettings([]byte(`{"key_limit":-1,"channel_limit":0}`)); err == nil {
		t.Fatal("negative key_limit must be rejected")
	}
	if _, err := DecodeConcurrencyDefaultsSettings([]byte(`{"key_limit":0,"channel_limit":0,"bogus":1}`)); err == nil {
		t.Fatal("unknown field must be rejected")
	}
	if def := DefaultConcurrencyDefaultsSettings(); def.KeyLimit != 0 || def.ChannelLimit != 0 {
		t.Fatalf("default must be disabled (0/0), got %+v", def)
	}
}

func TestCircuitBreakerSettingsRoundTrip(t *testing.T) {
	want := DefaultCircuitBreakerSettings()
	want.Enabled = false
	want.Window = 45 * time.Second
	want.MinRequests = 10
	want.FailureRatio = 0.8
	got, err := DecodeCircuitBreakerSettings(encodeCircuitBreakerSettings(want))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestCircuitBreakerSettingsDefaultMatchesRuntimeContract(t *testing.T) {
	got := DefaultCircuitBreakerSettings()
	if !got.Enabled || got.Window != 30*time.Second || got.MinRequests != 20 || got.FailureRatio != 0.5 {
		t.Fatalf("breaker defaults mismatch: %+v", got)
	}
	if got.ConsecutiveFailures != 3 || got.ConsecutiveWindow != 10*time.Second || got.HalfOpenSuccesses != 2 {
		t.Fatalf("breaker trigger defaults mismatch: %+v", got)
	}
	if got.AttemptPermitTTL != 30*time.Second || got.AttemptPermitRenewInterval != 10*time.Second || got.AttemptPermitTerminalTTL != 5*time.Minute {
		t.Fatalf("permit defaults mismatch: %+v", got)
	}
	if !reflect.DeepEqual(got.OpenDurations, []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute}) {
		t.Fatalf("breaker backoff defaults mismatch: %+v", got)
	}
}

// TestCircuitBreakerEncodesMsIntegers 验证时长以 int 毫秒持久化(单位内嵌字段名,拒绝字符串)。
func TestCircuitBreakerEncodesMsIntegers(t *testing.T) {
	raw := string(encodeCircuitBreakerSettings(DefaultCircuitBreakerSettings()))
	if !strings.Contains(raw, `"window_ms":30000`) || !strings.Contains(raw, `"open_durations_ms":[15000,30000,60000,120000,300000]`) {
		t.Fatalf("durations must encode as int ms: %s", raw)
	}
	if strings.Contains(raw, "open_duration_ms") {
		t.Fatalf("legacy open_duration_ms must not be encoded: %s", raw)
	}
}

func TestCircuitBreakerSettingsRejectsInvalid(t *testing.T) {
	cases := map[string]func(*CircuitBreakerSettings){
		"zero window":        func(s *CircuitBreakerSettings) { s.Window = 0 },
		"one min request":    func(s *CircuitBreakerSettings) { s.MinRequests = 1 },
		"ratio zero":         func(s *CircuitBreakerSettings) { s.FailureRatio = 0 },
		"consecutive zero":   func(s *CircuitBreakerSettings) { s.ConsecutiveFailures = 0 },
		"renew too slow":     func(s *CircuitBreakerSettings) { s.AttemptPermitRenewInterval = 11 * time.Second },
		"terminal too short": func(s *CircuitBreakerSettings) { s.AttemptPermitTerminalTTL = time.Second },
		"no open durations":  func(s *CircuitBreakerSettings) { s.OpenDurations = nil },
		"descending backoff": func(s *CircuitBreakerSettings) { s.OpenDurations = []time.Duration{time.Minute, time.Second} },
		"distinct too low":   func(s *CircuitBreakerSettings) { s.ProviderAmbiguousDistinctChannels = 1 },
	}
	for name, mutate := range cases {
		settings := DefaultCircuitBreakerSettings()
		mutate(&settings)
		if _, err := DecodeCircuitBreakerSettings(encodeCircuitBreakerSettings(settings)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
	for _, legacy := range []string{
		`{"enabled":true,"window_ms":30000,"min_requests":20,"failure_ratio":0.5,"open_duration_ms":30000}`,
		`{"enabled":true,"window":"30s","min_requests":20,"failure_ratio":0.5,"open_duration":"30s"}`,
	} {
		if _, err := DecodeCircuitBreakerSettings([]byte(legacy)); err == nil {
			t.Errorf("legacy breaker shape accepted: %s", legacy)
		}
	}
}

func TestRateLimitDefaultsRoundTrip(t *testing.T) {
	want := RateLimitDefaultsSettings{RPM: 120, TPM: 90000, RPD: 5000}
	got, err := DecodeRateLimitDefaultsSettings(encodeRateLimitDefaultsSettings(want))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestRateLimitDefaultsDefaultIsUnlimited(t *testing.T) {
	got := DefaultRateLimitDefaultsSettings()
	want := RateLimitDefaultsSettings{RPM: 0, TPM: 0, RPD: 0}
	if got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
}

func TestRateLimitDefaultsRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"negative rpm":  `{"rpm":-1,"tpm":0,"rpd":0}`,
		"negative tpm":  `{"rpm":60,"tpm":-1,"rpd":0}`,
		"negative rpd":  `{"rpm":60,"tpm":0,"rpd":-1}`,
		"legacy policy": `{"rpm":60,"tpm":0,"rpd":0,"failure_policy":"fail_open"}`,
	}
	for name, raw := range cases {
		if _, err := DecodeRateLimitDefaultsSettings([]byte(raw)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestChannelCooldownRoundTrip(t *testing.T) {
	want := ChannelCooldownSettings{Cooldown: 10 * time.Second, Cap: time.Minute}
	got, err := DecodeChannelCooldownSettings(encodeChannelCooldownSettings(want))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestChannelCooldownEncodesMsIntegers(t *testing.T) {
	raw := string(encodeChannelCooldownSettings(DefaultChannelCooldownSettings()))
	if raw != `{"cooldown_ms":5000,"cap_ms":300000}` {
		t.Fatalf("cooldown must encode as int ms: %s", raw)
	}
}

func TestChannelCooldownAllowsZeroRejectsNegative(t *testing.T) {
	// 0 合法(关闭默认冷却/不封顶),负数非法——对齐原 env 校验。
	got, err := DecodeChannelCooldownSettings([]byte(`{"cooldown_ms":0,"cap_ms":0}`))
	if err != nil {
		t.Fatalf("zero should be valid: %v", err)
	}
	if got.Cooldown != 0 || got.Cap != 0 {
		t.Fatalf("got %+v, want zeros", got)
	}
	if _, err := DecodeChannelCooldownSettings([]byte(`{"cooldown_ms":-5000,"cap_ms":0}`)); err == nil {
		t.Fatal("negative cooldown: expected error")
	}
	if _, err := DecodeChannelCooldownSettings([]byte(`{"cooldown_ms":5000,"cap_ms":-1}`)); err == nil {
		t.Fatal("negative cap: expected error")
	}
	if _, err := DecodeChannelCooldownSettings([]byte(`{"cooldown":"5s","cap":"5m"}`)); err == nil {
		t.Fatal("legacy string fields: expected error")
	}
}

func TestScalarSettingsDecode(t *testing.T) {
	d, err := DecodePositiveMsSetting([]byte(`600000`))
	if err != nil || d != 10*time.Minute {
		t.Fatalf("duration = %v, err = %v", d, err)
	}
	if _, err := DecodePositiveMsSetting([]byte(`0`)); err == nil {
		t.Fatal("zero ms: expected error")
	}
	if _, err := DecodePositiveMsSetting([]byte(`-1`)); err == nil {
		t.Fatal("negative ms: expected error")
	}
	if _, err := DecodePositiveMsSetting([]byte(`"10m"`)); err == nil {
		t.Fatal("string duration: expected error (must be int ms)")
	}

	n, err := DecodePositiveIntSetting([]byte(`3`))
	if err != nil || n != 3 {
		t.Fatalf("int = %d, err = %v", n, err)
	}
	if _, err := DecodePositiveIntSetting([]byte(`0`)); err == nil {
		t.Fatal("zero threshold: expected error")
	}
	if _, err := DecodePositiveIntSetting([]byte(`"3"`)); err == nil {
		t.Fatal("string int: expected error")
	}
}

// TestMsScalarDefaults 验证毫秒标量默认值的编码是纯整数。
func TestMsScalarDefaults(t *testing.T) {
	if got := string(encodeMsSetting(DefaultStreamIdleTimeoutSetting)); got != "600000" {
		t.Fatalf("stream idle default = %s, want 600000", got)
	}
	if got := string(encodeMsSetting(DefaultResponseTimeoutSetting)); got != "200000" {
		t.Fatalf("channel timeout default = %s, want 200000", got)
	}
}

// TestTimeoutAndCapacityWaitDefaultsAreFrozen 冻结 §11.3/§9.4 的最终默认值。
// 本次改造只新增 60s 首字保护；200s 响应默认与 10min 流式 idle 默认必须保持不变，
// 否则迁移会意外缩短合法长请求。
func TestTimeoutAndCapacityWaitDefaultsAreFrozen(t *testing.T) {
	reg := DefaultRegistry()
	for key, want := range map[string]string{
		GatewayDefaultResponseTimeoutKey:   "200000",
		GatewayDefaultFirstTokenTimeoutKey: "60000",
		GatewayStreamIdleTimeoutKey:        "600000",
		GatewayCapacityWaitTimeoutKey:      "1000",
	} {
		def, ok := reg.Get(key)
		if !ok {
			t.Fatalf("key %q is not registered", key)
		}
		if got := string(def.Default); got != want {
			t.Errorf("default for %q = %s, want %s", key, got, want)
		}
		if !def.HotReload {
			t.Errorf("key %q must be hot reloadable", key)
		}
	}
}

// TestCapacityWaitAcceptsZeroButRejectsNegative 验证 0 是「关闭短等」的合法配置，负数不是。
func TestCapacityWaitAcceptsZeroButRejectsNegative(t *testing.T) {
	def, ok := DefaultRegistry().Get(GatewayCapacityWaitTimeoutKey)
	if !ok {
		t.Fatal("capacity wait key is not registered")
	}
	if err := def.Validate([]byte(`0`)); err != nil {
		t.Fatalf("0 must be accepted to disable the wait: %v", err)
	}
	if err := def.Validate([]byte(`-1`)); err == nil {
		t.Fatal("negative capacity wait must be rejected")
	}
}

// TestTimeoutDefaultsRejectNonPositive 冻结 §11.3：0/负数不表示「无限」，必须被拒绝。
func TestTimeoutDefaultsRejectNonPositive(t *testing.T) {
	for _, key := range []string{
		GatewayDefaultResponseTimeoutKey,
		GatewayDefaultFirstTokenTimeoutKey,
		GatewayStreamIdleTimeoutKey,
	} {
		def, ok := DefaultRegistry().Get(key)
		if !ok {
			t.Fatalf("key %q is not registered", key)
		}
		for _, raw := range []string{`0`, `-1`} {
			if err := def.Validate([]byte(raw)); err == nil {
				t.Errorf("key %q accepted %s; 0/negative must never disable the protection", key, raw)
			}
		}
	}
}
