package appsettings

import (
	"encoding/json"
	"testing"
	"time"
)

func TestGatewayLoggingDebugSessionDefinition(t *testing.T) {
	definition, ok := DefaultRegistry().Get(GatewayLoggingDebugSessionKey)
	if !ok || !definition.HotReload || definition.Category != "gateway" {
		t.Fatalf("definition = %+v, ok=%v", definition, ok)
	}
	value, err := DecodeGatewayLoggingDebugSession(definition.Default)
	if err != nil {
		t.Fatalf("decode default: %v", err)
	}
	if value.SessionID != "" || value.Revision != 0 {
		t.Fatalf("default = %+v", value)
	}
}

func TestDecodeGatewayLoggingDebugSession(t *testing.T) {
	expiresAt := time.Now().Add(15 * time.Minute).UTC().Truncate(time.Second)
	raw, _ := json.Marshal(GatewayLoggingDebugSessionSetting{
		SessionID: "dbg-1", StartedAt: expiresAt.Add(-15 * time.Minute), ExpiresAt: expiresAt,
		Reason: "investigate ttft", EnabledByUserID: 7, Revision: 11,
	})
	value, err := DecodeGatewayLoggingDebugSession(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if value.SessionID != "dbg-1" || !value.ExpiresAt.Equal(expiresAt) || value.Revision != 11 {
		t.Fatalf("value = %+v", value)
	}

	for _, invalid := range []string{
		`{"session_id":"dbg","started_at":"2026-08-01T11:45:00Z","expires_at":"2026-08-01T12:00:00Z","reason":"","enabled_by_user_id":1,"revision":1}`,
		`{"session_id":"dbg","started_at":"2026-08-01T11:45:00Z","expires_at":"2026-08-01T12:00:00Z","reason":"bad\nreason","enabled_by_user_id":1,"revision":1}`,
		`{"session_id":"dbg","started_at":"2026-08-01T11:45:00Z","expires_at":"bad","reason":"reason","enabled_by_user_id":1,"revision":1}`,
	} {
		if _, err := DecodeGatewayLoggingDebugSession(json.RawMessage(invalid)); err == nil {
			t.Fatalf("expected error for %s", invalid)
		}
	}
}
