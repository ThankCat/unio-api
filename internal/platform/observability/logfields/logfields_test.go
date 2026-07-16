package logfields

import (
	"context"
	"testing"
)

func attrMap(attrs []any) map[string]any {
	m := make(map[string]any)
	for i := 0; i+1 < len(attrs); i += 2 {
		key, _ := attrs[i].(string)
		m[key] = attrs[i+1]
	}

	return m
}

// TestFieldsAttrsOmitUnset 验证只输出已设置字段，并由下游填充身份与路由维度。
func TestFieldsAttrsOmitUnset(t *testing.T) {
	ctx, fields := NewContext(context.Background(), "corr-1")

	// 仅 correlation_id 已设置时，其他字段不应出现。
	if got := attrMap(fields.Attrs()); len(got) != 1 || got["correlation_id"] != "corr-1" {
		t.Fatalf("expected only correlation_id, got %#v", got)
	}

	SetIdentity(ctx, 7, 100)
	SetRequestID(ctx, "req_abc")
	SetModel(ctx, "openai/gpt-4.1")
	SetRouteID(ctx, 2)
	SetChannel(ctx, "9123", "123")

	got := attrMap(fields.Attrs())
	cases := map[string]any{
		"correlation_id": "corr-1",
		"request_id":     "req_abc",
		"user_id":        int64(7),
		"api_key_id":     int64(100),
		"model":          "openai/gpt-4.1",
		"route_id":       int64(2),
		"provider":       "9123",
		"channel":        "123",
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
	SetChannel(ctx, "p", "c")

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
	f.SetRouteID(1)
	f.SetChannel("p", "c")
	if f.Attrs() != nil {
		t.Fatal("expected nil Attrs from nil Fields")
	}
}
