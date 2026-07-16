package chatcompletions

import (
	"encoding/json"
	"reflect"
	"testing"

	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
)

// TestDropUnsupportedTypedFields 验证 typed Drop 字段被清零/剔除，且 dropped 列表按字母序稳定。
func TestDropUnsupportedTypedFields(t *testing.T) {
	penalty := 0.5
	parallel := true
	req := chatcompletionsadapter.ChatRequest{
		FrequencyPenalty:  &penalty,
		PresencePenalty:   &penalty,
		ParallelToolCalls: &parallel,
		ResponseFormat:    &chatcompletionsadapter.ChatResponseFormat{Type: "json_schema"},
		Tools: []chatcompletionsadapter.ChatTool{
			{Type: "function"},
			{Type: "custom"},
		},
	}

	cleaned, dropped := dropUnsupported(req)

	if cleaned.FrequencyPenalty != nil || cleaned.PresencePenalty != nil || cleaned.ParallelToolCalls != nil {
		t.Fatalf("expected penalties/parallel dropped: %+v", cleaned)
	}
	if cleaned.ResponseFormat != nil {
		t.Fatal("expected json_schema response_format dropped")
	}
	if len(cleaned.Tools) != 1 || cleaned.Tools[0].Type != "function" {
		t.Fatalf("expected only function tool kept: %+v", cleaned.Tools)
	}

	assertDropped(t, dropped,
		"frequency_penalty",
		"parallel_tool_calls",
		"presence_penalty",
		"response_format",
		"tools",
	)
}

// TestDropUnsupportedFiltersExtensions 验证 Extensions 仅保留白名单，其余非 OpenAI 规范的
// vendor key 一律 Drop（OpenAI 规范字段现在走 typed 字段，见 TestDropUnsupportedTypedSpecFields）。
func TestDropUnsupportedFiltersExtensions(t *testing.T) {
	req := chatcompletionsadapter.ChatRequest{
		Extensions: map[string]json.RawMessage{
			"thinking":      json.RawMessage(`{"type":"enabled"}`),
			"logprobs":      json.RawMessage(`true`),
			"top_logprobs":  json.RawMessage(`5`),
			"vendor_hint":   json.RawMessage(`{"x":1}`),
			"custom_widget": json.RawMessage(`true`),
		},
	}

	cleaned, dropped := dropUnsupported(req)

	for _, keep := range []string{"thinking", "logprobs", "top_logprobs"} {
		if _, ok := cleaned.Extensions[keep]; !ok {
			t.Fatalf("expected extension %q kept", keep)
		}
	}
	for _, drop := range []string{"vendor_hint", "custom_widget"} {
		if _, ok := cleaned.Extensions[drop]; ok {
			t.Fatalf("expected extension %q dropped", drop)
		}
	}

	assertDropped(t, dropped, "custom_widget", "vendor_hint")
}

// TestDropUnsupportedTypedSpecFields 验证新 typed 化的 OpenAI 规范字段按 mapping §2 Drop，
// 且 logprobs / top_logprobs 作为 Pass 保留。
func TestDropUnsupportedTypedSpecFields(t *testing.T) {
	n := 2
	seed := 42
	store := true
	logprobs := true
	topLogprobs := 5
	serviceTier := "auto"
	verbosity := "low"
	cacheKey := "ck"
	cacheRetention := "24h"
	safetyID := "sid"

	req := chatcompletionsadapter.ChatRequest{
		N:                    &n,
		Seed:                 &seed,
		Store:                &store,
		Logprobs:             &logprobs,
		TopLogprobs:          &topLogprobs,
		ServiceTier:          &serviceTier,
		Verbosity:            &verbosity,
		PromptCacheKey:       &cacheKey,
		PromptCacheRetention: &cacheRetention,
		SafetyIdentifier:     &safetyID,
		Modalities:           []string{"text", "audio"},
		LogitBias:            json.RawMessage(`{"50256":-100}`),
		Audio:                json.RawMessage(`{"voice":"alloy"}`),
		Prediction:           json.RawMessage(`{"type":"content"}`),
		Metadata:             json.RawMessage(`{"k":"v"}`),
		WebSearchOptions:     json.RawMessage(`{}`),
	}

	cleaned, dropped := dropUnsupported(req)

	// Pass：logprobs / top_logprobs 必须保留进 upstream wire。
	if cleaned.Logprobs == nil || !*cleaned.Logprobs {
		t.Fatalf("expected logprobs kept, got %#v", cleaned.Logprobs)
	}
	if cleaned.TopLogprobs == nil || *cleaned.TopLogprobs != 5 {
		t.Fatalf("expected top_logprobs kept, got %#v", cleaned.TopLogprobs)
	}

	// Drop：其余 typed 规范字段必须清零。
	if cleaned.N != nil || cleaned.Seed != nil || cleaned.Store != nil ||
		cleaned.ServiceTier != nil || cleaned.Verbosity != nil ||
		cleaned.PromptCacheKey != nil || cleaned.PromptCacheRetention != nil ||
		cleaned.SafetyIdentifier != nil || len(cleaned.Modalities) != 0 ||
		len(cleaned.LogitBias) != 0 || len(cleaned.Audio) != 0 ||
		len(cleaned.Prediction) != 0 || len(cleaned.Metadata) != 0 ||
		len(cleaned.WebSearchOptions) != 0 {
		t.Fatalf("expected unsupported typed spec fields dropped, got %#v", cleaned)
	}

	assertDropped(t, dropped,
		"audio",
		"logit_bias",
		"metadata",
		"modalities",
		"n",
		"prediction",
		"prompt_cache_key",
		"prompt_cache_retention",
		"safety_identifier",
		"seed",
		"service_tier",
		"store",
		"verbosity",
		"web_search_options",
	)
}

