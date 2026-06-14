package chatcompletions

import (
	"encoding/json"
	"testing"

	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
)

// TestNormalizeReasoningEffort 验证 reasoning_effort 归一为 DeepSeek 支持的 high/max，未知枚举不识别。
func TestNormalizeReasoningEffort(t *testing.T) {
	highCases := []string{"minimal", "low", "medium", "high", "MINIMAL", " low ", "High"}
	for _, in := range highCases {
		got, ok := normalizeReasoningEffort(in)
		if !ok || got != "high" {
			t.Fatalf("normalizeReasoningEffort(%q) = (%q,%v), want (high,true)", in, got, ok)
		}
	}

	maxCases := []string{"xhigh", "max", "XHigh", " max "}
	for _, in := range maxCases {
		got, ok := normalizeReasoningEffort(in)
		if !ok || got != "max" {
			t.Fatalf("normalizeReasoningEffort(%q) = (%q,%v), want (max,true)", in, got, ok)
		}
	}

	for _, in := range []string{"", "none", "ultra", "1"} {
		if got, ok := normalizeReasoningEffort(in); ok {
			t.Fatalf("normalizeReasoningEffort(%q) = (%q,true), want ok=false", in, got)
		}
	}
}

// TestDropUnsupportedAdaptsReasoningEffort 验证 dropUnsupported 把 reasoning_effort 归一为 high/max，
// 归一成功不计入 dropped；未知枚举 Drop 并计入审计。
func TestDropUnsupportedAdaptsReasoningEffort(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"minimal", "high"},
		{"low", "high"},
		{"medium", "high"},
		{"high", "high"},
		{"xhigh", "max"},
		{"max", "max"},
	} {
		effort := tc.in
		cleaned, dropped := dropUnsupported(chatcompletionsadapter.ChatRequest{ReasoningEffort: &effort})
		if cleaned.ReasoningEffort == nil || *cleaned.ReasoningEffort != tc.want {
			t.Fatalf("reasoning_effort %q normalized = %v, want %q", tc.in, cleaned.ReasoningEffort, tc.want)
		}
		for _, d := range dropped {
			if d == "reasoning_effort" {
				t.Fatalf("reasoning_effort %q should adapt, not drop", tc.in)
			}
		}
		// 不修改调用方原值。
		if effort != tc.in {
			t.Fatalf("dropUnsupported mutated caller reasoning_effort: %q -> %q", tc.in, effort)
		}
	}

	unknown := "none"
	cleaned, dropped := dropUnsupported(chatcompletionsadapter.ChatRequest{ReasoningEffort: &unknown})
	if cleaned.ReasoningEffort != nil {
		t.Fatalf("unknown reasoning_effort should be dropped, got %v", *cleaned.ReasoningEffort)
	}
	assertDropped(t, dropped, "reasoning_effort")
}

// TestAdaptThinkingDisabled 验证 ReasoningDisabled 时注入 thinking:disabled，且不覆盖显式 thinking、
// 同时 Drop 矛盾的 reasoning_effort；未禁用时不注入。
func TestAdaptThinkingDisabled(t *testing.T) {
	// ReasoningDisabled → 注入 thinking:disabled。
	cleaned, _ := dropUnsupported(chatcompletionsadapter.ChatRequest{ReasoningDisabled: true})
	if !thinkingTypeEquals(cleaned.Extensions["thinking"], "disabled") {
		t.Fatalf("expected thinking:disabled injected, got %s", cleaned.Extensions["thinking"])
	}

	// 未禁用 → 不注入 thinking。
	noDisable, _ := dropUnsupported(chatcompletionsadapter.ChatRequest{})
	if _, ok := noDisable.Extensions["thinking"]; ok {
		t.Fatal("expected no thinking injected when reasoning not disabled")
	}

	// 显式 thinking 不被覆盖（尊重 chat ingress extra_body.thinking）。
	explicit := chatcompletionsadapter.ChatRequest{
		ReasoningDisabled: true,
		Extensions:        map[string]json.RawMessage{"thinking": json.RawMessage(`{"type":"enabled"}`)},
	}
	cleanedExplicit, _ := dropUnsupported(explicit)
	if !thinkingTypeEquals(cleanedExplicit.Extensions["thinking"], "enabled") {
		t.Fatalf("expected explicit thinking preserved, got %s", cleanedExplicit.Extensions["thinking"])
	}

	// ReasoningDisabled + reasoning_effort → effort 与 thinking:disabled 矛盾，Drop。
	effort := "high"
	cleanedEffort, dropped := dropUnsupported(chatcompletionsadapter.ChatRequest{ReasoningDisabled: true, ReasoningEffort: &effort})
	if cleanedEffort.ReasoningEffort != nil {
		t.Fatalf("expected reasoning_effort dropped when reasoning disabled, got %v", *cleanedEffort.ReasoningEffort)
	}
	if !containsString(dropped, "reasoning_effort") {
		t.Fatalf("expected reasoning_effort in dropped, got %v", dropped)
	}
}

