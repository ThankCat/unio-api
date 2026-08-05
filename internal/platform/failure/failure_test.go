package failure

import (
	"errors"
	"testing"
)

func TestFailureAccessors(t *testing.T) {
	cause := errors.New("parse failed")
	err := Wrap(
		CodeConfigInvalid,
		cause,
		WithMessage("parse REDIS_DB as int"),
		WithField("config_key", "REDIS_DB"),
		WithField("config_value", ""),
	)

	if CodeOf(err) != CodeConfigInvalid {
		t.Fatalf("expected code %q, got %q", CodeConfigInvalid, CodeOf(err))
	}
	if CategoryOf(err) != CategoryConfig {
		t.Fatalf("expected category %q, got %q", CategoryConfig, CategoryOf(err))
	}
	if !errors.Is(err, cause) {
		t.Fatalf("expected cause %v to be wrapped by %v", cause, err)
	}
	if err.Error() != "parse REDIS_DB as int" {
		t.Fatalf("expected custom message, got %q", err.Error())
	}

	fields := FieldsOf(err)
	if len(fields) != 2 {
		t.Fatalf("expected 2 fields, got %#v", fields)
	}
	if fields[0].Key != "config_key" || fields[0].Value != "REDIS_DB" {
		t.Fatalf("unexpected fields: %#v", fields)
	}
	if fields[1].Key != "config_value" || fields[1].Value != "" {
		t.Fatalf("unexpected fields: %#v", fields)
	}
}

func TestFailureUsesCodeAsDefaultMessage(t *testing.T) {
	err := New(CodeConfigMissing)

	if err.Error() != string(CodeConfigMissing) {
		t.Fatalf("expected code as default message, got %q", err.Error())
	}
}

func TestCategoryOfUnknownError(t *testing.T) {
	if CategoryOf(errors.New("plain error")) != CategoryUnknown {
		t.Fatalf("expected unknown category")
	}
}

func TestCodeCategory(t *testing.T) {
	tests := []struct {
		name string
		code Code
		want Category
	}{
		{name: "config", code: CodeConfigInvalid, want: CategoryConfig},
		{name: "http", code: CodeHTTPInvalidJSONBody, want: CategoryHTTP},
		{name: "api key rate limit", code: CodeRateLimitExceeded, want: CategoryRateLimit},
		{name: "channel rate limit", code: CodeGatewayChannelRateLimited, want: CategoryRateLimit},
		{name: "empty", code: "", want: CategoryUnknown},
		{name: "no separator", code: Code("invalid"), want: CategoryUnknown},
		{name: "bad prefix", code: Code("_invalid"), want: CategoryUnknown},
		{name: "undefined prefix", code: Code("invented_failure"), want: CategoryUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.code.Category(); got != tt.want {
				t.Fatalf("expected category %q, got %q", tt.want, got)
			}
		})
	}
}
