package messages

import (
	"encoding/json"
	"testing"

	messagesadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic/messages"
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
	apply  func(*messagesadapter.MessageRequest)
	verify func(t *testing.T, cleaned messagesadapter.MessageRequest) probeOutcome
}

// anthropicCapabilityProbes 为每个 Drop 可观测的能力提供探针。full 且无 Drop 语义的能力
// （text.*、stream.*、tools.choice_required）由协议契约保证，不在此探测。
func anthropicCapabilityProbes() []capabilityProbe {
	return []capabilityProbe{
		{
			key: capability.Key("tools.function"),
			apply: func(r *messagesadapter.MessageRequest) {
				r.Tools = json.RawMessage(`[{"name":"get_weather","input_schema":{"type":"object"}}]`)
			},
			verify: func(t *testing.T, c messagesadapter.MessageRequest) probeOutcome {
				return keptIf(anthropicToolPresent(t, c.Tools, func(tool map[string]any) bool {
					return tool["name"] == "get_weather"
				}))
			},
		},
		{
			key: capability.Key("reasoning.budget"),
			apply: func(r *messagesadapter.MessageRequest) {
				r.Thinking = json.RawMessage(`{"type":"enabled","budget_tokens":1024}`)
			},
			verify: func(t *testing.T, c messagesadapter.MessageRequest) probeOutcome {
				return keptIf(len(c.Thinking) > 0)
			},
		},
		{
			key: capability.Key("reasoning.effort"),
			apply: func(r *messagesadapter.MessageRequest) {
				r.Extensions = map[string]json.RawMessage{
					"output_config": json.RawMessage(`{"effort":"low"}`),
				}
			},
			verify: func(t *testing.T, c messagesadapter.MessageRequest) probeOutcome {
				cfg, ok := outputConfig(t, c)
				if !ok {
					return outcomeStripped
				}
				effort, ok := cfg["effort"].(string)
				if !ok {
					return outcomeStripped
				}
				if effort != "low" {
					return outcomeAdapted
				}
				return outcomeKept
			},
		},
		{
			key: capability.Key("image.input"),
			apply: func(r *messagesadapter.MessageRequest) {
				r.Messages = []messagesadapter.Message{
					userMessage(`[{"type":"text","text":"hi"},{"type":"image","source":{"type":"base64","data":"x"}}]`),
				}
			},
			verify: func(t *testing.T, c messagesadapter.MessageRequest) probeOutcome {
				return contentBlockOutcome(t, c, "image")
			},
		},
		{
			key: capability.Key("file.input"),
			apply: func(r *messagesadapter.MessageRequest) {
				r.Messages = []messagesadapter.Message{
					userMessage(`[{"type":"text","text":"hi"},{"type":"document","source":{"type":"base64","data":"x"}}]`),
				}
			},
			verify: func(t *testing.T, c messagesadapter.MessageRequest) probeOutcome {
				return contentBlockOutcome(t, c, "document")
			},
		},
		{
			key: capability.Key("service_tier"),
			apply: func(r *messagesadapter.MessageRequest) {
				r.Extensions = map[string]json.RawMessage{"service_tier": json.RawMessage(`"auto"`)}
			},
			verify: func(t *testing.T, c messagesadapter.MessageRequest) probeOutcome {
				return strippedIf(!extensionPresent(c, "service_tier"))
			},
		},
		{
			key: capability.Key("tools.builtin.web_search"),
			apply: func(r *messagesadapter.MessageRequest) {
				r.Tools = json.RawMessage(`[{"type":"web_search_20250305","name":"web_search"}]`)
			},
			verify: func(t *testing.T, c messagesadapter.MessageRequest) probeOutcome {
				return strippedIf(!anthropicToolPresent(t, c.Tools, func(tool map[string]any) bool {
					return tool["name"] == "web_search"
				}))
			},
		},
		{
			key: capability.Key("tools.builtin.mcp"),
			apply: func(r *messagesadapter.MessageRequest) {
				r.Extensions = map[string]json.RawMessage{
					"mcp_servers": json.RawMessage(`[{"type":"url","url":"http://x","name":"m"}]`),
				}
			},
			verify: func(t *testing.T, c messagesadapter.MessageRequest) probeOutcome {
				return strippedIf(!extensionPresent(c, "mcp_servers"))
			},
		},
		{
			key: capability.Key("response_format.json_schema"),
			apply: func(r *messagesadapter.MessageRequest) {
				r.Extensions = map[string]json.RawMessage{
					"output_config": json.RawMessage(`{"format":{"type":"json_schema"}}`),
				}
			},
			verify: func(t *testing.T, c messagesadapter.MessageRequest) probeOutcome {
				cfg, ok := outputConfig(t, c)
				if !ok {
					return outcomeStripped
				}
				if _, has := cfg["format"]; has {
					return outcomeKept
				}
				return outcomeStripped
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

// TestCapabilityProfileMatchesDropBehavior 守护画像与 dropUnsupported 行为一致（见 openai 侧同名测试）。
func TestCapabilityProfileMatchesDropBehavior(t *testing.T) {
	profile := CapabilityProfile()
	levelByKey := make(map[capability.Key]capability.SupportLevel, len(profile.Declarations))
	for _, d := range profile.Declarations {
		levelByKey[d.Key] = d.SupportLevel
	}

	probed := make(map[capability.Key]struct{})
	for _, probe := range anthropicCapabilityProbes() {
		level, declared := levelByKey[probe.key]
		if !declared {
			t.Fatalf("probe for key %q not declared in capability profile", probe.key)
		}
		probed[probe.key] = struct{}{}

		req := messagesadapter.MessageRequest{}
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

func contentBlockOutcome(t *testing.T, c messagesadapter.MessageRequest, blockType string) probeOutcome {
	t.Helper()

	if len(c.Messages) == 0 {
		t.Fatal("probe lost messages")
	}
	var blocks []map[string]any
	if err := json.Unmarshal(c.Messages[0].Content, &blocks); err != nil {
		t.Fatalf("unmarshal cleaned content: %v", err)
	}
	for _, block := range blocks {
		if block["type"] == blockType {
			return outcomeKept
		}
	}
	return outcomeStripped
}

func outputConfig(t *testing.T, c messagesadapter.MessageRequest) (map[string]any, bool) {
	t.Helper()

	raw, ok := c.Extensions["output_config"]
	if !ok {
		return nil, false
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal output_config: %v", err)
	}
	return cfg, true
}

func extensionPresent(c messagesadapter.MessageRequest, key string) bool {
	_, ok := c.Extensions[key]
	return ok
}

func anthropicToolPresent(t *testing.T, raw json.RawMessage, predicate func(map[string]any) bool) bool {
	t.Helper()

	if len(raw) == 0 {
		return false
	}
	var tools []map[string]any
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatalf("unmarshal cleaned tools: %v", err)
	}
	for _, tool := range tools {
		if predicate(tool) {
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