func thinkingTypeEquals(raw json.RawMessage, want string) bool {
	var th struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &th) == nil && th.Type == want
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// TestAdaptLegacyFunctionsConvertsToTools 验证 legacy functions 被 Adapt 成现代 function tools，
// 不计入 dropped 审计。
func TestAdaptLegacyFunctionsConvertsToTools(t *testing.T) {
	req := chatcompletionsadapter.ChatRequest{
		Functions: json.RawMessage(`[{"name":"get_weather","description":"d","parameters":{"type":"object"}}]`),
	}

	cleaned, dropped := adaptLegacyFunctions(req)

	if len(cleaned.Functions) != 0 {
		t.Fatalf("expected legacy functions cleared, got %s", cleaned.Functions)
	}
	if len(cleaned.Tools) != 1 || cleaned.Tools[0].Type != "function" || cleaned.Tools[0].Function.Name != "get_weather" {
		t.Fatalf("expected functions adapted to function tool, got %#v", cleaned.Tools)
	}
	if len(dropped) != 0 {
		t.Fatalf("expected no dropped for successful adapt, got %v", dropped)
	}
}

// TestAdaptLegacyFunctionsDropsWhenToolsPresent 验证已存在 tools 时 legacy functions 被 Drop（不覆盖）。
func TestAdaptLegacyFunctionsDropsWhenToolsPresent(t *testing.T) {
	req := chatcompletionsadapter.ChatRequest{
		Tools:     []chatcompletionsadapter.ChatTool{{Type: "function", Function: chatcompletionsadapter.ChatFunctionTool{Name: "existing"}}},
		Functions: json.RawMessage(`[{"name":"legacy"}]`),
	}

	cleaned, dropped := adaptLegacyFunctions(req)

	if len(cleaned.Tools) != 1 || cleaned.Tools[0].Function.Name != "existing" {
		t.Fatalf("expected existing tools kept, got %#v", cleaned.Tools)
	}
	if len(cleaned.Functions) != 0 {
		t.Fatal("expected legacy functions cleared")
	}
	assertDropped(t, dropped, "functions")
}

// TestAdaptLegacyFunctionCallConvertsStringAndObject 验证 function_call 字符串与对象形态的转换。
func TestAdaptLegacyFunctionCallConvertsStringAndObject(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantChoice string
	}{
		{"none", `"none"`, `"none"`},
		{"auto", `"auto"`, `"auto"`},
		{"named", `{"name":"get_weather"}`, `{"function":{"name":"get_weather"},"type":"function"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := chatcompletionsadapter.ChatRequest{FunctionCall: json.RawMessage(tt.raw)}

			cleaned, dropped := adaptLegacyFunctions(req)

			if len(cleaned.FunctionCall) != 0 {
				t.Fatalf("expected function_call cleared, got %s", cleaned.FunctionCall)
			}
			if len(dropped) != 0 {
				t.Fatalf("expected no dropped, got %v", dropped)
			}
			if string(cleaned.ToolChoice) != tt.wantChoice {
				t.Fatalf("tool_choice = %s, want %s", cleaned.ToolChoice, tt.wantChoice)
			}
		})
	}
}

// TestAdaptLegacyFunctionCallDropsWhenToolChoicePresent 验证已存在 tool_choice 时 function_call 被 Drop。
func TestAdaptLegacyFunctionCallDropsWhenToolChoicePresent(t *testing.T) {
	req := chatcompletionsadapter.ChatRequest{
		ToolChoice:   json.RawMessage(`"auto"`),
		FunctionCall: json.RawMessage(`{"name":"x"}`),
	}

	cleaned, dropped := adaptLegacyFunctions(req)

	if string(cleaned.ToolChoice) != `"auto"` {
		t.Fatalf("expected existing tool_choice kept, got %s", cleaned.ToolChoice)
	}
	assertDropped(t, dropped, "function_call")
}

// TestAdaptLegacyFunctionCallDropsUnrecognized 验证无法识别的 function_call 被 Drop。
func TestAdaptLegacyFunctionCallDropsUnrecognized(t *testing.T) {
	req := chatcompletionsadapter.ChatRequest{FunctionCall: json.RawMessage(`"required"`)}

	cleaned, dropped := adaptLegacyFunctions(req)

	if len(cleaned.ToolChoice) != 0 {
		t.Fatalf("expected no tool_choice for unrecognized function_call, got %s", cleaned.ToolChoice)
	}
	assertDropped(t, dropped, "function_call")
}

// TestDropUnsupportedAdaptsLegacyEndToEnd 验证经 dropUnsupported 整链：legacy functions/function_call
// 被 Adapt 后进入 wire 候选，不出现在 dropped 审计。
func TestDropUnsupportedAdaptsLegacyEndToEnd(t *testing.T) {
	req := chatcompletionsadapter.ChatRequest{
		Functions:    json.RawMessage(`[{"name":"f"}]`),
		FunctionCall: json.RawMessage(`"auto"`),
	}

	cleaned, dropped := dropUnsupported(req)

	if len(cleaned.Tools) != 1 || cleaned.Tools[0].Function.Name != "f" {
		t.Fatalf("expected functions adapted to tools, got %#v", cleaned.Tools)
	}
	if string(cleaned.ToolChoice) != `"auto"` {
		t.Fatalf("expected function_call adapted to tool_choice, got %s", cleaned.ToolChoice)
	}
	if len(dropped) != 0 {
		t.Fatalf("expected no dropped for successful legacy adapt, got %v", dropped)
	}
}
