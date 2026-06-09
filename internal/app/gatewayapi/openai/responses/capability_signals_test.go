package responses

import (
	"encoding/json"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/capability"
)

func respBoolPtr(v bool) *bool    { return &v }
func respStrPtr(v string) *string { return &v }

// TestCapabilitySignalsBaseline 验证字符串 input 的最小请求只产出文本基线。
func TestCapabilitySignalsBaseline(t *testing.T) {
	text := "hi"
	req := ResponsesRequest{
		Model: "gpt-5-codex",
		Input: ResponsesInput{Text: &text},
	}

	got := capability.Infer(capabilitySignals(req))
	want := capability.NewSet(capability.KeyTextInput, capability.KeyTextOutput)
	if !got.Equal(want) {
		t.Fatalf("baseline = %v, want %v", got.Keys(), want.Keys())
	}
}

// TestCapabilitySignalsMultimodalInput 验证 input item content parts 识别图片/音频/文件。
func TestCapabilitySignalsMultimodalInput(t *testing.T) {
	req := ResponsesRequest{
		Model: "gpt-5",
		Input: ResponsesInput{Items: []ResponseInputItem{
			{
				Type:    "message",
				Role:    "user",
				Content: json.RawMessage(`[{"type":"input_text","text":"see"},{"type":"input_image","image_url":"x"},{"type":"input_audio"},{"type":"input_file"}]`),
			},
		}},
	}

	got := capability.Infer(capabilitySignals(req))
	for _, key := range []capability.Key{capability.KeyImageInput, capability.KeyAudioInput, capability.KeyFileInput} {
		if !got.Has(key) {
			t.Fatalf("expected %s present, got %v", key, got.Keys())
		}
	}
}

// TestCapabilitySignalsReasoningAndStream 验证 reasoning.effort/summary 与流式派生。
func TestCapabilitySignalsReasoningAndStream(t *testing.T) {
	req := ResponsesRequest{
		Model:     "gpt-5-codex",
		Input:     ResponsesInput{Text: respStrPtr("go")},
		Stream:    respBoolPtr(true),
		Reasoning: &ResponsesReasoning{Effort: respStrPtr("high"), Summary: respStrPtr("auto")},
	}

	got := capability.Infer(capabilitySignals(req))
	for _, key := range []capability.Key{capability.KeyStream, capability.KeyReasoningEffort, capability.KeyReasoningSummary} {
		if !got.Has(key) {
			t.Fatalf("expected %s present, got %v", key, got.Keys())
		}
	}

	// GAP-12-012：reasoning.effort 档位值要抽进 RequestLimits 供闸门 limited 超限判定。
	if limits := RequestLimits(req); limits.ReasoningEffort != "high" {
		t.Fatalf("RequestLimits.ReasoningEffort = %q, want high", limits.ReasoningEffort)
	}
}

// TestCapabilitySignalsReasoningEffortNone 验证 effort="none" 不算请求 reasoning effort 能力。
func TestCapabilitySignalsReasoningEffortNone(t *testing.T) {
	req := ResponsesRequest{
		Model:     "gpt-5-codex",
		Input:     ResponsesInput{Text: respStrPtr("go")},
		Reasoning: &ResponsesReasoning{Effort: respStrPtr("none")},
	}

	if capabilitySignals(req).ReasoningEffort {
		t.Fatal(`reasoning effort "none" must not set reasoning effort signal`)
	}
}

