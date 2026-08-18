package system

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/config"
)

func TestSystemConfigHandlerFormatsBodyLimitsAsMiB(t *testing.T) {
	handler := &systemConfigHandler{
		gateway: config.GatewayConfig{MaxUpstreamResponseBytes: 8 << 20},
		http: config.HTTPConfig{
			GatewayMaxJSONBodyBytes:     256 << 20,
			GatewayTextMaxJSONBodyBytes: 32 << 20,
			AdminMaxJSONBodyBytes:       4 << 20,
		},
	}
	recorder := httptest.NewRecorder()
	handler.get(recorder, httptest.NewRequest(http.MethodGet, "/system/config", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body struct {
		Data systemConfigDTO `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	wantValues := map[string]string{
		"GATEWAY_MAX_UPSTREAM_RESPONSE_MB": "8 MiB",
		"GATEWAY_MAX_JSON_BODY_MB":         "256 MiB",
		"GATEWAY_TEXT_MAX_JSON_BODY_MB":    "32 MiB",
		"ADMIN_MAX_JSON_BODY_MB":           "4 MiB",
	}
	for _, group := range body.Data.Groups {
		for _, entry := range group.Entries {
			want, ok := wantValues[entry.Env]
			if !ok {
				continue
			}
			if entry.Value != want {
				t.Errorf("%s value = %q, want %q", entry.Env, entry.Value, want)
			}
			if strings.Contains(entry.Label, "字节") {
				t.Errorf("%s label should be human-readable: %q", entry.Env, entry.Label)
			}
			delete(wantValues, entry.Env)
		}
	}
	if len(wantValues) != 0 {
		t.Fatalf("missing body-limit entries: %v", wantValues)
	}
}

func TestFormatMiBPreservesFractionalValues(t *testing.T) {
	if got := formatMiB(3 << 19); got != "1.5 MiB" {
		t.Fatalf("formatMiB() = %q, want %q", got, "1.5 MiB")
	}
}
