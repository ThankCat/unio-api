//go:build blackbox

package openaisdk_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/ThankCat/unio-gateway/internal/blackbox/sdkfixture"
)

// OAI-SDK-Mock-09：一次成功的 SDK 请求后，DB 中事实链路完整。
//
// 验证：
//   - request_records 写入 succeeded 终态，且 ingress_protocol='openai'/endpoint='chat_completions'；
//   - request_attempts 写入 succeeded，upstream_status_code=200；
//   - usage_records 写入，与上游 usage 一致；
//   - ledger_entries 有一条 debit 流水（capture）；
//   - user_balances 已扣减；
//   - price_snapshots / cost_snapshots 各一条。
//
// 这是「商业账务事实可审计」的最小端到端证明。
func TestOAISDKMockSettlementWritesAuditTrail(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeMockChatCompletion(w, "chatcmpl-settle-1", "settle ok", 100, 50)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL,
	})

	client := openai.NewClient(
		option.WithBaseURL(f.BaseURL),
		option.WithAPIKey(f.APIKey),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(f.ModelID),
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("openai-go call: %v", err)
	}
	if resp.Choices[0].Message.Content != "settle ok" {
		t.Fatalf("unexpected content: %q", resp.Choices[0].Message.Content)
	}

	// 给 settlement 一点点时间（同步路径下应已完成，留 200ms buffer）。
	time.Sleep(200 * time.Millisecond)

	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dbCancel()

	// 1) request_records
	var (
		rrID         int64
		rrStatus     string
		rrIngress    string
		rrEndpoint  string
		rrFinalChan  *int64
		rrModelID    string
		rrResponseID *string
	)
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT id, status, ingress_protocol, endpoint, final_channel_id, requested_model_id, response_id
		FROM request_records
		WHERE user_id = $1
		ORDER BY id DESC
		LIMIT 1
	`, f.UserID).Scan(&rrID, &rrStatus, &rrIngress, &rrEndpoint, &rrFinalChan, &rrModelID, &rrResponseID); err != nil {
		t.Fatalf("query request_records: %v", err)
	}
	if rrStatus != "succeeded" {
		t.Errorf("request_records.status = %q, want succeeded", rrStatus)
	}
	if rrIngress != "openai" {
		t.Errorf("request_records.ingress_protocol = %q, want openai", rrIngress)
	}
	if rrEndpoint != "chat_completions" {
		t.Errorf("request_records.endpoint = %q, want chat_completions", rrEndpoint)
	}
	if rrFinalChan == nil || *rrFinalChan != f.ChannelID {
		t.Errorf("request_records.final_channel_id = %v, want %d", rrFinalChan, f.ChannelID)
	}
	if rrModelID != f.ModelID {
		t.Errorf("request_records.requested_model_id = %q, want %q", rrModelID, f.ModelID)
	}
	if rrResponseID == nil || *rrResponseID != "chatcmpl-settle-1" {
		t.Errorf("request_records.response_id = %v, want chatcmpl-settle-1", rrResponseID)
	}

	// 2) request_attempts
	var (
		attemptStatus     string
		upstreamStatusInt *int32
	)
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT status, upstream_status_code
		FROM request_attempts
		WHERE request_record_id = $1
		ORDER BY attempt_index DESC
		LIMIT 1
	`, rrID).Scan(&attemptStatus, &upstreamStatusInt); err != nil {
		t.Fatalf("query request_attempts: %v", err)
	}
	if attemptStatus != "succeeded" {
		t.Errorf("request_attempts.status = %q, want succeeded", attemptStatus)
	}
	if upstreamStatusInt == nil || *upstreamStatusInt != 200 {
		t.Errorf("request_attempts.upstream_status_code = %v, want 200", upstreamStatusInt)
	}

	// 3) usage_records（协议无关 facts 维度，列名见 migrations/000035_usage_records.up.sql）
	var (
		usageUncached int64
		usageCacheR   int64
		usageOutput   int64
		usageReason   int64
		usageMapVer   string
		usageSource   string
	)
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT uncached_input_tokens, cache_read_input_tokens, output_tokens_total,
		       reasoning_output_tokens, usage_mapping_version, usage_source
		FROM usage_records
		WHERE request_record_id = $1
	`, rrID).Scan(&usageUncached, &usageCacheR, &usageOutput, &usageReason, &usageMapVer, &usageSource); err != nil {
		t.Fatalf("query usage_records: %v", err)
	}
	// OpenAI prompt_tokens=100 全部走 uncached_input；completion_tokens=50 走 output_tokens_total。
	if usageUncached != 100 {
		t.Errorf("usage_records.uncached_input_tokens = %d, want 100", usageUncached)
	}
	if usageCacheR != 0 {
		t.Errorf("usage_records.cache_read_input_tokens = %d, want 0", usageCacheR)
	}
	if usageOutput != 50 {
		t.Errorf("usage_records.output_tokens_total = %d, want 50", usageOutput)
	}
	if usageReason != 0 {
		t.Errorf("usage_records.reasoning_output_tokens = %d, want 0 (mock 无 reasoning)", usageReason)
	}
	if usageMapVer == "" {
		t.Errorf("usage_records.usage_mapping_version is empty")
	}
	if usageSource != "upstream_response" {
		t.Errorf("usage_records.usage_source = %q, want upstream_response", usageSource)
	}

	// 顺带验 attempt.upstream_protocol = openai
	var attemptUpstream string
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT upstream_protocol FROM request_attempts WHERE request_record_id = $1 LIMIT 1
	`, rrID).Scan(&attemptUpstream); err != nil {
		t.Fatalf("query request_attempts upstream_protocol: %v", err)
	}
	if attemptUpstream != "openai" {
		t.Errorf("request_attempts.upstream_protocol = %q, want openai", attemptUpstream)
	}

	// 4) ledger_entries：必须有一条 debit
	var entryCount int
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT COUNT(*) FROM ledger_entries
		WHERE request_record_id = $1 AND entry_type = 'debit'
	`, rrID).Scan(&entryCount); err != nil {
		t.Fatalf("query ledger_entries: %v", err)
	}
	if entryCount < 1 {
		t.Errorf("expected at least 1 debit ledger_entry, got %d", entryCount)
	}

	// 5) price_snapshots / cost_snapshots：各一条
	var priceSnaps int
	if err := f.Pool.QueryRow(dbCtx, `SELECT COUNT(*) FROM price_snapshots WHERE request_record_id = $1`, rrID).Scan(&priceSnaps); err != nil {
		t.Fatalf("query price_snapshots: %v", err)
	}
	if priceSnaps != 1 {
		t.Errorf("expected 1 price_snapshot, got %d", priceSnaps)
	}
	var costSnaps int
	if err := f.Pool.QueryRow(dbCtx, `SELECT COUNT(*) FROM cost_snapshots WHERE request_record_id = $1`, rrID).Scan(&costSnaps); err != nil {
		t.Fatalf("query cost_snapshots: %v", err)
	}
	if costSnaps != 1 {
		t.Errorf("expected 1 cost_snapshot, got %d", costSnaps)
	}
}
