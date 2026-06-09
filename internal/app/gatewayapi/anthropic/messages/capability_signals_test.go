package messages

import (
	"encoding/json"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/capability"
)

func msgBoolPtr(v bool) *bool { return &v }

// TestCapabilitySignalsBaseline 验证最小请求只产出文本基线。
func TestCapabilitySignalsBaseline(t *testing.T) {
	req := MessageRequest{
		Model:    "claude-3-5-sonnet",
		Messages: []Message{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}

	got := capability.Infer(capabilitySignals(req))
	want := capability.NewSet(capability.KeyTextInput, capability.KeyTextOutput)
	if !got.Equal(want) {
		t.Fatalf("baseline = %v, want %v", got.Keys(), want.Keys())
	}
}

// TestCapabilitySignalsStreamNoUsage 验证流式不会派生 stream.usage（Anthropic 无 include_usage）。
func TestCapabilitySignalsStreamNoUsage(t *testing.T) {
	req := MessageRequest{
		Model:    "claude-3-5-sonnet",
		Messages: []Message{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		Stream:   msgBoolPtr(true),
	}

	got := capability.Infer(capabilitySignals(req))
	if !got.Has(capability.KeyStream) {
		t.Fatalf("expected stream, got %v", got.Keys())
	}
	if got.Has(capability.KeyStreamUsage) {
		t.Fatalf("anthropic must not infer stream.usage, got %v", got.Keys())
	}
}

// TestCapabilitySignalsMultimodalBlocks 验证 image/document block 识别图片/文件输入。
func TestCapabilitySignalsMultimodalBlocks(t *testing.T) {
	content := json.RawMessage(`[
		{"type":"text","text":"see"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"x"}},
		{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"y"}}
	]`)
	req := MessageRequest{
		Model:    "claude-3-5-sonnet",
		Messages: []Message{{Role: "user", Content: content}},
	}

	got := capability.Infer(capabilitySignals(req))
	if !got.Has(capability.KeyImageInput) || !got.Has(capability.KeyFileInput) {
		t.Fatalf("expected image.input + file.input, got %v", got.Keys())
	}
}

// TestCapabilitySignalsThinking 验证 thinking enabled/adaptive 派生 reasoning.budget。
func TestCapabilitySignalsThinking(t *testing.T) {
	for _, thinkingType := range []string{"enabled", "adaptive"} {
		req := MessageRequest{
			Model:    "claude-3-7-sonnet",
			Messages: []Message{{Role: "user", Content: json.RawMessage(`"hi"`)}},
			Thinking: json.RawMessage(`{"type":"` + thinkingType + `","budget_tokens":1024}`),
		}
		if !capabilitySignals(req).ReasoningBudget {
			t.Fatalf("thinking %q should set reasoning budget", thinkingType)
		}
	}

	disabled := MessageRequest{
		Model:    "claude-3-7-sonnet",
		Messages: []Message{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		Thinking: json.RawMessage(`{"type":"disabled"}`),
	}
	if capabilitySignals(disabled).ReasoningBudget {
		t.Fatal("disabled thinking must not set reasoning budget")
	}
}

// TestCapabilitySignalsTools 验证 custom 工具→function、server tool→内置工具、tool_choice any→required。
func TestCapabilitySignalsTools(t *testing.T) {
	req := MessageRequest{
		Model:    "claude-3-5-sonnet",
		Messages: []Message{{Role: "user", Content: json.RawMessage(`"go"`)}},
		Stream:   msgBoolPtr(true),
		Tools: json.RawMessage(`[
			{"name":"get_weather","input_schema":{"type":"object"}},
			{"type":"web_search_20250305","name":"web_search"},
			{"type":"code_execution_20250522","name":"code_execution"}
		]`),
		ToolChoice: json.RawMessage(`{"type":"any"}`),
	}

	got := capability.Infer(capabilitySignals(req))
	want := []capability.Key{
		capability.KeyToolsFunction,
		capability.KeyToolsBuiltinWebSearch,
		capability.KeyToolsBuiltinCodeInterpreter,
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

// TestCapabilitySignalsUnmappedServerTool 验证未建模 server tool 不产出额外能力。
func TestCapabilitySignalsUnmappedServerTool(t *testing.T) {
	req := MessageRequest{
		Model:    "claude-3-5-sonnet",
		Messages: []Message{{Role: "user", Content: json.RawMessage(`"go"`)}},
		Tools:    json.RawMessage(`[{"type":"text_editor_20250124","name":"str_replace"}]`),
	}

	got := capability.Infer(capabilitySignals(req))
	want := capability.NewSet(capability.KeyTextInput, capability.KeyTextOutput)
	if !got.Equal(want) {
		t.Fatalf("unmapped server tool should not add caps, got %v", got.Keys())
	}
}

// FuzzCapabilitySignals 验证任意 JSON 请求体不 panic，且只产出注册表内能力与文本基线。
func FuzzCapabilitySignals(f *testing.F) {
	f.Add(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	f.Add(`{"model":"m","messages":[{"role":"user","content":[{"type":"image"}]}],"x":1}`)
	f.Add(`{"model":"m","thinking":{"type":"enabled"},"tools":[{"type":"weird"}]}`)
	f.Add(`{"model":"m","stream":true,"tool_choice":{"type":"tool","name":"f"}}`)

	f.Fuzz(func(t *testing.T, body string) {
		var req MessageRequest
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
