//go:build blackbox

package openaisdk_test

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/ThankCat/unio-gateway/internal/blackbox/sdkfixture"
)

// OAI-SDK-Mock-10：fallback。
//
// 装两个 channel：
//   - 主 channel 始终 500（auth/permission/server 都行）；
//   - 备 channel 正常返回 200。
//
// SDK 调用应：
//   - 看到一个成功的响应（来自备 channel）；
//   - 内部 request_attempts 表至少 2 条（一条 failed/主、一条 succeeded/备）；
//   - response_id 来自备 channel 的返回。
func TestOAISDKMockFallbackToSecondaryChannel(t *testing.T) {
	var primaryCalls int32
	var secondaryCalls int32

	primary := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		atomic.AddInt32(&primaryCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"upstream down","type":"server_error"}}`)
	})
	t.Cleanup(primary.Close)

	secondary := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		atomic.AddInt32(&secondaryCalls, 1)
		writeMockChatCompletion(w, "chatcmpl-fallback-secondary", "from secondary", 5, 2)
	})
	t.Cleanup(secondary.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: primary.URL,
	})
	// priority 数字越大优先级越低 => primary 默认 10，secondary 设 20 作为 fallback。
	f.AddFallbackChannel(t, secondary.URL, 20)

	client := openai.NewClient(
		option.WithBaseURL(f.BaseURL),
		option.WithAPIKey(f.APIKey),
		option.WithMaxRetries(0),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(f.ModelID),
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	if err != nil {
		var errorCode, internalDetail string
		_ = f.Pool.QueryRow(context.Background(), `
			SELECT COALESCE(error_code, ''), COALESCE(internal_error_detail, '')
			FROM request_records
			WHERE user_id = $1
			ORDER BY id DESC
			LIMIT 1
		`, f.UserID).Scan(&errorCode, &internalDetail)
		t.Fatalf(
			"expected fallback success, got error: %v (primary_calls=%d secondary_calls=%d error_code=%q detail=%q)",
			err,
			atomic.LoadInt32(&primaryCalls),
			atomic.LoadInt32(&secondaryCalls),
			errorCode,
			internalDetail,
		)
	}
	if got := resp.Choices[0].Message.Content; got != "from secondary" {
		t.Fatalf("expected fallback response 'from secondary', got %q", got)
	}
	if resp.ID != "chatcmpl-fallback-secondary" {
		t.Errorf("expected response id 'chatcmpl-fallback-secondary', got %q", resp.ID)
	}
	if atomic.LoadInt32(&primaryCalls) == 0 {
		t.Errorf("expected primary upstream to be tried at least once, got 0 calls")
	}
	if atomic.LoadInt32(&secondaryCalls) == 0 {
		t.Errorf("expected secondary upstream to be called, got 0 calls")
	}

	// settlement 是同步的，但 fallback 涉及多个 attempt，给 300ms buffer 抹平时序竞争。
	time.Sleep(300 * time.Millisecond)

	// DB 校验 request_attempts 至少 2 条，且终态对应 succeeded。
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dbCancel()

	var attemptCount int
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT COUNT(*) FROM request_attempts
		WHERE request_record_id = (SELECT id FROM request_records WHERE user_id = $1 ORDER BY id DESC LIMIT 1)
	`, f.UserID).Scan(&attemptCount); err != nil {
		t.Fatalf("query attempt count: %v", err)
	}
	if attemptCount < 2 {
		t.Errorf("expected >=2 request_attempts (primary fail + secondary success), got %d", attemptCount)
	}

	// 终态语义（TASK-10.10 durable closeout 收口后）：
	//   - 合法生产配置下（所有 enabled channel+model 都有 cost_price），fallback 路径上
	//     settle 应同步成功；client 拿到响应时 request_records.status 必须已经是 succeeded。
	//   - 仅在「副 channel 缺 cost_price」或「ledger 故障」等异常配置下，才会走
	//     RecoverableChatSettlementExecutor 的 recovery 路径，由 worker 异步推进。
	//
	// 本用例强制 fixture 的 fallback channel 与主 channel 同形状（含 cost_price），
	// 因此必须断言 succeeded；任何 running + recovery job 都视为 durable closeout 回归。
	var rrStatus string
	var rrID int64
	var rrFinalChannelID *int64
	var rrResponseID *string
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT id, status, final_channel_id, response_id
		FROM request_records WHERE user_id = $1 ORDER BY id DESC LIMIT 1
	`, f.UserID).Scan(&rrID, &rrStatus, &rrFinalChannelID, &rrResponseID); err != nil {
		t.Fatalf("query final status: %v", err)
	}

	if rrStatus != "succeeded" {
		t.Fatalf("expected request_records.status=succeeded after fallback (durable closeout), "+
			"got %q; 客户视角终态推进必须不依赖 worker 是否在跑。request_record_id=%d", rrStatus, rrID)
	}

	// 入账的 channel 必须是 secondary（fallback 命中）而非 primary。
	if rrFinalChannelID == nil {
		t.Errorf("expected final_channel_id set on succeeded fallback request, got NULL")
	} else if *rrFinalChannelID == f.ChannelID {
		t.Errorf("expected fallback final_channel_id to be secondary channel id, "+
			"but got primary channel id %d", *rrFinalChannelID)
	}
	if rrResponseID == nil || *rrResponseID != "chatcmpl-fallback-secondary" {
		t.Errorf("expected response_id=chatcmpl-fallback-secondary, got %v", rrResponseID)
	}

	// cost_snapshots 必须有且只有一条（对应 secondary channel）。
	var costSnapshotCount int
	var costSnapshotChannelID int64
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT COUNT(*), MAX(channel_id) FROM cost_snapshots WHERE request_record_id = $1
	`, rrID).Scan(&costSnapshotCount, &costSnapshotChannelID); err != nil {
		t.Fatalf("query cost snapshots: %v", err)
	}
	if costSnapshotCount != 1 {
		t.Errorf("expected exactly 1 cost_snapshot (secondary channel only), got %d", costSnapshotCount)
	}
	if rrFinalChannelID != nil && costSnapshotChannelID != *rrFinalChannelID {
		t.Errorf("cost_snapshot.channel_id=%d != request_records.final_channel_id=%d",
			costSnapshotChannelID, *rrFinalChannelID)
	}

	// settlement_recovery_jobs：RecoverableChatSettlementExecutor 总会先写一条 pending
	// 备份，然后真实 settle 成功后 mark 为 succeeded。所以同步成功路径下：
	//   - 必须恰好一条 job；
	//   - 其 status 必须是 succeeded（不能留 pending/running，否则 worker 会重复执行）。
	var jobTotal int
	var jobSucceeded int
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT COUNT(*) FILTER (WHERE TRUE), COUNT(*) FILTER (WHERE status = 'succeeded')
		FROM settlement_recovery_jobs WHERE request_record_id = $1
	`, rrID).Scan(&jobTotal, &jobSucceeded); err != nil {
		t.Fatalf("query recovery jobs: %v", err)
	}
	if jobTotal != 1 {
		t.Errorf("expected exactly 1 settlement_recovery_jobs row (executor 先写 pending 再 mark succeeded), got %d", jobTotal)
	}
	if jobSucceeded != 1 {
		t.Errorf("expected the settlement_recovery_jobs row to be 'succeeded' after sync settle, "+
			"got %d succeeded rows of %d total; durable closeout 违约（fallback 留下未完结 job）",
			jobSucceeded, jobTotal)
	}
}
