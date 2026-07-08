package appsettings

import (
	"strings"
	"testing"
	"time"
)

// TestGatewaySettingsRegistered 验证 6 组 gateway 配置全部注册且默认值可通过自身校验。
func TestGatewaySettingsRegistered(t *testing.T) {
	reg := DefaultRegistry()
	keys := []string{
		GatewayCircuitBreakerKey,
		GatewayRateLimitDefaultsKey,
		GatewayStreamIdleTimeoutKey,
		GatewayChannelCooldownKey,
		GatewayCredential401ThresholdKey,
		GatewayDefaultChannelTimeoutKey,
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

// TestDurationKeysCarryMsSuffix 时长类标量 key 必须带 _ms 后缀,与值单位(毫秒)自证一致。
func TestDurationKeysCarryMsSuffix(t *testing.T) {
	for _, key := range []string{GatewayStreamIdleTimeoutKey, GatewayDefaultChannelTimeoutKey} {
		if !strings.HasSuffix(key, "_ms") {
			t.Errorf("duration key %q must end with _ms", key)
		}
	}
}

func TestCircuitBreakerSettingsRoundTrip(t *testing.T) {
	want := CircuitBreakerSettings{
		Enabled:      false,
		Window:       45 * time.Second,
		MinRequests:  10,
		FailureRatio: 0.8,
		OpenDuration: time.Minute,
	}
	got, err := DecodeCircuitBreakerSettings(encodeCircuitBreakerSettings(want))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestCircuitBreakerSettingsDefaultMatchesEnvDefaults(t *testing.T) {
	// 与原 CIRCUIT_BREAKER_* env 默认对齐(迁移不改变行为)。
	got := DefaultCircuitBreakerSettings()
	want := CircuitBreakerSettings{
		Enabled:      true,
		Window:       30 * time.Second,
		MinRequests:  20,
		FailureRatio: 0.5,
		OpenDuration: 30 * time.Second,
	}
	if got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
}

// TestCircuitBreakerEncodesMsIntegers 验证时长以 int 毫秒持久化(单位内嵌字段名,拒绝字符串)。
func TestCircuitBreakerEncodesMsIntegers(t *testing.T) {
	raw := string(encodeCircuitBreakerSettings(DefaultCircuitBreakerSettings()))
	if !strings.Contains(raw, `"window_ms":30000`) || !strings.Contains(raw, `"open_duration_ms":30000`) {
		t.Fatalf("durations must encode as int ms: %s", raw)
	}
}

func TestCircuitBreakerSettingsRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"zero window":       `{"enabled":true,"window_ms":0,"min_requests":20,"failure_ratio":0.5,"open_duration_ms":30000}`,
		"negative window":   `{"enabled":true,"window_ms":-1,"min_requests":20,"failure_ratio":0.5,"open_duration_ms":30000}`,
		"zero min_requests": `{"enabled":true,"window_ms":30000,"min_requests":0,"failure_ratio":0.5,"open_duration_ms":30000}`,
		"ratio zero":        `{"enabled":true,"window_ms":30000,"min_requests":20,"failure_ratio":0,"open_duration_ms":30000}`,
		"ratio above one":   `{"enabled":true,"window_ms":30000,"min_requests":20,"failure_ratio":1.5,"open_duration_ms":30000}`,
		"zero open":         `{"enabled":true,"window_ms":30000,"min_requests":20,"failure_ratio":0.5,"open_duration_ms":0}`,
		"string duration":   `{"enabled":true,"window_ms":"30s","min_requests":20,"failure_ratio":0.5,"open_duration_ms":30000}`,
		"legacy field":      `{"enabled":true,"window":"30s","min_requests":20,"failure_ratio":0.5,"open_duration":"30s"}`,
	}
	for name, raw := range cases {
		if _, err := DecodeCircuitBreakerSettings([]byte(raw)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestRateLimitDefaultsRoundTrip(t *testing.T) {
	want := RateLimitDefaultsSettings{RPM: 120, TPM: 90000, RPD: 5000, FailurePolicy: RateLimitFailOpen}
	got, err := DecodeRateLimitDefaultsSettings(encodeRateLimitDefaultsSettings(want))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	if !got.FailOpen() {
		t.Fatal("FailOpen() = false for fail_open policy")
	}
}

func TestRateLimitDefaultsDefaultMatchesEnvDefaults(t *testing.T) {
	got := DefaultRateLimitDefaultsSettings()
	want := RateLimitDefaultsSettings{RPM: 60, TPM: 0, RPD: 0, FailurePolicy: RateLimitFailClosed}
	if got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
	if got.FailOpen() {
		t.Fatal("FailOpen() = true for fail_closed policy")
	}
}

func TestRateLimitDefaultsRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"negative rpm":   `{"rpm":-1,"tpm":0,"rpd":0,"failure_policy":"fail_closed"}`,
		"negative tpm":   `{"rpm":60,"tpm":-1,"rpd":0,"failure_policy":"fail_closed"}`,
		"negative rpd":   `{"rpm":60,"tpm":0,"rpd":-1,"failure_policy":"fail_closed"}`,
		"bad policy":     `{"rpm":60,"tpm":0,"rpd":0,"failure_policy":"explode"}`,
		"missing policy": `{"rpm":60,"tpm":0,"rpd":0,"failure_policy":""}`,
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
