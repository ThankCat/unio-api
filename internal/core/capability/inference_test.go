package capability

import "testing"

// TestInferBaseline 验证空信号也产出文本输入/输出基线。
func TestInferBaseline(t *testing.T) {
	got := Infer(RequestSignals{})

	want := NewSet(KeyTextInput, KeyTextOutput)
	if !got.Equal(want) {
		t.Fatalf("baseline = %v, want %v", got.Keys(), want.Keys())
	}
}

// TestInferRuleByRule 逐条验证「单个信号 → 期望追加的 key」。
//
// 每个用例只置位一个信号，断言推断结果恰好等于基线加上期望 key，
// 确保规则之间没有意外耦合。
func TestInferRuleByRule(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*RequestSignals)
		expect []Key
	}{
		{"stream", func(s *RequestSignals) { s.Stream = true }, []Key{KeyStream}},
		{"image_input", func(s *RequestSignals) { s.HasImageInput = true }, []Key{KeyImageInput}},
		{"audio_input", func(s *RequestSignals) { s.HasAudioInput = true }, []Key{KeyAudioInput}},
		{"file_input", func(s *RequestSignals) { s.HasFileInput = true }, []Key{KeyFileInput}},
		{"audio_output", func(s *RequestSignals) { s.AudioOutput = true }, []Key{KeyAudioOutput}},
		{"function_tool", func(s *RequestSignals) { s.HasFunctionTool = true }, []Key{KeyToolsFunction}},
		{"custom_tool", func(s *RequestSignals) { s.HasCustomTool = true }, []Key{KeyToolsCustom}},
		{"parallel_tools", func(s *RequestSignals) { s.ParallelToolCalls = true }, []Key{KeyToolsParallel}},
		{"tool_choice_required", func(s *RequestSignals) { s.ToolChoiceRequired = true }, []Key{KeyToolsChoiceRequired}},
		{"builtin_web_search", func(s *RequestSignals) { s.BuiltinWebSearch = true }, []Key{KeyToolsBuiltinWebSearch}},
		{"builtin_file_search", func(s *RequestSignals) { s.BuiltinFileSearch = true }, []Key{KeyToolsBuiltinFileSearch}},
		{"builtin_code_interpreter", func(s *RequestSignals) { s.BuiltinCodeInterpreter = true }, []Key{KeyToolsBuiltinCodeInterpreter}},
		{"builtin_computer_use", func(s *RequestSignals) { s.BuiltinComputerUse = true }, []Key{KeyToolsBuiltinComputerUse}},
		{"builtin_image_generation", func(s *RequestSignals) { s.BuiltinImageGeneration = true }, []Key{KeyToolsBuiltinImageGeneration}},
		{"builtin_mcp", func(s *RequestSignals) { s.BuiltinMCP = true }, []Key{KeyToolsBuiltinMCP}},
		{"reasoning_effort", func(s *RequestSignals) { s.ReasoningEffort = true }, []Key{KeyReasoningEffort}},
		{"reasoning_budget", func(s *RequestSignals) { s.ReasoningBudget = true }, []Key{KeyReasoningBudget}},
		{"reasoning_summary", func(s *RequestSignals) { s.ReasoningSummary = true }, []Key{KeyReasoningSummary}},
		{"response_format_json_object", func(s *RequestSignals) { s.ResponseFormatJSONObject = true }, []Key{KeyResponseFormatJSONObject}},
		{"response_format_json_schema", func(s *RequestSignals) { s.ResponseFormatJSONSchema = true }, []Key{KeyResponseFormatJSONSchema}},
		{"prompt_cache", func(s *RequestSignals) { s.PromptCache = true }, []Key{KeyPromptCache}},
		{"logprobs", func(s *RequestSignals) { s.Logprobs = true }, []Key{KeyLogprobs}},
		{"service_tier", func(s *RequestSignals) { s.ServiceTier = true }, []Key{KeyServiceTier}},
		{"server_state_store", func(s *RequestSignals) { s.ServerStateStore = true }, []Key{KeyServerStateStore}},
		{"server_state_background", func(s *RequestSignals) { s.ServerStateBackground = true }, []Key{KeyServerStateBackground}},
		{"encrypted_content", func(s *RequestSignals) { s.EncryptedContent = true }, []Key{KeyResponsesEncryptedContent}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var signals RequestSignals
			tc.mutate(&signals)

			want := NewSet(append([]Key{KeyTextInput, KeyTextOutput}, tc.expect...)...)
			got := Infer(signals)
			if !got.Equal(want) {
				t.Fatalf("Infer = %v, want %v", got.Keys(), want.Keys())
			}
		})
	}
}

