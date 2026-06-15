package sqlc_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// TestListEnabledChannelAdaptersReturnsEnabledBindings 验证 preflight 查询只返回启用 provider 下启用 channel 的 (protocol, adapter_key)。
func TestListEnabledChannelAdaptersReturnsEnabledBindings(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	timeoutMS := int32(15000)
	enabledSlug := fmt.Sprintf("chan-adapters-enabled-%d", suffix)

	enabledProvider := insertProvider(t, ctx, tx, enabledSlug, "enabled")
	enabledChannelID := insertChannelWithBinding(t, ctx, tx, enabledProvider, fmt.Sprintf("chan-adapters-openai-%d", suffix), "openai", "openai", "enabled", 10, &timeoutMS)
	disabledChannelID := insertChannelWithBinding(t, ctx, tx, enabledProvider, fmt.Sprintf("chan-adapters-disabled-%d", suffix), "openai", "openai", "disabled", 20, &timeoutMS)

	disabledProvider := insertProvider(t, ctx, tx, fmt.Sprintf("chan-adapters-disabled-provider-%d", suffix), "disabled")
	disabledProviderChannelID := insertChannelWithBinding(t, ctx, tx, disabledProvider, fmt.Sprintf("chan-adapters-disabled-provider-channel-%d", suffix), "anthropic", "deepseek", "enabled", 10, &timeoutMS)

	rows, err := queries.ListEnabledChannelAdapters(ctx)
	if err != nil {
		t.Fatalf("list enabled channel adapters: %v", err)
	}

	var got *sqlc.ListEnabledChannelAdaptersRow
	for i := range rows {
		switch rows[i].ChannelID {
		case enabledChannelID:
			row := rows[i]
			got = &row
		case disabledChannelID:
			t.Fatalf("disabled channel %d should not be returned", disabledChannelID)
		case disabledProviderChannelID:
			t.Fatalf("channel %d under disabled provider should not be returned", disabledProviderChannelID)
		}
	}

	if got == nil {
		t.Fatalf("expected enabled channel %d in result", enabledChannelID)
	}
	if got.Protocol != "openai" {
		t.Fatalf("expected protocol %q, got %q", "openai", got.Protocol)
	}
	if got.AdapterKey != "openai" {
		t.Fatalf("expected adapter key %q, got %q", "openai", got.AdapterKey)
	}
	if got.ProviderSlug != enabledSlug {
		t.Fatalf("expected provider slug %q, got %q", enabledSlug, got.ProviderSlug)
	}
}

// TestFindRouteCandidatesFiltersByIngressProtocol 验证同模型在 openai 与 anthropic 两个 channel 下，
// routing 只返回与 ingress protocol 同协议的 channel（OpenAI ingress 不命中 Anthropic channel，反之亦然）。
func TestFindRouteCandidatesFiltersByIngressProtocol(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	timeoutMS := int32(15000)

	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("proto-filter-%d", suffix), "enabled")
	requestedModel := fmt.Sprintf("dual/proto-filter-%d", suffix)
	modelID := insertModel(t, ctx, tx, requestedModel, "dual", "enabled")

	openaiChannelID := insertChannelWithBinding(t, ctx, tx, providerID, fmt.Sprintf("proto-openai-%d", suffix), "openai", "openai", "enabled", 10, &timeoutMS)
	anthropicChannelID := insertChannelWithBinding(t, ctx, tx, providerID, fmt.Sprintf("proto-anthropic-%d", suffix), "anthropic", "deepseek", "enabled", 10, &timeoutMS)
	insertChannelModel(t, ctx, tx, openaiChannelID, modelID, "proto-openai-upstream", "enabled")
	insertChannelModel(t, ctx, tx, anthropicChannelID, modelID, "proto-anthropic-upstream", "enabled")

	// 阶段 15：FindRouteCandidates 只返回「已定价」渠道，给两条渠道各配一条 enabled 渠道-模型价。
	now := time.Now().UTC()
	createChannelPriceForTest(t, ctx, queries, openaiChannelID, modelID, now)
	createChannelPriceForTest(t, ctx, queries, anthropicChannelID, modelID, now)

	openaiCandidates, err := queries.FindRouteCandidates(ctx, sqlc.FindRouteCandidatesParams{
		RequestedModelID: requestedModel,
		IngressProtocol:  "openai",
		ProjectID:        1,
		PoolKind:         "all",
		RouteID:          0,
		AtTime:           timestamptz(now),
	})
	if err != nil {
		t.Fatalf("find openai route candidates: %v", err)
	}
	if len(openaiCandidates) != 1 {
		t.Fatalf("expected 1 openai candidate, got %d: %#v", len(openaiCandidates), openaiCandidates)
	}
	if openaiCandidates[0].ChannelID != openaiChannelID {
		t.Fatalf("expected openai channel %d, got %d", openaiChannelID, openaiCandidates[0].ChannelID)
	}
	if openaiCandidates[0].AdapterKey != "openai" {
		t.Fatalf("expected adapter key %q, got %q", "openai", openaiCandidates[0].AdapterKey)
	}

	anthropicCandidates, err := queries.FindRouteCandidates(ctx, sqlc.FindRouteCandidatesParams{
		RequestedModelID: requestedModel,
		IngressProtocol:  "anthropic",
		ProjectID:        1,
		PoolKind:         "all",
		RouteID:          0,
		AtTime:           timestamptz(now),
	})
	if err != nil {
		t.Fatalf("find anthropic route candidates: %v", err)
	}
	if len(anthropicCandidates) != 1 {
		t.Fatalf("expected 1 anthropic candidate, got %d: %#v", len(anthropicCandidates), anthropicCandidates)
	}
	if anthropicCandidates[0].ChannelID != anthropicChannelID {
		t.Fatalf("expected anthropic channel %d, got %d", anthropicChannelID, anthropicCandidates[0].ChannelID)
	}
	if anthropicCandidates[0].AdapterKey != "deepseek" {
		t.Fatalf("expected adapter key %q, got %q", "deepseek", anthropicCandidates[0].AdapterKey)
	}
}
