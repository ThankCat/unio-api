package deepseek

import (
	"encoding/json"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
)

// TestAdaptLegacyFunctionsConvertsToTools 验证 legacy functions 被 Adapt 成现代 function tools，
// 不计入 dropped 审计。
func TestAdaptLegacyFunctionsConvertsToTools(t *testing.T) {
	req := openai.ChatRequest{
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
	req := openai.ChatRequest{
		Tools:     []openai.ChatTool{{Type: "function", Function: openai.ChatFunctionTool{Name: "existing"}}},
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
			req := openai.ChatRequest{FunctionCall: json.RawMessage(tt.raw)}

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
	req := openai.ChatRequest{
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
	req := openai.ChatRequest{FunctionCall: json.RawMessage(`"required"`)}

	cleaned, dropped := adaptLegacyFunctions(req)

	if len(cleaned.ToolChoice) != 0 {
		t.Fatalf("expected no tool_choice for unrecognized function_call, got %s", cleaned.ToolChoice)
	}
	assertDropped(t, dropped, "function_call")
}

// TestDropUnsupportedAdaptsLegacyEndToEnd 验证经 dropUnsupported 整链：legacy functions/function_call
// 被 Adapt 后进入 wire 候选，不出现在 dropped 审计。
func TestDropUnsupportedAdaptsLegacyEndToEnd(t *testing.T) {
	req := openai.ChatRequest{
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
