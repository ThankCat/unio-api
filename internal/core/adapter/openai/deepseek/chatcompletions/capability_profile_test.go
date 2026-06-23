package chatcompletions

import (
	"encoding/json"
	"testing"

	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-api/internal/core/capability"
)

// probeOutcome 是某能力字段经 dropUnsupported 后的实际处置。
type probeOutcome int

const (
	outcomeStripped probeOutcome = iota // 出站被 Drop（对应 unsupported）
	outcomeAdapted                      // 出站被改写但保留（对应 limited）
	outcomeKept                         // 出站原样保留（对应 full）
)

func (o probeOutcome) String() string {
	switch o {
	case outcomeStripped:
		return "stripped"
	case outcomeAdapted:
		return "adapted"
	default:
		return "kept"
	}
}

// capabilityProbe 描述如何在请求上触发某能力，并观测 dropUnsupported 的真实处置。
type capabilityProbe struct {
	key    capability.Key
	apply  func(*chatcompletionsadapter.ChatRequest)
	verify func(t *testing.T, cleaned chatcompletionsadapter.ChatRequest) probeOutcome
}

// openaiCapabilityProbes 为每个 Drop 可观测的能力提供探针。full 且无 Drop 语义的能力
// （text.*、stream.*、tools.choice_required）由协议契约保证，不在此探测。
func openaiCapabilityProbes() []capabilityProbe {
	return []capabilityProbe{
		{
			key:   capability.Key("image.input"),
			apply: setContent(`[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"http://x"}}]`),
			verify: func(t *testing.T, c chatcompletionsadapter.ChatRequest) probeOutcome {
				return contentPartOutcome(t, c, "image_url")
			},
		},
		{
			key:   capability.Key("audio.input"),
			apply: setContent(`[{"type":"text","text":"hi"},{"type":"input_audio","input_audio":{"data":"x","format":"wav"}}]`),
			verify: func(t *testing.T, c chatcompletionsadapter.ChatRequest) probeOutcome {
				return contentPartOutcome(t, c, "input_audio")
			},
		},
		{
			key:   capability.Key("file.input"),
			apply: setContent(`[{"type":"text","text":"hi"},{"type":"file","file":{"file_id":"f"}}]`),
			verify: func(t *testing.T, c chatcompletionsadapter.ChatRequest) probeOutcome {
				return contentPartOutcome(t, c, "file")
			},
		},
		{
			key:   capability.Key("audio.output"),
			apply: func(r *chatcompletionsadapter.ChatRequest) { r.Modalities = []string{"text", "audio"} },
			verify: func(t *testing.T, c chatcompletionsadapter.ChatRequest) probeOutcome {
				return strippedIf(len(c.Modalities) == 0)
			},
		},
		{
			key: capability.Key("tools.custom"),
			apply: func(r *chatcompletionsadapter.ChatRequest) {
				r.Tools = []chatcompletionsadapter.ChatTool{{Type: "custom"}}
			},
			verify: func(t *testing.T, c chatcompletionsadapter.ChatRequest) probeOutcome {
				return strippedIf(!hasToolType(c.Tools, "custom"))
			},
		},
		{
			key: capability.Key("tools.function"),
			apply: func(r *chatcompletionsadapter.ChatRequest) {
				r.Tools = []chatcompletionsadapter.ChatTool{{Type: "function"}}
			},
			verify: func(t *testing.T, c chatcompletionsadapter.ChatRequest) probeOutcome {
				return keptIf(hasToolType(c.Tools, "function"))
			},
		},
		{
			key: capability.Key("tools.parallel"),
			apply: func(r *chatcompletionsadapter.ChatRequest) {
				v := true
				r.ParallelToolCalls = &v
			},
			verify: func(t *testing.T, c chatcompletionsadapter.ChatRequest) probeOutcome {
				return strippedIf(c.ParallelToolCalls == nil)
			},
		},
		{
			key: capability.Key("response_format.json_schema"),
			apply: func(r *chatcompletionsadapter.ChatRequest) {
				r.ResponseFormat = &chatcompletionsadapter.ChatResponseFormat{Type: "json_schema"}
			},
			verify: func(t *testing.T, c chatcompletionsadapter.ChatRequest) probeOutcome {
				return strippedIf(c.ResponseFormat == nil)
			},
		},
		{
			key: capability.Key("response_format.json_object"),
			apply: func(r *chatcompletionsadapter.ChatRequest) {
				r.ResponseFormat = &chatcompletionsadapter.ChatResponseFormat{Type: "json_object"}
			},
			verify: func(t *testing.T, c chatcompletionsadapter.ChatRequest) probeOutcome {
				return keptIf(c.ResponseFormat != nil && c.ResponseFormat.Type == "json_object")
			},
		},
		{
			key: capability.Key("service_tier"),
			apply: func(r *chatcompletionsadapter.ChatRequest) {
				v := "auto"
				r.ServiceTier = &v
			},
			verify: func(t *testing.T, c chatcompletionsadapter.ChatRequest) probeOutcome {
				return strippedIf(c.ServiceTier == nil)
			},
		},
		{
			key: capability.Key("server_state.store"),
			apply: func(r *chatcompletionsadapter.ChatRequest) {
				v := true
				r.Store = &v
			},
			verify: func(t *testing.T, c chatcompletionsadapter.ChatRequest) probeOutcome {
				return strippedIf(c.Store == nil)
			},
		},
		{
			key: capability.Key("prompt_cache"),
			apply: func(r *chatcompletionsadapter.ChatRequest) {
				v := "ck"
				r.PromptCacheKey = &v
			},
			verify: func(t *testing.T, c chatcompletionsadapter.ChatRequest) probeOutcome {
				return strippedIf(c.PromptCacheKey == nil)
			},
		},
		{
			key:   capability.Key("tools.builtin.web_search"),
			apply: func(r *chatcompletionsadapter.ChatRequest) { r.WebSearchOptions = json.RawMessage(`{}`) },
			verify: func(t *testing.T, c chatcompletionsadapter.ChatRequest) probeOutcome {
				return strippedIf(len(c.WebSearchOptions) == 0)
			},
		},
		{
			key: capability.Key("reasoning.effort"),
			apply: func(r *chatcompletionsadapter.ChatRequest) {
				v := "low"
				r.ReasoningEffort = &v
			},
			verify: func(t *testing.T, c chatcompletionsadapter.ChatRequest) probeOutcome {
				if c.ReasoningEffort == nil {
					return outcomeStripped
				}
				if *c.ReasoningEffort != "low" {
					return outcomeAdapted
				}
				return outcomeKept
			},
		},
		{
			key: capability.Key("logprobs"),
			apply: func(r *chatcompletionsadapter.ChatRequest) {
				v := true
				r.Logprobs = &v
			},
			verify: func(t *testing.T, c chatcompletionsadapter.ChatRequest) probeOutcome {
				return keptIf(c.Logprobs != nil)
			},
		},
	}
}

