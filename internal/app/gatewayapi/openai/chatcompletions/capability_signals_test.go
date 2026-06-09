package chatcompletions

import (
	"encoding/json"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/capability"
)

// TestCapabilitySignalsBaseline 验证最小请求只产出文本基线。
func TestCapabilitySignalsBaseline(t *testing.T) {
	req := ChatCompletionRequest{
		Model:    "deepseek-chat",
		Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
	}

	got := capability.Infer(capabilitySignals(req))
	want := capability.NewSet(capability.KeyTextInput, capability.KeyTextOutput)
	if !got.Equal(want) {
		t.Fatalf("baseline = %v, want %v", got.Keys(), want.Keys())
	}
}

// TestCapabilitySignalsStreamUsage 验证流式 + include_usage 派生 stream/stream.usage。
func TestCapabilitySignalsStreamUsage(t *testing.T) {
	req := ChatCompletionRequest{
		Model:         "deepseek-chat",
		Messages:      []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		Stream:        boolPtr(true),
		StreamOptions: &ChatCompletionStreamOptions{IncludeUsage: boolPtr(true)},
	}

	got := capability.Infer(capabilitySignals(req))
	if !got.Has(capability.KeyStream) || !got.Has(capability.KeyStreamUsage) {
		t.Fatalf("expected stream + stream.usage, got %v", got.Keys())
	}
}

// TestCapabilitySignalsMultimodalContent 验证 content part 识别图片/音频/文件输入。
func TestCapabilitySignalsMultimodalContent(t *testing.T) {
	content := json.RawMessage(`[
		{"type":"text","text":"look"},
		{"type":"image_url","image_url":{"url":"https://x/y.png"}},
		{"type":"input_audio","input_audio":{"data":"...","format":"wav"}},
		{"type":"file","file":{"file_id":"f1"}}
	]`)
	req := ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []ChatMessage{{Role: "user", Content: content}},
	}

	got := capability.Infer(capabilitySignals(req))
	for _, key := range []capability.Key{capability.KeyImageInput, capability.KeyAudioInput, capability.KeyFileInput} {
		if !got.Has(key) {
			t.Fatalf("expected %s present, got %v", key, got.Keys())
		}
	}
}

// TestCapabilitySignalsToolsAndChoice 验证 function/custom 工具、parallel 与 tool_choice=required。
func TestCapabilitySignalsToolsAndChoice(t *testing.T) {
	req := ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"go"`)}},
		Stream:   boolPtr(true),
		Tools: []ChatCompletionTool{
			{Type: "function", Function: ChatCompletionFunctionTool{Name: "get_weather"}},
			{Type: "custom"},
		},
		ParallelToolCalls: boolPtr(true),
		ToolChoice:        json.RawMessage(`"required"`),
	}

	got := capability.Infer(capabilitySignals(req))
	want := []capability.Key{
		capability.KeyToolsFunction,
		capability.KeyToolsCustom,
		capability.KeyToolsParallel,
		capability.KeyToolsChoiceRequired,
		capability.KeyStream,
		capability.KeyStreamTools,
	}
	for _, key := range want {
		if !got.Has(key) {
			t.Fatalf("expected %s present, got %v", key, got.Keys())
		}
	}
}

// TestCapabilitySignalsToolChoiceObjectNotRequired 验证对象形态 tool_choice 不算 required。
func TestCapabilitySignalsToolChoiceObjectNotRequired(t *testing.T) {
	req := ChatCompletionRequest{
		Model:      "gpt-4o",
		Messages:   []ChatMessage{{Role: "user", Content: json.RawMessage(`"go"`)}},
		ToolChoice: json.RawMessage(`{"type":"function","function":{"name":"f"}}`),
	}

	if capabilitySignals(req).ToolChoiceRequired {
		t.Fatal("object tool_choice must not be treated as required")
	}
}

// TestCapabilitySignalsMiscFields 验证 reasoning/response_format/prompt_cache 等字段映射。
func TestCapabilitySignalsMiscFields(t *testing.T) {
	effort := "high"
	tier := "flex"
	cacheKey := "k1"
	req := ChatCompletionRequest{
		Model:            "gpt-5",
		Messages:         []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		Modalities:       []string{"text", "audio"},
		ReasoningEffort:  &effort,
		ResponseFormat:   &ChatCompletionResponseFormat{Type: "json_schema"},
		PromptCacheKey:   &cacheKey,
		Logprobs:         boolPtr(true),
		ServiceTier:      &tier,
		Store:            boolPtr(true),
		WebSearchOptions: json.RawMessage(`{}`),
	}

	got := capability.Infer(capabilitySignals(req))
	want := []capability.Key{
		capability.KeyAudioOutput,
		capability.KeyReasoningEffort,
		capability.KeyResponseFormatJSONSchema,
		capability.KeyPromptCache,
		capability.KeyLogprobs,
		capability.KeyServiceTier,
		capability.KeyServerStateStore,
		capability.KeyToolsBuiltinWebSearch,
	}
	for _, key := range want {
		if !got.Has(key) {
			t.Fatalf("expected %s present, got %v", key, got.Keys())
		}
	}

	// GAP-12-012：reasoning_effort 档位值要抽进 RequestLimits 供闸门 limited 超限判定。
	if limits := RequestLimits(req); limits.ReasoningEffort != "high" {
		t.Fatalf("RequestLimits.ReasoningEffort = %q, want high", limits.ReasoningEffort)
	}
}

// FuzzCapabilitySignals 验证任意 JSON 请求体不会 panic，且推断结果只含已注册 key 与文本基线。
//
// 覆盖 ACCEPTANCE「fuzz 确认未识别字段不污染 required set」：未知字段不应产出注册表外能力。
func FuzzCapabilitySignals(f *testing.F) {
	f.Add(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	f.Add(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url"}]}],"unknown_field":123}`)
	f.Add(`{"model":"m","tools":[{"type":"weird"}],"reasoning_effort":""}`)
	f.Add(`{"model":"m","stream":true,"stream_options":{"include_usage":true}}`)

	f.Fuzz(func(t *testing.T, body string) {
		var req ChatCompletionRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			return
		}

		got := capability.Infer(capabilitySignals(req))

		if !got.Has(capability.KeyTextInput) || !got.Has(capability.KeyTextOutput) {
			t.Fatalf("text baseline missing for body %q: %v", body, got.Keys())
		}
		for _, key := range got.Keys() {
			if !capability.IsRegisteredKey(key) {
				t.Fatalf("unregistered key %q inferred from body %q", key, body)
			}
		}
	})
}
