//go:build blackbox

package anthropicsdk_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/ThankCat/unio-gateway/internal/blackbox/sdkfixture"
)

// ANT-SDK-Mock-07：一次成功的 Anthropic SDK 请求后，DB 中事实链路完整。
//
// 验证：
//   - request_records 写入 succeeded 终态，且 ingress_protocol='anthropic'/operation='messages'；
//   - request_attempts 写入 succeeded（upstream_protocol='anthropic'），upstream_status_code=200；
//   - usage_records 写入，与上游 usage 一致（input_tokens → uncached_input_tokens；
//     output_tokens → output_tokens_total）；
//   - ledger_entries 有一条 debit；price_snapshots / cost_snapshots 各一条。
//
// 这是 Anthropic 协议「商业账务事实可审计」的最小端到端证明，
// 与 OAI-SDK-Mock-09 镜像。
func TestANTSDKMockSettlementWritesAuditTrail(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeMockMessageResponse(w, "msg_settle_1", "settle ok", 100, 50)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		Protocol:        "anthropic",
		AdapterKey:      "deepseek",
		UpstreamBaseURL: mock.URL,
	})

	client := anthropic.NewClient(
		option.WithBaseURL(f.AnthropicBaseURL),
		option.WithAPIKey(f.APIKey),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msg, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(f.ModelID),
		MaxTokens: 16,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("hi")),
		},
	})
	if err != nil {
		t.Fatalf("anthropic-sdk-go call: %v", err)
	}
	if len(msg.Content) == 0 || msg.Content[0].Text != "settle ok" {
		t.Fatalf("unexpected content: %+v", msg.Content)
	}

	// settlement 同步路径下应已完成，留 200ms buffer。
	time.Sleep(200 * time.Millisecond)

	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dbCancel()

	// 1) request_records
	var (
		rrID         int64
		rrStatus     string
		rrIngress    string
		rrOperation  string
		rrFinalChan  *int64
		rrModelID    string
		rrResponseID *string
	)
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT id, status, ingress_protocol, operation, final_channel_id, requested_model_id, response_id
		FROM request_records
		WHERE user_id = $1
		ORDER BY id DESC
		LIMIT 1
	`, f.UserID).Scan(&rrID, &rrStatus, &rrIngress, &rrOperation, &rrFinalChan, &rrModelID, &rrResponseID); err != nil {
		t.Fatalf("query request_records: %v", err)
	}
	if rrStatus != "succeeded" {
		t.Errorf("request_records.status = %q, want succeeded", rrStatus)
	}
	if rrIngress != "anthropic" {
		t.Errorf("request_records.ingress_protocol = %q, want anthropic", rrIngress)
	}
	if rrOperation != "messages" {
		t.Errorf("request_records.operation = %q, want messages", rrOperation)
	}
	if rrFinalChan == nil || *rrFinalChan != f.ChannelID {
		t.Errorf("request_records.final_channel_id = %v, want %d", rrFinalChan, f.ChannelID)
	}
	if rrModelID != f.ModelID {
		t.Errorf("request_records.requested_model_id = %q, want %q", rrModelID, f.ModelID)
	}
	if rrResponseID == nil || *rrResponseID != "msg_settle_1" {
		t.Errorf("request_records.response_id = %v, want msg_settle_1", rrResponseID)
	}

	// 2) request_attempts
	var (
		attemptStatus     string
		attemptUpstream   string
		upstreamStatusInt *int32
	)
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT status, upstream_protocol, upstream_status_code
		FROM request_attempts
		WHERE request_record_id = $1
		ORDER BY attempt_index DESC
		LIMIT 1
	`, rrID).Scan(&attemptStatus, &attemptUpstream, &upstreamStatusInt); err != nil {
		t.Fatalf("query request_attempts: %v", err)
	}
	if attemptStatus != "succeeded" {
		t.Errorf("request_attempts.status = %q, want succeeded", attemptStatus)
	}
	if attemptUpstream != "anthropic" {
		t.Errorf("request_attempts.upstream_protocol = %q, want anthropic", attemptUpstream)
	}
	if upstreamStatusInt == nil || *upstreamStatusInt != 200 {
		t.Errorf("request_attempts.upstream_status_code = %v, want 200", upstreamStatusInt)
	}

	// 3) usage_records
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
	// Anthropic input_tokens=100 全部走 uncached_input；output_tokens=50 走 output_tokens_total。
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
		t.Errorf("usage_records.reasoning_output_tokens = %d, want 0 (mock 无 thinking)", usageReason)
	}
	if usageMapVer == "" {
		t.Errorf("usage_records.usage_mapping_version is empty")
	}
	if usageSource != "upstream_response" {
		t.Errorf("usage_records.usage_source = %q, want upstream_response", usageSource)
	}

	// 4) ledger_entries：至少一条 debit
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