// TestCapabilitySignalsTools 验证 function/custom/namespace/内置工具映射与 tool_choice=required。
func TestCapabilitySignalsTools(t *testing.T) {
	req := ResponsesRequest{
		Model:  "gpt-5",
		Input:  ResponsesInput{Text: respStrPtr("go")},
		Stream: respBoolPtr(true),
		Tools: []ResponsesTool{
			{Type: "function", Name: "shell"},
			{Type: "custom"},
			{Type: "namespace", Name: "mcp__github"},
			{Type: "web_search"},
			{Type: "file_search"},
			{Type: "code_interpreter"},
			{Type: "computer_use_preview"},
			{Type: "image_generation"},
		},
		ParallelToolCalls: respBoolPtr(true),
		ToolChoice:        json.RawMessage(`"required"`),
	}

	got := capability.Infer(capabilitySignals(req))
	want := []capability.Key{
		capability.KeyToolsFunction,
		capability.KeyToolsCustom,
		capability.KeyToolsBuiltinMCP,
		capability.KeyToolsBuiltinWebSearch,
		capability.KeyToolsBuiltinFileSearch,
		capability.KeyToolsBuiltinCodeInterpreter,
		capability.KeyToolsBuiltinComputerUse,
		capability.KeyToolsBuiltinImageGeneration,
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

// TestCapabilitySignalsToolChoiceObjectRequired 验证对象形态 tool_choice（required/any/tool）
// 同样推断出 tool.choice.required，与 mapResponsesRequestToChat 的归一一致，避免漏置信号。
func TestCapabilitySignalsToolChoiceObjectRequired(t *testing.T) {
	for _, raw := range []string{
		`{"type":"required"}`,
		`{"type":"any"}`,
		`{"type":"tool","name":"shell"}`,
	} {
		req := ResponsesRequest{
			Model:      "gpt-5",
			Input:      ResponsesInput{Text: respStrPtr("go")},
			Tools:      []ResponsesTool{{Type: "function", Name: "shell"}},
			ToolChoice: json.RawMessage(raw),
		}
		got := capability.Infer(capabilitySignals(req))
		if !got.Has(capability.KeyToolsChoiceRequired) {
			t.Fatalf("tool_choice %s: expected tool.choice.required, got %v", raw, got.Keys())
		}
	}

	// 具名 function（非强制）不应推断为 required。
	named := ResponsesRequest{
		Model:      "gpt-5",
		Input:      ResponsesInput{Text: respStrPtr("go")},
		Tools:      []ResponsesTool{{Type: "function", Name: "shell"}},
		ToolChoice: json.RawMessage(`{"type":"function","name":"shell"}`),
	}
	if capability.Infer(capabilitySignals(named)).Has(capability.KeyToolsChoiceRequired) {
		t.Fatal("named function tool_choice should not infer tool.choice.required")
	}
}

// TestCapabilitySignalsServerStateAndEncrypted 验证 store/text.format/include 派生能力。
func TestCapabilitySignalsServerStateAndEncrypted(t *testing.T) {
	req := ResponsesRequest{
		Model:          "gpt-5",
		Input:          ResponsesInput{Text: respStrPtr("go")},
		Store:          respBoolPtr(true),
		Text:           &ResponsesTextControls{Format: json.RawMessage(`{"type":"json_schema","name":"s"}`)},
		Include:        []string{"reasoning.encrypted_content"},
		PromptCacheKey: respStrPtr("k1"),
		ServiceTier:    respStrPtr("flex"),
	}

	got := capability.Infer(capabilitySignals(req))
	want := []capability.Key{
		capability.KeyServerStateStore,
		capability.KeyResponseFormatJSONSchema,
		capability.KeyResponsesEncryptedContent,
		capability.KeyPromptCache,
		capability.KeyServiceTier,
	}
	for _, key := range want {
		if !got.Has(key) {
			t.Fatalf("expected %s present, got %v", key, got.Keys())
		}
	}
}

// FuzzCapabilitySignals 验证任意 JSON 请求体不 panic，且只产出注册表内能力与文本基线。
func FuzzCapabilitySignals(f *testing.F) {
	f.Add(`{"model":"m","input":"hi"}`)
	f.Add(`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_image"}]}],"x":1}`)
	f.Add(`{"model":"m","reasoning":{"effort":"high"},"tools":[{"type":"weird"}]}`)
	f.Add(`{"model":"m","stream":true,"include":["reasoning.encrypted_content"]}`)

	f.Fuzz(func(t *testing.T, body string) {
		var req ResponsesRequest
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
