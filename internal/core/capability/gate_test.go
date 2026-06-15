package capability

import (
	"encoding/json"
	"reflect"
	"testing"
)

func modelCap(key Key, level SupportLevel, limits string) ModelCapability {
	mc := ModelCapability{ModelID: 1, Key: key, SupportLevel: level}
	if limits != "" {
		mc.Limits = json.RawMessage(limits)
	}
	return mc
}

func channelOverride(channelID int64, key Key, level SupportLevel, limits string) ChannelOverride {
	ov := ChannelOverride{ChannelID: channelID, Key: key, SupportLevel: level}
	if limits != "" {
		ov.Limits = json.RawMessage(limits)
	}
	return ov
}

func TestEvaluateUnprovisionedModelSkips(t *testing.T) {
	got := Evaluate(nil, []ChannelCaps{{ChannelID: 1}}, NewSet(KeyTextInput, KeyTextOutput), RequestLimits{})

	if got.Result != GateResultUnprovisioned {
		t.Fatalf("result = %q, want %q", got.Result, GateResultUnprovisioned)
	}
	if got.Provisioned {
		t.Fatalf("provisioned = true, want false")
	}
}

func TestEvaluateNoRequiredSkips(t *testing.T) {
	got := Evaluate([]ModelCapability{modelCap(KeyTextInput, SupportLevelFull, "")}, nil, Set{}, RequestLimits{})

	if got.Result != GateResultNoRequired {
		t.Fatalf("result = %q, want %q", got.Result, GateResultNoRequired)
	}
}

func TestEvaluateModelMissingCapability(t *testing.T) {
	modelCaps := []ModelCapability{
		modelCap(KeyTextInput, SupportLevelFull, ""),
		modelCap(KeyTextOutput, SupportLevelFull, ""),
		// 缺 image.input 声明行。
	}
	required := NewSet(KeyTextInput, KeyTextOutput, KeyImageInput)

	got := Evaluate(modelCaps, []ChannelCaps{{ChannelID: 1}}, required, RequestLimits{})

	if got.Result != GateResultModelUnavailable {
		t.Fatalf("result = %q, want %q", got.Result, GateResultModelUnavailable)
	}
	if want := []Key{KeyImageInput}; !reflect.DeepEqual(got.MissingModel, want) {
		t.Fatalf("missing model = %v, want %v", got.MissingModel, want)
	}
}

func TestEvaluateModelUnsupportedCapability(t *testing.T) {
	modelCaps := []ModelCapability{
		modelCap(KeyTextInput, SupportLevelFull, ""),
		modelCap(KeyTextOutput, SupportLevelFull, ""),
		modelCap(KeyImageInput, SupportLevelUnsupported, ""),
	}
	required := NewSet(KeyTextInput, KeyTextOutput, KeyImageInput)

	got := Evaluate(modelCaps, []ChannelCaps{{ChannelID: 1}}, required, RequestLimits{})

	if got.Result != GateResultModelUnavailable {
		t.Fatalf("result = %q, want %q", got.Result, GateResultModelUnavailable)
	}
	if want := []Key{KeyImageInput}; !reflect.DeepEqual(got.MissingModel, want) {
		t.Fatalf("missing model = %v, want %v", got.MissingModel, want)
	}
}

func TestEvaluateLimitedReasoningEffortExceeded(t *testing.T) {
	modelCaps := []ModelCapability{
		modelCap(KeyTextInput, SupportLevelFull, ""),
		modelCap(KeyTextOutput, SupportLevelFull, ""),
		modelCap(KeyReasoningEffort, SupportLevelLimited, `{"max_effort":"medium"}`),
	}
	required := NewSet(KeyTextInput, KeyTextOutput, KeyReasoningEffort)

	// 请求 high 超过模型 medium 上限 → 缺能力。
	exceeded := Evaluate(modelCaps, []ChannelCaps{{ChannelID: 1}}, required, RequestLimits{ReasoningEffort: "high"})
	if exceeded.Result != GateResultModelUnavailable {
		t.Fatalf("exceeded result = %q, want %q", exceeded.Result, GateResultModelUnavailable)
	}
	if want := []Key{KeyReasoningEffort}; !reflect.DeepEqual(exceeded.MissingModel, want) {
		t.Fatalf("exceeded missing = %v, want %v", exceeded.MissingModel, want)
	}

	// 请求 medium 等于上限 → 满足。
	within := Evaluate(modelCaps, []ChannelCaps{{ChannelID: 1}}, required, RequestLimits{ReasoningEffort: "medium"})
	if within.Result != GateResultOK {
		t.Fatalf("within result = %q, want %q", within.Result, GateResultOK)
	}

	// 未抽取档位值（wired observe 现状）→ limited 视为满足。
	noValue := Evaluate(modelCaps, []ChannelCaps{{ChannelID: 1}}, required, RequestLimits{})
	if noValue.Result != GateResultOK {
		t.Fatalf("noValue result = %q, want %q", noValue.Result, GateResultOK)
	}
}

