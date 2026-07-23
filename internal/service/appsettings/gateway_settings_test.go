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
		GatewayChannelRateLimitDefaultsKey,
		GatewayStreamIdleTimeoutKey,
		GatewayChannelCooldownKey,
		GatewayCredential401ThresholdKey,
		GatewayDefaultChannelTimeoutKey,
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
		TTFTTarget:           2500 * time.Millisecond,
		TTFTWeight:           0.4,
		CostWeight:           0.6,
		MinimumRoutingFactor: 0.08,
		TTFTEWMAAlpha:        0.25,
		Enabled:              true,
		WeightByRemaining:    true,
	}
	got, err := DecodeRoutingBalanceSettings(encodeRoutingBalanceSettings(want))
	if err != nil || got != want {
		t.Fatalf("round trip got %+v err=%v, want %+v", got, err, want)
	}
	invalid := []string{
		`{"ttft_target_ms":0,"ttft_weight":0.35,"minimum_routing_factor":0.05,"ttft_ewma_alpha":0.2}`,
		`{"ttft_target_ms":2000,"ttft_weight":1.1,"minimum_routing_factor":0.05,"ttft_ewma_alpha":0.2}`,
		`{"ttft_target_ms":2000,"ttft_weight":0.35,"minimum_routing_factor":0,"ttft_ewma_alpha":0.2}`,
		`{"ttft_target_ms":2000,"ttft_weight":0.35,"minimum_routing_factor":0.05,"ttft_ewma_alpha":0}`,
		`{"ttft_target_ms":2000,"ttft_weight":0.35,"cost_weight":-0.1,"minimum_routing_factor":0.05,"ttft_ewma_alpha":0.2}`,
		`{"ttft_target_ms":2000,"ttft_weight":0.35,"cost_weight":1.1,"minimum_routing_factor":0.05,"ttft_ewma_alpha":0.2}`,
		`{"ttft_target_ms":2000,"ttft_weight":0.35,"cost_weight":null,"minimum_routing_factor":0.05,"ttft_ewma_alpha":0.2}`,
		`{"ttft_target_ms":2000,"cost_weight":0.5,"minimum_routing_factor":0.05,"ttft_ewma_alpha":0.2}`,
		`{"enabled":true,"weight_by_remaining":true}`,
		`{"ttft_target_ms":2000,"ttft_weight":0.35,"minimum_routing_factor":0.05,"ttft_ewma_alpha":0.2,"bogus":1}`,
	}
	for _, raw := range invalid {
		if _, err := DecodeRoutingBalanceSettings([]byte(raw)); err == nil {
			t.Fatalf("invalid routing balance accepted: %s", raw)
		}
	}
}

func TestRoutingBalanceCostWeightDefaultsAndLegacyCompatibility(t *testing.T) {
	defaults := DefaultRoutingBalanceSettings()
	if defaults.CostWeight != 0.5 {
		t.Fatalf("fresh default cost weight = %v, want 0.5", defaults.CostWeight)
	}
	if raw := string(encodeRoutingBalanceSettings(defaults)); !strings.Contains(raw, `"cost_weight":0.5`) {
		t.Fatalf("new encoder must include cost_weight: %s", raw)
	}

	legacy := []byte(`{"ttft_target_ms":2000,"ttft_weight":0.35,"minimum_routing_factor":0.05,"ttft_ewma_alpha":0.2}`)
	decoded, err := DecodeRoutingBalanceSettings(legacy)
	if err != nil {
		t.Fatalf("decode legacy routing balance: %v", err)
	}
	if decoded.CostWeight != 0 {
		t.Fatalf("legacy payload cost weight = %v, want revision-stable 0", decoded.CostWeight)
	}
	if raw := string(encodeRoutingBalanceSettings(decoded)); !strings.Contains(raw, `"cost_weight":0`) {
		t.Fatalf("canonical legacy payload must explicitly encode cost_weight=0: %s", raw)
	}
}

// TestDurationKeysCarryMsSuffix 时长类标量 key 必须带 _ms 后缀,与值单位(毫秒)自证一致。
func TestDurationKeysCarryMsSuffix(t *testing.T) {
	for _, key := range []string{GatewayStreamIdleTimeoutKey, GatewayDefaultChannelTimeoutKey} {
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

func TestCircuitBreakerSettingsDefaultMatchesP4Decision(t *testing.T) {
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
	if got.EndpointStatusBatchMax != 256 || !reflect.DeepEqual(got.OpenDurations, []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute}) {
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
		"batch too large":    func(s *CircuitBreakerSettings) { s.EndpointStatusBatchMax = 1025 },
		"no open durations":  func(s *CircuitBreakerSettings) { s.OpenDurations = nil },
		"descending backoff": func(s *CircuitBreakerSettings) { s.OpenDurations = []time.Duration{time.Minute, time.Second} },
		"distinct too low":   func(s *CircuitBreakerSettings) { s.EndpointAmbiguousDistinctChannels = 1 },
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
	if got := string(encodeMsSetting(DefaultChannelTimeoutSetting)); got != "30000" {
		t.Fatalf("channel timeout default = %s, want 30000", got)
	}
}
