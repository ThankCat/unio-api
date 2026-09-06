package sessionhint

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestOpenAISessionHintPrecedence 验证 OpenAI 提取顺序：body prompt_cache_key 优先，header 回退。
func TestOpenAISessionHintPrecedence(t *testing.T) {
	ctx := WithClientSessionID(context.Background(), "header-session")

	bodyKey := "body-cache-key"
	if got := OpenAISessionHint(ctx, &bodyKey).Key; got != "body-cache-key" {
		t.Fatalf("expected body key to win, got %q", got)
	}

	if got := OpenAISessionHint(ctx, nil).Key; got != "header-session" {
		t.Fatalf("expected header fallback, got %q", got)
	}

	empty := "   "
	if got := OpenAISessionHint(ctx, &empty).Key; got != "header-session" {
		t.Fatalf("expected blank body key to fall back to header, got %q", got)
	}

	if got := OpenAISessionHint(context.Background(), nil).Key; got != "" {
		t.Fatalf("expected empty without any signal, got %q", got)
	}

	// 超长键拒绝（R6 第一道闸）。
	oversized := strings.Repeat("x", 600)
	if got := OpenAISessionHint(context.Background(), &oversized).Key; got != "" {
		t.Fatalf("expected oversized key rejected, got %q", got)
	}
}

// TestUpstreamAffinityContextRoundTrip 验证 service → adapter 的会话亲和载体：写入即可读回，
// 空白会话键不写入（本请求不向上游表达会话身份），未写入返回零值。
func TestUpstreamAffinityContextRoundTrip(t *testing.T) {
	ctx := WithUpstreamAffinity(context.Background(), UpstreamAffinity{SessionKey: "  thread-1  ", APIKeyID: 42})
	if got := UpstreamAffinityFromContext(ctx); got != (UpstreamAffinity{SessionKey: "thread-1", APIKeyID: 42}) {
		t.Fatalf("unexpected affinity: %+v", got)
	}

	blank := WithUpstreamAffinity(context.Background(), UpstreamAffinity{SessionKey: "   ", APIKeyID: 42})
	if got := UpstreamAffinityFromContext(blank); got != (UpstreamAffinity{}) {
		t.Fatalf("blank session key must not be stored, got %+v", got)
	}
	if got := UpstreamAffinityFromContext(context.Background()); got != (UpstreamAffinity{}) {
		t.Fatalf("missing affinity must be zero value, got %+v", got)
	}
}

func TestOpenAISessionHintReportsSelectedSource(t *testing.T) {
	ctx := WithClientSessionID(context.Background(), "header-session")
	bodyKey := "body-cache-key"
	if got := OpenAISessionHint(ctx, &bodyKey); got != (Hint{Key: bodyKey, Source: "prompt_cache_key"}) {
		t.Fatalf("unexpected body hint: %+v", got)
	}
	if got := OpenAISessionHint(ctx, nil); got != (Hint{Key: "header-session", Source: "session_id_header"}) {
		t.Fatalf("unexpected header hint: %+v", got)
	}
}

// TestAnthropicSessionHintPrecedence 验证 Anthropic 提取顺序：会话头优先，metadata.user_id 严格回退（R9）。
func TestAnthropicSessionHintPrecedence(t *testing.T) {
	meta := json.RawMessage(`{"user_id":"user_abc123_account_11111111-2222-3333-4444-555555555555_session_d81712fa-1111-2222-3333-44445555bca9"}`)

	ctx := WithClientSessionID(context.Background(), "d81712fa-head")
	if got := AnthropicSessionHint(ctx, meta).Key; got != "d81712fa-head" {
		t.Fatalf("expected header to win, got %q", got)
	}

	if got := AnthropicSessionHint(context.Background(), meta).Key; got != "d81712fa-1111-2222-3333-44445555bca9" {
		t.Fatalf("expected metadata session suffix, got %q", got)
	}
}

func TestAnthropicSessionHintReportsSelectedSource(t *testing.T) {
	meta := json.RawMessage(`{"user_id":"user_abc_session_d81712fa-1111-2222-3333-44445555bca9"}`)
	ctx := WithClientSessionID(context.Background(), "d81712fa-head")
	if got := AnthropicSessionHint(ctx, meta); got != (Hint{Key: "d81712fa-head", Source: "claude_session_id_header"}) {
		t.Fatalf("unexpected header hint: %+v", got)
	}
	if got := AnthropicSessionHint(context.Background(), meta); got != (Hint{Key: "d81712fa-1111-2222-3333-44445555bca9", Source: "metadata_user_id"}) {
		t.Fatalf("unexpected metadata hint: %+v", got)
	}
}

// TestAnthropicSessionHintEmbeddedJSONMetadata 验证 Claude Code 2.1.104 起的
// user_id 内嵌 JSON 形态（抓包实测：account_uuid 为空串）也能回退出会话键。
func TestAnthropicSessionHintEmbeddedJSONMetadata(t *testing.T) {
	meta := json.RawMessage(`{"user_id":"{\"device_id\":\"a7ce301933cdf60596384ad006f6ad6c80fd27a968af1ab0043c7ff20a5151ea\",\"account_uuid\":\"\",\"session_id\":\"1adb4182-525a-4f47-95ec-a02595470274\"}"}`)

	if got := AnthropicSessionHint(context.Background(), meta).Key; got != "1adb4182-525a-4f47-95ec-a02595470274" {
		t.Fatalf("expected embedded json session_id, got %q", got)
	}

	// 来源标识描述字段来源而非格式变体，两种形态共用 metadata_user_id。
	want := Hint{Key: "1adb4182-525a-4f47-95ec-a02595470274", Source: "metadata_user_id"}
	if got := AnthropicSessionHint(context.Background(), meta); got != want {
		t.Fatalf("unexpected embedded json hint: %+v", got)
	}

	// 头仍然优先于 body 回退。
	ctx := WithClientSessionID(context.Background(), "1adb4182-head")
	if got := AnthropicSessionHint(ctx, meta).Key; got != "1adb4182-head" {
		t.Fatalf("expected header to win over embedded json, got %q", got)
	}
}

// TestAnthropicSessionHintStrictParse 验证严格解析：格式不符即不粘、绝不猜（R9）。
func TestAnthropicSessionHintStrictParse(t *testing.T) {
	cases := []struct {
		name string
		meta string
	}{
		{"no metadata", ""},
		{"invalid json", `{user_id}`},
		{"no user_id", `{"other":"x"}`},
		{"no session marker", `{"user_id":"user_abc_account_123"}`},
		{"non-uuid session suffix", `{"user_id":"user_abc_session_!!bad??"}`},
		{"empty session suffix", `{"user_id":"user_abc_session_"}`},
		{"embedded json without session_id", `{"user_id":"{\"device_id\":\"abc\",\"account_uuid\":\"\"}"}`},
		{"embedded json non-uuid session_id", `{"user_id":"{\"session_id\":\"!!bad??\"}"}`},
		{"embedded json empty session_id", `{"user_id":"{\"session_id\":\"\"}"}`},
		{"malformed embedded json", `{"user_id":"{\"session_id\":"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var meta json.RawMessage
			if tc.meta != "" {
				meta = json.RawMessage(tc.meta)
			}
			if got := AnthropicSessionHint(context.Background(), meta).Key; got != "" {
				t.Fatalf("expected strict parse failure to yield empty key, got %q", got)
			}
		})
	}
}