func TestEvaluateChannelOverrideCloses(t *testing.T) {
	modelCaps := []ModelCapability{
		modelCap(KeyTextInput, SupportLevelFull, ""),
		modelCap(KeyTextOutput, SupportLevelFull, ""),
		modelCap(KeyImageInput, SupportLevelFull, ""),
	}
	required := NewSet(KeyTextInput, KeyTextOutput, KeyImageInput)

	// 唯一候选 channel 把 image.input override 关闭 → channel_unavailable。
	channels := []ChannelCaps{{
		ChannelID: 7,
		Overrides: []ChannelOverride{channelOverride(7, KeyImageInput, SupportLevelUnsupported, "")},
	}}

	got := Evaluate(modelCaps, channels, required, RequestLimits{})

	if got.Result != GateResultChannelUnavailable {
		t.Fatalf("result = %q, want %q", got.Result, GateResultChannelUnavailable)
	}
	if want := []Key{KeyImageInput}; !reflect.DeepEqual(got.MissingChannel, want) {
		t.Fatalf("missing channel = %v, want %v", got.MissingChannel, want)
	}
	if len(got.MissingModel) != 0 {
		t.Fatalf("missing model = %v, want empty", got.MissingModel)
	}
}

func TestEvaluateMultiChannelPartialAvailability(t *testing.T) {
	modelCaps := []ModelCapability{
		modelCap(KeyTextInput, SupportLevelFull, ""),
		modelCap(KeyTextOutput, SupportLevelFull, ""),
		modelCap(KeyImageInput, SupportLevelFull, ""),
	}
	required := NewSet(KeyTextInput, KeyTextOutput, KeyImageInput)

	// channel 7 关闭 image.input，channel 9 无 override → 整体 ok（部分可用即放行）。
	channels := []ChannelCaps{
		{ChannelID: 7, Overrides: []ChannelOverride{channelOverride(7, KeyImageInput, SupportLevelUnsupported, "")}},
		{ChannelID: 9},
	}

	got := Evaluate(modelCaps, channels, required, RequestLimits{})

	if got.Result != GateResultOK {
		t.Fatalf("result = %q, want %q", got.Result, GateResultOK)
	}
}

func TestEvaluateAllSatisfied(t *testing.T) {
	modelCaps := []ModelCapability{
		modelCap(KeyTextInput, SupportLevelFull, ""),
		modelCap(KeyTextOutput, SupportLevelFull, ""),
		modelCap(KeyToolsFunction, SupportLevelLimited, ""),
	}
	required := NewSet(KeyTextInput, KeyTextOutput, KeyToolsFunction)

	got := Evaluate(modelCaps, []ChannelCaps{{ChannelID: 1}, {ChannelID: 2}}, required, RequestLimits{})

	if got.Result != GateResultOK {
		t.Fatalf("result = %q, want %q", got.Result, GateResultOK)
	}
	if !got.Provisioned {
		t.Fatalf("provisioned = false, want true")
	}
}

func TestEvaluateChannelOverrideLimitedWithinLimitStillOK(t *testing.T) {
	modelCaps := []ModelCapability{
		modelCap(KeyTextInput, SupportLevelFull, ""),
		modelCap(KeyTextOutput, SupportLevelFull, ""),
		modelCap(KeyReasoningEffort, SupportLevelFull, ""),
	}
	required := NewSet(KeyTextInput, KeyTextOutput, KeyReasoningEffort)

	// channel override 把 effort 收紧到 limited max=medium，请求未带档位值 → 视为满足。
	channels := []ChannelCaps{{
		ChannelID: 5,
		Overrides: []ChannelOverride{channelOverride(5, KeyReasoningEffort, SupportLevelLimited, `{"max_effort":"medium"}`)},
	}}

	got := Evaluate(modelCaps, channels, required, RequestLimits{})
	if got.Result != GateResultOK {
		t.Fatalf("result = %q, want %q", got.Result, GateResultOK)
	}

	// 请求 high 超过 channel 收紧上限 → channel_unavailable。
	exceeded := Evaluate(modelCaps, channels, required, RequestLimits{ReasoningEffort: "high"})
	if exceeded.Result != GateResultChannelUnavailable {
		t.Fatalf("exceeded result = %q, want %q", exceeded.Result, GateResultChannelUnavailable)
	}
}
