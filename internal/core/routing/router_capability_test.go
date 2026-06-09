package routing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// fakeCapabilityChecker 记录入参并返回预设判定快照，用于断言 observe 接线。
type fakeCapabilityChecker struct {
	calls []CapabilityCheckInput
	obs   CapabilityObservation
}

func (f *fakeCapabilityChecker) Check(_ context.Context, in CapabilityCheckInput) CapabilityObservation {
	f.calls = append(f.calls, in)
	return f.obs
}

func twoCandidateStore(t *testing.T) *fakeStore {
	t.Helper()
	encrypted := mustEncryptTestCredential(t, "secret://openai/main")
	return &fakeStore{
		rows: []sqlc.FindRouteCandidatesRow{
			{
				RequestedModelID:    "openai/gpt-4.1",
				ModelDbID:           77,
				ProviderID:          11,
				AdapterKey:          "openai",
				ChannelID:           123,
				BaseUrl:             "https://api.openai.example/v1",
				CredentialEncrypted: encrypted,
				UpstreamModel:       "gpt-4.1",
			},
			{
				RequestedModelID:    "openai/gpt-4.1",
				ModelDbID:           77,
				ProviderID:          11,
				AdapterKey:          "openai",
				ChannelID:           456,
				BaseUrl:             "https://backup.openai.example/v1",
				CredentialEncrypted: encrypted,
				UpstreamModel:       "gpt-4.1",
			},
		},
	}
}

func TestPlanChatObserveRecordsCapabilityWithoutRejecting(t *testing.T) {
	store := twoCandidateStore(t)
	checker := &fakeCapabilityChecker{obs: CapabilityObservation{
		Result:       capability.GateResultModelUnavailable,
		Provisioned:  true,
		MissingModel: []capability.Key{capability.KeyImageInput},
	}}
	router := NewRouter(store, &fakeCredentialDecryptor{apiKey: "resolved"}, 30*time.Second)
	router.SetCapabilityChecker(checker)

	required := capability.NewSet(capability.KeyTextInput, capability.KeyImageInput)
	plan, err := router.PlanChat(context.Background(), ChatRouteRequest{
		ProjectID:            42,
		ModelID:              "openai/gpt-4.1",
		IngressProtocol:      ProtocolOpenAI,
		RequiredCapabilities: required,
	})
	if err != nil {
		t.Fatalf("PlanChat returned error: %v", err)
	}

	// observe 不拒绝：候选保持不变。
	if len(plan.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2 (observe must not filter)", len(plan.Candidates))
	}
	if plan.Capability == nil {
		t.Fatalf("plan.Capability = nil, want recorded observation")
	}
	if plan.Capability.Result != capability.GateResultModelUnavailable {
		t.Fatalf("observation result = %q, want model_unavailable", plan.Capability.Result)
	}

	// 闸门入参：modelDBID + 全部候选 channelID + required 透传。
	if len(checker.calls) != 1 {
		t.Fatalf("checker called %d times, want 1", len(checker.calls))
	}
	call := checker.calls[0]
	if call.ModelDBID != 77 {
		t.Fatalf("checker model db id = %d, want 77", call.ModelDBID)
	}
	if want := []int64{123, 456}; !equalInt64Slice(call.ChannelIDs, want) {
		t.Fatalf("checker channel ids = %v, want %v", call.ChannelIDs, want)
	}
	if !call.Required.Has(capability.KeyImageInput) {
		t.Fatalf("checker required missing image.input")
	}
}

func TestPlanChatSkipsCapabilityWhenNoChecker(t *testing.T) {
	store := twoCandidateStore(t)
	router := NewRouter(store, &fakeCredentialDecryptor{apiKey: "resolved"}, 30*time.Second)

	plan, err := router.PlanChat(context.Background(), ChatRouteRequest{
		ProjectID:            42,
		ModelID:              "openai/gpt-4.1",
		IngressProtocol:      ProtocolOpenAI,
		RequiredCapabilities: capability.NewSet(capability.KeyTextInput),
	})
	if err != nil {
		t.Fatalf("PlanChat returned error: %v", err)
	}
	if plan.Capability != nil {
		t.Fatalf("plan.Capability = %+v, want nil without checker", plan.Capability)
	}
}

func TestPlanChatSkipsCapabilityWhenNoRequired(t *testing.T) {
	store := twoCandidateStore(t)
	checker := &fakeCapabilityChecker{}
	router := NewRouter(store, &fakeCredentialDecryptor{apiKey: "resolved"}, 30*time.Second)
	router.SetCapabilityChecker(checker)

	plan, err := router.PlanChat(context.Background(), ChatRouteRequest{
		ProjectID:       42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
		// 不带 RequiredCapabilities。
	})
	if err != nil {
		t.Fatalf("PlanChat returned error: %v", err)
	}
	if len(checker.calls) != 0 {
		t.Fatalf("checker called %d times, want 0 when no required", len(checker.calls))
	}
	if plan.Capability != nil {
		t.Fatalf("plan.Capability = %+v, want nil when no required", plan.Capability)
	}
}

