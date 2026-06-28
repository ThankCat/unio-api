package responses

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizeInlineErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "upstream provider error"},
		{"whitespace only", "   \n\t ", "upstream provider error"},
		{"redacts url", "failed to reach https://api.internal.example.com/v1/chat now", "failed to reach [redacted] now"},
		{"redacts http url", "dial http://10.0.0.5:8080/upstream refused", "dial [redacted] refused"},
		{"collapses whitespace", "line1\n\n   line2\tline3", "line1 line2 line3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeInlineErrorMessage(tc.in); got != tc.want {
				t.Fatalf("sanitizeInlineErrorMessage(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeInlineErrorMessageTruncates(t *testing.T) {
	long := strings.Repeat("a", maxInlineErrorMessageLen+50)
	got := sanitizeInlineErrorMessage(long)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected truncation suffix, got %q", got)
	}
	if len([]rune(got)) > maxInlineErrorMessageLen+1 {
		t.Fatalf("message not truncated: len=%d", len([]rune(got)))
	}
}

func TestSanitizedResponsesFailedEventStripsURL(t *testing.T) {
	data := sanitizedResponsesFailedEvent("resp_9", "server_error", "boom at https://secret.upstream.example/x")
	var env struct {
		Type     string `json:"type"`
		Response struct {
			ID    string `json:"id"`
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("invalid json: %v (%s)", err, data)
	}
	if env.Type != eventResponseFailed {
		t.Fatalf("type = %q", env.Type)
	}
	if env.Response.ID != "resp_9" || env.Response.Error.Code != "server_error" {
		t.Fatalf("unexpected envelope: %+v", env)
	}
	if strings.Contains(env.Response.Error.Message, "https://") {
		t.Fatalf("url leaked: %q", env.Response.Error.Message)
	}
}

func TestSanitizedResponsesErrorEventStripsURL(t *testing.T) {
	data := sanitizedResponsesErrorEvent("rate_limit", "slow down see https://dashboard.upstream.example")
	var env struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("invalid json: %v (%s)", err, data)
	}
	if env.Type != eventError || env.Code != "rate_limit" {
		t.Fatalf("unexpected envelope: %+v", env)
	}
	if strings.Contains(env.Message, "https://") {
		t.Fatalf("url leaked: %q", env.Message)
	}
}
