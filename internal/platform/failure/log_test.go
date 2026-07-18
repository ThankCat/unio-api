package failure

import (
	"errors"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestLogFieldsReturnsNilForNilError(t *testing.T) {
	if fields := LogFields(nil); fields != nil {
		t.Fatalf("expected nil fields, got %#v", fields)
	}
}

func TestLogFieldsForPlainError(t *testing.T) {
	err := errors.New("plain error")

	fields := LogFields(err)

	assertLogField(t, fields, "error", "plain error")
	assertNoLogField(t, fields, "error_code")
	assertNoLogField(t, fields, "error_category")
}

func TestLogFieldsForFailure(t *testing.T) {
	err := New(
		CodeConfigInvalid,
		WithMessage("parse REDIS_DB as int"),
	)

	fields := LogFields(err)

	assertLogField(t, fields, "error", err.Error())
	assertLogField(t, fields, "error_code", string(CodeConfigInvalid))
	assertLogField(t, fields, "error_category", string(CategoryConfig))
}

func TestLogFieldsIncludesFailureFields(t *testing.T) {
	err := New(
		CodeConfigInvalid,
		WithField("config_key", "REDIS_DB"),
		WithField("", "ignored"),
	)

	fields := LogFields(err)

	assertLogField(t, fields, "config_key", "REDIS_DB")
	assertNoLogField(t, fields, "")
}

func assertLogField(t *testing.T, fields []zap.Field, key string, want any) {
	t.Helper()

	for _, f := range fields {
		if f.Key != key {
			continue
		}
		got := fieldInterface(f)
		if got != want {
			t.Fatalf("expected log field %s=%#v, got %#v in %#v", key, want, got, fields)
		}
		return
	}

	t.Fatalf("expected log field %s=%#v in %#v", key, want, fields)
}

func assertNoLogField(t *testing.T, fields []zap.Field, key string) {
	t.Helper()

	for _, f := range fields {
		if f.Key == key {
			t.Fatalf("expected no log field %s, got %#v", key, fields)
		}
	}
}

func fieldInterface(f zap.Field) any {
	enc := zapcore.NewMapObjectEncoder()
	f.AddTo(enc)
	return enc.Fields[f.Key]
}
