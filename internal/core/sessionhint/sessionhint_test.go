package sessionhint

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestOpenAISessionKeyPrecedence 验证 OpenAI 提取顺序：body prompt_cache_key 优先，header 回退。
func TestOpenAISessionKeyPrecedence(t *testing.T) {
	ctx := WithClientSessionID(context.Background(), "header-session")

	bodyKey := "body-cache-key"
	if got := OpenAISessionKey(ctx, &bodyKey); got != "body-cache-key" {
		t.Fatalf("expected body key to win, got %q", got)
	}

	if got := OpenAISessionKey(ctx, nil); got != "header-session" {
		t.Fatalf("expected header fallback, got %q", got)
	}

	empty := "   "
	if got := OpenAISessionKey(ctx, &empty); got != "header-session" {
		t.Fatalf("expected blank body key to fall back to header, got %q", got)
	}

	if got := OpenAISessionKey(context.Background(), nil); got != "" {
		t.Fatalf("expected empty without any signal, got %q", got)
	}

	// 超长键拒绝（R6 第一道闸）。
	oversized := strings.Repeat("x", 600)
	if got := OpenAISessionKey(context.Background(), &oversized); got != "" {
		t.Fatalf("expected oversized key rejected, got %q", got)
	}
}

// TestAnthropicSessionKeyPrecedence 验证 Anthropic 提取顺序：会话头优先，metadata.user_id 严格回退（R9）。
func TestAnthropicSessionKeyPrecedence(t *testing.T) {
	meta := json.RawMessage(`{"user_id":"user_abc123_account_11111111-2222-3333-4444-555555555555_session_d81712fa-1111-2222-3333-44445555bca9"}`)

	ctx := WithClientSessionID(context.Background(), "d81712fa-head")
	if got := AnthropicSessionKey(ctx, meta); got != "d81712fa-head" {
		t.Fatalf("expected header to win, got %q", got)
	}

	if got := AnthropicSessionKey(context.Background(), meta); got != "d81712fa-1111-2222-3333-44445555bca9" {
		t.Fatalf("expected metadata session suffix, got %q", got)
	}
}

// TestAnthropicSessionKeyStrictParse 验证严格解析：格式不符即不粘、绝不猜（R9）。
func TestAnthropicSessionKeyStrictParse(t *testing.T) {
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var meta json.RawMessage
			if tc.meta != "" {
				meta = json.RawMessage(tc.meta)
			}
			if got := AnthropicSessionKey(context.Background(), meta); got != "" {
				t.Fatalf("expected strict parse failure to yield empty key, got %q", got)
			}
		})
	}
}
