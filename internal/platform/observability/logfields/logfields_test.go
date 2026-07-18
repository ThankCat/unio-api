package logfields

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func fieldMap(fields []zap.Field) map[string]any {
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range fields {
		f.AddTo(enc)
	}
	return enc.Fields
}

// TestFieldsZapFieldsOmitUnset 验证只输出已设置字段，并由下游填充身份与路由维度。
func TestFieldsZapFieldsOmitUnset(t *testing.T) {
	ctx, fields := NewContext(context.Background(), "corr-1")

	if got := fieldMap(fields.ZapFields()); len(got) != 1 || got["correlation_id"] != "corr-1" {
		t.Fatalf("expected only correlation_id, got %#v", got)
	}

	SetIdentity(ctx, 7, 100)
	SetRequestID(ctx, "req_abc")
	SetModel(ctx, "openai/gpt-4.1")
	SetRouteID(ctx, 2)
	SetUpstreamAttempt(ctx, UpstreamAttempt{
		ModelID:    99,
		Router:     "default-route",
		ProviderID: 9123,
		Provider:   "openai",
		ChannelID:  123,
		Channel:    "main",
	})

	got := fieldMap(fields.ZapFields())
	cases := map[string]any{
		"correlation_id": "corr-1",
		"request_id":     "req_abc",
		"user_id":        int64(7),
		"api_key_id":     int64(100),
		"model":          "openai/gpt-4.1",
		"model_id":       int64(99),
		"route_id":       int64(2),
		"router":         "default-route",
		"provider_id":    int64(9123),
		"provider":       "openai",
		"channel_id":     int64(123),
		"channel":        "main",
	}
	for key, want := range cases {
		if got[key] != want {
			t.Errorf("field %q: got %v, want %v", key, got[key], want)
		}
	}
}

// TestContextHelpersNoopWithoutHolder 验证没有安装 Fields 时 setter 静默忽略，不 panic。
func TestContextHelpersNoopWithoutHolder(t *testing.T) {
	ctx := context.Background()

	SetIdentity(ctx, 1, 3)
	SetRequestID(ctx, "req")
	SetModel(ctx, "m")
	SetRouteID(ctx, 1)
	SetUpstreamAttempt(ctx, UpstreamAttempt{Provider: "p", Channel: "c"})

	if _, ok := FromContext(ctx); ok {
		t.Fatal("expected no Fields in bare context")
	}
}

// TestNilFieldsSettersSafe 验证 nil *Fields 的方法安全。
func TestNilFieldsSettersSafe(t *testing.T) {
	var f *Fields
	f.SetIdentity(1, 3)
	f.SetRequestID("x")
	f.SetModel("m")
	f.SetModelID(1)
	f.SetRouteID(1)
	f.SetRouter("r")
	f.SetUpstreamAttempt(UpstreamAttempt{Provider: "p", Channel: "c"})
	if f.ZapFields() != nil {
		t.Fatal("expected nil ZapFields from nil Fields")
	}
}