// TestDropUnsupportedRemovesMultimodalContentParts 验证多模态 content part 被剔除，且不修改调用方原始 content。
func TestDropUnsupportedRemovesMultimodalContentParts(t *testing.T) {
	content := json.RawMessage(`[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"http://x"}}]`)
	req := chatcompletionsadapter.ChatRequest{
		Messages: []chatcompletionsadapter.ChatMessage{
			{Role: "user", Content: content},
		},
	}

	cleaned, dropped := dropUnsupported(req)

	var parts []map[string]any
	if err := json.Unmarshal(cleaned.Messages[0].Content, &parts); err != nil {
		t.Fatalf("unmarshal cleaned content: %v", err)
	}
	if len(parts) != 1 || parts[0]["type"] != "text" {
		t.Fatalf("expected only text part kept: %+v", parts)
	}

	assertDropped(t, dropped, "messages")

	var orig []map[string]any
	if err := json.Unmarshal(content, &orig); err != nil {
		t.Fatalf("unmarshal original content: %v", err)
	}
	if len(orig) != 2 {
		t.Fatalf("expected original content untouched, got %d parts", len(orig))
	}
}

// TestDropUnsupportedKeepsSupportedRequest 验证全部受支持的请求不产生任何 Drop。
func TestDropUnsupportedKeepsSupportedRequest(t *testing.T) {
	temp := 0.7
	req := chatcompletionsadapter.ChatRequest{
		Model:          "deepseek-v4-flash",
		Temperature:    &temp,
		Tools:          []chatcompletionsadapter.ChatTool{{Type: "function"}},
		ResponseFormat: &chatcompletionsadapter.ChatResponseFormat{Type: "json_object"},
		Extensions: map[string]json.RawMessage{
			"thinking": json.RawMessage(`{"type":"enabled"}`),
		},
	}

	cleaned, dropped := dropUnsupported(req)

	if len(dropped) != 0 {
		t.Fatalf("expected no dropped fields, got %v", dropped)
	}
	if cleaned.ResponseFormat == nil || cleaned.ResponseFormat.Type != "json_object" {
		t.Fatal("expected json_object response_format kept")
	}
}

// TestDropUnsupportedAdaptsUserToUserID 验证合法 user 被 Adapt 成 DeepSeek user_id（进 Extensions），
// 且不计入 dropped 审计，标准 user 字段被清空。
func TestDropUnsupportedAdaptsUserToUserID(t *testing.T) {
	user := "tenant_42-abc"
	req := chatcompletionsadapter.ChatRequest{User: &user}

	cleaned, dropped := dropUnsupported(req)

	if cleaned.User != nil {
		t.Fatalf("expected standard user cleared, got %#v", cleaned.User)
	}
	got, ok := cleaned.Extensions["user_id"]
	if !ok {
		t.Fatalf("expected user_id injected into extensions, got %#v", cleaned.Extensions)
	}
	if string(got) != `"tenant_42-abc"` {
		t.Fatalf("user_id = %s", got)
	}
	if len(dropped) != 0 {
		t.Fatalf("expected no dropped fields for valid adapt, got %v", dropped)
	}
}

