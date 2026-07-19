package appsettings

import (
	"testing"
	"time"
)

func TestRoutingTraceSettingsRoundTrip(t *testing.T) {
	want := RoutingTraceSettings{
		SampleRate: 0.125, Retention: 14 * 24 * time.Hour,
		CleanupInterval: 30 * time.Minute, CleanupBatchSize: 250,
	}
	got, err := DecodeRoutingTraceSettings(encodeRoutingTraceSettings(want))
	if err != nil {
		t.Fatalf("decode routing trace settings: %v", err)
	}
	if got != want {
		t.Fatalf("unexpected round trip: got=%+v want=%+v", got, want)
	}
}

func TestRoutingTraceSettingsRejectInvalidValues(t *testing.T) {
	invalid := []string{
		`{"sample_rate":1.1,"retention_days":7,"cleanup_interval_ms":1000,"cleanup_batch_size":100}`,
		`{"sample_rate":0.05,"retention_days":0,"cleanup_interval_ms":1000,"cleanup_batch_size":100}`,
		`{"sample_rate":0.05,"retention_days":7,"cleanup_interval_ms":0,"cleanup_batch_size":100}`,
		`{"sample_rate":0.05,"retention_days":7,"cleanup_interval_ms":1000,"cleanup_batch_size":10001}`,
		`{"sample_rate":0.05,"retention_days":7,"cleanup_interval_ms":1000,"cleanup_batch_size":100,"unknown":true}`,
	}
	for _, raw := range invalid {
		if _, err := DecodeRoutingTraceSettings([]byte(raw)); err == nil {
			t.Fatalf("expected invalid settings to fail: %s", raw)
		}
	}
}