// TestInferStreamWithTools 验证流式 + 工具组合派生 stream.tools 与 stream.usage。
func TestInferStreamWithTools(t *testing.T) {
	t.Run("stream_with_function_tool_adds_stream_tools", func(t *testing.T) {
		got := Infer(RequestSignals{Stream: true, HasFunctionTool: true})
		if !got.Has(KeyStreamTools) {
			t.Fatalf("expected %s, got %v", KeyStreamTools, got.Keys())
		}
	})

	t.Run("stream_with_custom_tool_adds_stream_tools", func(t *testing.T) {
		got := Infer(RequestSignals{Stream: true, HasCustomTool: true})
		if !got.Has(KeyStreamTools) {
			t.Fatalf("expected %s, got %v", KeyStreamTools, got.Keys())
		}
	})

	t.Run("stream_usage_requires_stream", func(t *testing.T) {
		// StreamUsage 只有在 Stream 为真时才生效，避免无流式却要求 stream.usage。
		got := Infer(RequestSignals{StreamUsage: true})
		if got.Has(KeyStreamUsage) || got.Has(KeyStream) {
			t.Fatalf("stream.usage must not appear without stream, got %v", got.Keys())
		}
	})

	t.Run("stream_with_usage", func(t *testing.T) {
		got := Infer(RequestSignals{Stream: true, StreamUsage: true})
		if !got.Has(KeyStream) || !got.Has(KeyStreamUsage) {
			t.Fatalf("expected stream + stream.usage, got %v", got.Keys())
		}
	})

	t.Run("stream_without_tools_has_no_stream_tools", func(t *testing.T) {
		got := Infer(RequestSignals{Stream: true})
		if got.Has(KeyStreamTools) {
			t.Fatalf("stream.tools must not appear without tools, got %v", got.Keys())
		}
	})
}

// TestInferOnlyEmitsRegisteredKeys 守护：Infer 在所有信号置位时产出的 key 全部已注册。
//
// 这是「能力 key 是公开稳定契约」的回归护栏：推断不得产出注册表外的 key。
func TestInferOnlyEmitsRegisteredKeys(t *testing.T) {
	all := RequestSignals{
		Stream:                   true,
		StreamUsage:              true,
		HasImageInput:            true,
		HasAudioInput:            true,
		HasFileInput:             true,
		AudioOutput:              true,
		HasFunctionTool:          true,
		HasCustomTool:            true,
		ParallelToolCalls:        true,
		ToolChoiceRequired:       true,
		BuiltinWebSearch:         true,
		BuiltinFileSearch:        true,
		BuiltinCodeInterpreter:   true,
		BuiltinComputerUse:       true,
		BuiltinImageGeneration:   true,
		BuiltinMCP:               true,
		ReasoningEffort:          true,
		ReasoningBudget:          true,
		ReasoningSummary:         true,
		ResponseFormatJSONObject: true,
		ResponseFormatJSONSchema: true,
		PromptCache:              true,
		Logprobs:                 true,
		ServiceTier:              true,
		ServerStateStore:         true,
		ServerStateBackground:    true,
		EncryptedContent:         true,
	}

	for _, key := range Infer(all).Keys() {
		if !IsRegisteredKey(key) {
			t.Fatalf("Infer emitted unregistered key %q", key)
		}
	}
}

// TestInferDeterministic 验证相同输入多次推断得到一致结果。
func TestInferDeterministic(t *testing.T) {
	signals := RequestSignals{Stream: true, HasFunctionTool: true, ReasoningEffort: true}

	first := Infer(signals)
	second := Infer(signals)
	if !first.Equal(second) {
		t.Fatalf("Infer not deterministic: %v vs %v", first.Keys(), second.Keys())
	}
}

// TestInferLimits 验证 InferLimits 把 reasoning effort 档位值映射进 RequestLimits（GAP-12-012）。
func TestInferLimits(t *testing.T) {
	t.Run("empty_signals_yield_empty_limits", func(t *testing.T) {
		if got := InferLimits(RequestSignals{}); got.ReasoningEffort != "" {
			t.Fatalf("ReasoningEffort = %q, want empty", got.ReasoningEffort)
		}
	})

	t.Run("effort_level_threads_into_limits", func(t *testing.T) {
		got := InferLimits(RequestSignals{ReasoningEffort: true, ReasoningEffortLevel: "high"})
		if got.ReasoningEffort != "high" {
			t.Fatalf("ReasoningEffort = %q, want high", got.ReasoningEffort)
		}
	})

	t.Run("presence_without_level_yields_empty", func(t *testing.T) {
		// 只置 presence 位（理论上不该发生）也不应臆造档位值，limited 据此放行。
		if got := InferLimits(RequestSignals{ReasoningEffort: true}); got.ReasoningEffort != "" {
			t.Fatalf("ReasoningEffort = %q, want empty", got.ReasoningEffort)
		}
	})
}