// TestDropUnsupportedDropsInvalidUser 验证含非法字符的 user 无法无损 Adapt 时被 Drop，不发送 user_id。
func TestDropUnsupportedDropsInvalidUser(t *testing.T) {
	user := "user@example.com"
	req := chatcompletionsadapter.ChatRequest{User: &user}

	cleaned, dropped := dropUnsupported(req)

	if cleaned.User != nil {
		t.Fatalf("expected standard user cleared, got %#v", cleaned.User)
	}
	if _, ok := cleaned.Extensions["user_id"]; ok {
		t.Fatal("expected invalid user not adapted to user_id")
	}
	assertDropped(t, dropped, "user")
}

// TestDropUnsupportedCollapsesMaxCompletionTokens 验证路线 C 下沉后 DeepSeek 行为零回归：
// max_completion_tokens 塌缩为 max_tokens（同时传两者时优先 completion tokens），Adapt 不计入 dropped。
func TestDropUnsupportedCollapsesMaxCompletionTokens(t *testing.T) {
	maxTokens := 10
	maxCompletionTokens := 20
	req := chatcompletionsadapter.ChatRequest{
		MaxTokens:           &maxTokens,
		MaxCompletionTokens: &maxCompletionTokens,
	}

	cleaned, dropped := dropUnsupported(req)

	if cleaned.MaxCompletionTokens != nil {
		t.Fatalf("expected max_completion_tokens collapsed, got %v", *cleaned.MaxCompletionTokens)
	}
	if cleaned.MaxTokens == nil || *cleaned.MaxTokens != 20 {
		t.Fatalf("expected max_tokens = 20 (completion tokens win conflict), got %v", cleaned.MaxTokens)
	}
	if len(dropped) != 0 {
		t.Fatalf("collapse is adapt, expected no dropped fields, got %v", dropped)
	}

	// 仅传 max_tokens 时原样保留。
	only := 8
	cleaned, _ = dropUnsupported(chatcompletionsadapter.ChatRequest{MaxTokens: &only})
	if cleaned.MaxTokens == nil || *cleaned.MaxTokens != 8 {
		t.Fatalf("expected plain max_tokens kept, got %v", cleaned.MaxTokens)
	}

	// 调用方原始请求不被修改。
	if *req.MaxTokens != 10 || req.MaxCompletionTokens == nil {
		t.Fatalf("dropUnsupported mutated caller request: %+v", req)
	}
}

// TestDropUnsupportedCollapsesDeveloperRole 验证路线 C 下沉后 developer role 塌缩为 system，
// 保持消息相对顺序、Adapt 不计入 dropped，且不修改调用方底层 messages 数组。
func TestDropUnsupportedCollapsesDeveloperRole(t *testing.T) {
	messages := []chatcompletionsadapter.ChatMessage{
		{Role: "developer", Content: json.RawMessage(`"rules"`)},
		{Role: "system", Content: json.RawMessage(`"sys"`)},
		{Role: "user", Content: json.RawMessage(`"hi"`)},
	}
	req := chatcompletionsadapter.ChatRequest{Messages: messages}

	cleaned, dropped := dropUnsupported(req)

	wantRoles := []string{"system", "system", "user"}
	for i, want := range wantRoles {
		if cleaned.Messages[i].Role != want {
			t.Fatalf("messages[%d].Role = %q, want %q", i, cleaned.Messages[i].Role, want)
		}
	}
	if len(dropped) != 0 {
		t.Fatalf("role collapse is adapt, expected no dropped fields, got %v", dropped)
	}
	if messages[0].Role != "developer" {
		t.Fatalf("dropUnsupported mutated caller messages: %+v", messages[0])
	}

	// 无 developer 消息时不复制、原样返回。
	plain := []chatcompletionsadapter.ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}
	cleaned, _ = dropUnsupported(chatcompletionsadapter.ChatRequest{Messages: plain})
	if &cleaned.Messages[0] != &plain[0] {
		t.Fatal("expected messages slice reused when no developer role present")
	}
}

func assertDropped(t *testing.T, got []string, want ...string) {
	t.Helper()

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dropped = %v, want %v", got, want)
	}
}