// TestPlanChatEnforceRejectsWhenEnabled 验证 enforce 模式下闸门「不可用」判定升级为路由错误（TASK-12.08）：
// 同一表面开关 ON → model_unavailable 升级为 ErrModelCapabilityUnavailable，并带缺失能力 key；OK → 放行。
func TestPlanChatEnforceRejectsWhenEnabled(t *testing.T) {
	required := capability.NewSet(capability.KeyTextInput, capability.KeyImageInput)

	t.Run("model_unavailable_rejected", func(t *testing.T) {
		store := twoCandidateStore(t)
		checker := &fakeCapabilityChecker{obs: CapabilityObservation{
			Result:       capability.GateResultModelUnavailable,
			Provisioned:  true,
			MissingModel: []capability.Key{capability.KeyImageInput},
		}}
		router := NewRouter(store, &fakeCredentialDecryptor{apiKey: "resolved"}, 30*time.Second)
		router.SetCapabilityChecker(checker)
		router.SetCapabilityEnforcement(CapabilityEnforcement{OpenAIChat: true})

		plan, err := router.PlanChat(context.Background(), ChatRouteRequest{
			ProjectID:            42,
			ModelID:              "openai/gpt-4.1",
			IngressProtocol:      ProtocolOpenAI,
			Operation:            OperationChatCompletions,
			RequiredCapabilities: required,
		})
		if !errors.Is(err, ErrModelCapabilityUnavailable) {
			t.Fatalf("err = %v, want ErrModelCapabilityUnavailable", err)
		}
		if missing := MissingCapabilities(err); missing != string(capability.KeyImageInput) {
			t.Fatalf("missing capabilities = %q, want %q", missing, capability.KeyImageInput)
		}
		// 审计保真：enforce 拒绝时仍随 plan 返回判定快照，供 service 写 capability_check_result。
		if plan.Capability == nil || plan.Capability.Result != capability.GateResultModelUnavailable {
			t.Fatalf("plan.Capability = %+v, want recorded model_unavailable for audit", plan.Capability)
		}
		if len(plan.Candidates) != 0 {
			t.Fatalf("candidates = %d, want 0 on enforce rejection", len(plan.Candidates))
		}
	})

	t.Run("channel_unavailable_rejected", func(t *testing.T) {
		store := twoCandidateStore(t)
		checker := &fakeCapabilityChecker{obs: CapabilityObservation{
			Result:         capability.GateResultChannelUnavailable,
			Provisioned:    true,
			MissingChannel: []capability.Key{capability.KeyImageInput},
		}}
		router := NewRouter(store, &fakeCredentialDecryptor{apiKey: "resolved"}, 30*time.Second)
		router.SetCapabilityChecker(checker)
		router.SetCapabilityEnforcement(CapabilityEnforcement{OpenAIChat: true})

		_, err := router.PlanChat(context.Background(), ChatRouteRequest{
			ProjectID:            42,
			ModelID:              "openai/gpt-4.1",
			IngressProtocol:      ProtocolOpenAI,
			Operation:            OperationChatCompletions,
			RequiredCapabilities: required,
		})
		if !errors.Is(err, ErrChannelCapabilityUnavailable) {
			t.Fatalf("err = %v, want ErrChannelCapabilityUnavailable", err)
		}
	})

	t.Run("ok_result_passes", func(t *testing.T) {
		store := twoCandidateStore(t)
		checker := &fakeCapabilityChecker{obs: CapabilityObservation{Result: capability.GateResultOK, Provisioned: true}}
		router := NewRouter(store, &fakeCredentialDecryptor{apiKey: "resolved"}, 30*time.Second)
		router.SetCapabilityChecker(checker)
		router.SetCapabilityEnforcement(CapabilityEnforcement{OpenAIChat: true})

		plan, err := router.PlanChat(context.Background(), ChatRouteRequest{
			ProjectID:            42,
			ModelID:              "openai/gpt-4.1",
			IngressProtocol:      ProtocolOpenAI,
			Operation:            OperationChatCompletions,
			RequiredCapabilities: required,
		})
		if err != nil {
			t.Fatalf("PlanChat returned error: %v", err)
		}
		if len(plan.Candidates) != 2 {
			t.Fatalf("candidates = %d, want 2", len(plan.Candidates))
		}
	})
}

// TestPlanChatEnforceScopedByOperation 验证 enforce 按 ingress 表面独立：开关只对匹配表面生效，
// 其余表面（及 observe 默认）即便判定不可用也放行（TASK-12.08 灰度切换）。
func TestPlanChatEnforceScopedByOperation(t *testing.T) {
	required := capability.NewSet(capability.KeyTextInput, capability.KeyImageInput)
	unavailable := CapabilityObservation{
		Result:       capability.GateResultModelUnavailable,
		Provisioned:  true,
		MissingModel: []capability.Key{capability.KeyImageInput},
	}

	cases := []struct {
		name        string
		enforcement CapabilityEnforcement
		operation   string
		wantReject  bool
	}{
		{"observe_default_passes", CapabilityEnforcement{}, OperationChatCompletions, false},
		{"other_surface_enabled_passes", CapabilityEnforcement{OpenAIResponses: true}, OperationChatCompletions, false},
		{"matching_surface_rejects", CapabilityEnforcement{OpenAIResponses: true}, OperationResponses, true},
		{"empty_operation_passes", CapabilityEnforcement{OpenAIChat: true}, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := twoCandidateStore(t)
			checker := &fakeCapabilityChecker{obs: unavailable}
			router := NewRouter(store, &fakeCredentialDecryptor{apiKey: "resolved"}, 30*time.Second)
			router.SetCapabilityChecker(checker)
			router.SetCapabilityEnforcement(tc.enforcement)

			_, err := router.PlanChat(context.Background(), ChatRouteRequest{
				ProjectID:            42,
				ModelID:              "openai/gpt-4.1",
				IngressProtocol:      ProtocolOpenAI,
				Operation:            tc.operation,
				RequiredCapabilities: required,
			})
			if tc.wantReject && err == nil {
				t.Fatalf("expected capability rejection, got nil")
			}
			if !tc.wantReject && err != nil {
				t.Fatalf("expected pass, got error: %v", err)
			}
		})
	}
}

func equalInt64Slice(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
