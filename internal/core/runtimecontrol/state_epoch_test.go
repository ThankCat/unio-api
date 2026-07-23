package runtimecontrol_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
)

func TestNewRecoveringStateEpoch(t *testing.T) {
	epoch, err := runtimecontrol.NewRecoveringStateEpoch(runtimecontrol.StateEpochReasonBootstrap)
	if err != nil {
		t.Fatalf("new epoch: %v", err)
	}
	if len(epoch.Epoch) != 32 || epoch.State != runtimecontrol.StateEpochRecovering || epoch.ActivatedAt != nil {
		t.Fatalf("unexpected epoch: %+v", epoch)
	}
	raw, err := epoch.Marshal()
	if err != nil {
		t.Fatalf("marshal epoch: %v", err)
	}
	decoded, err := runtimecontrol.DecodeStateEpoch(raw)
	if err != nil {
		t.Fatalf("decode epoch: %v", err)
	}
	if decoded.Epoch != epoch.Epoch || decoded.Reason != epoch.Reason {
		t.Fatalf("decoded epoch mismatch: got %+v want %+v", decoded, epoch)
	}
}

func TestDecodeStateEpochStrictValidation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "unknown field", raw: json.RawMessage(`{"epoch":"00112233445566778899aabbccddeeff","state":"recovering","reason":"bootstrap","activated_at":null,"extra":true}`)},
		{name: "short epoch", raw: json.RawMessage(`{"epoch":"0011","state":"recovering","reason":"bootstrap","activated_at":null}`)},
		{name: "ready without activation", raw: json.RawMessage(`{"epoch":"00112233445566778899aabbccddeeff","state":"ready","reason":"bootstrap","activated_at":null}`)},
		{name: "recovering with activation", raw: mustJSON(t, map[string]any{"epoch": "00112233445566778899aabbccddeeff", "state": "recovering", "reason": "restore", "activated_at": now})},
		{name: "trailing value", raw: json.RawMessage(`{"epoch":"00112233445566778899aabbccddeeff","state":"recovering","reason":"bootstrap","activated_at":null} {}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := runtimecontrol.DecodeStateEpoch(tc.raw); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestDecodeReadyStateEpoch(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	raw := mustJSON(t, map[string]any{
		"epoch":        "00112233-4455-6677-8899-aabbccddeeff",
		"state":        "ready",
		"reason":       "state_loss",
		"activated_at": now,
	})
	got, err := runtimecontrol.DecodeStateEpoch(raw)
	if err != nil {
		t.Fatalf("decode ready epoch: %v", err)
	}
	if got.ActivatedAt == nil || !got.ActivatedAt.Equal(now) {
		t.Fatalf("activation mismatch: %+v", got.ActivatedAt)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return raw
}