// TestCapabilityProfileIsSelfConsistent 校验画像通过基础自洽校验。
func TestCapabilityProfileIsSelfConsistent(t *testing.T) {
	if err := CapabilityProfile().Validate(); err != nil {
		t.Fatalf("capability profile invalid: %v", err)
	}
}

// TestCapabilityProfileReasoningEffortLimitsSchema 锁定 reasoning.effort 的 limits 使用 capability 闸门
// 可消费的 {"max_effort":<档位>} schema。闸门 limitViolated 只认 max_effort 字段；写成其它 schema
// （如 {"effort":[...]}）会让 limited 退化为恒满足的死声明，本测试守护该回归。上限取 "high"：effortRank
// 不识别 "max"，若误写 "max" 会令任意请求被判超限。
func TestCapabilityProfileReasoningEffortLimitsSchema(t *testing.T) {
	var decl capability.Declaration
	for _, d := range CapabilityProfile().Declarations {
		if d.Key == capability.Key("reasoning.effort") {
			decl = d
			break
		}
	}
	if decl.Key != capability.Key("reasoning.effort") {
		t.Fatal("reasoning.effort declaration missing")
	}

	var limit struct {
		MaxEffort string `json:"max_effort"`
	}
	if err := json.Unmarshal(decl.Limits, &limit); err != nil {
		t.Fatalf("limits not gate-consumable JSON: %v (%s)", err, string(decl.Limits))
	}
	if limit.MaxEffort != "high" {
		t.Fatalf("reasoning.effort max_effort = %q, want \"high\" (gate-consumable ceiling); raw=%s", limit.MaxEffort, string(decl.Limits))
	}
}

// TestCapabilityProfileMatchesDropBehavior 守护画像与 dropUnsupported 行为一致：
//   - 每个探针的实际处置必须等于画像声明级别的预期处置；
//   - 探针 key 必须在画像内声明；
//   - 画像里每个 unsupported/limited 都必须有探针证明 adapter 确实 Drop/Adapt（防止只声明不验证）。
func TestCapabilityProfileMatchesDropBehavior(t *testing.T) {
	profile := CapabilityProfile()
	levelByKey := make(map[capability.Key]capability.SupportLevel, len(profile.Declarations))
	for _, d := range profile.Declarations {
		levelByKey[d.Key] = d.SupportLevel
	}

	probed := make(map[capability.Key]struct{})
	for _, probe := range openaiCapabilityProbes() {
		level, declared := levelByKey[probe.key]
		if !declared {
			t.Fatalf("probe for key %q not declared in capability profile", probe.key)
		}
		probed[probe.key] = struct{}{}

		req := chatcompletionsadapter.ChatRequest{}
		probe.apply(&req)
		cleaned, _ := dropUnsupported(req)

		got := probe.verify(t, cleaned)
		want := expectedOutcome(level)
		if got != want {
			t.Fatalf("key %q declared %q (expect %s) but dropUnsupported yielded %s", probe.key, level, want, got)
		}
	}

	for _, d := range profile.Declarations {
		if d.SupportLevel == capability.SupportLevelFull {
			continue
		}
		if _, ok := probed[d.Key]; !ok {
			t.Fatalf("key %q declared %q without a drop probe; unsupported/limited must be verified against dropUnsupported", d.Key, d.SupportLevel)
		}
	}
}

func expectedOutcome(level capability.SupportLevel) probeOutcome {
	switch level {
	case capability.SupportLevelUnsupported:
		return outcomeStripped
	case capability.SupportLevelLimited:
		return outcomeAdapted
	default:
		return outcomeKept
	}
}

func setContent(content string) func(*chatcompletionsadapter.ChatRequest) {
	return func(r *chatcompletionsadapter.ChatRequest) {
		r.Messages = []chatcompletionsadapter.ChatMessage{{Role: "user", Content: json.RawMessage(content)}}
	}
}

func contentPartOutcome(t *testing.T, c chatcompletionsadapter.ChatRequest, partType string) probeOutcome {
	t.Helper()

	if len(c.Messages) == 0 {
		t.Fatal("probe lost messages")
	}
	var parts []map[string]any
	if err := json.Unmarshal(c.Messages[0].Content, &parts); err != nil {
		t.Fatalf("unmarshal cleaned content: %v", err)
	}
	for _, part := range parts {
		if part["type"] == partType {
			return outcomeKept
		}
	}
	return outcomeStripped
}

func hasToolType(tools []chatcompletionsadapter.ChatTool, toolType string) bool {
	for _, tool := range tools {
		if tool.Type == toolType {
			return true
		}
	}
	return false
}

func strippedIf(stripped bool) probeOutcome {
	if stripped {
		return outcomeStripped
	}
	return outcomeKept
}

func keptIf(kept bool) probeOutcome {
	if kept {
		return outcomeKept
	}
	return outcomeStripped
}
