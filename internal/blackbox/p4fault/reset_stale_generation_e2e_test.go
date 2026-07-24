package p4fault_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// TestP4ResetStaleGenerationLongStreamE2E proves that an old stream which
// fails after Reset settles its real partial delivery and billing facts, while
// its stale Finish cannot mutate the Origin or Channel's new generation.
func TestP4ResetStaleGenerationLongStreamE2E(t *testing.T) {
	if os.Getenv("P4_FAULT_E2E") != "1" || os.Getenv("P4_RESET_STALE_GENERATION_E2E") != "1" {
		t.Skip("set P4_FAULT_E2E=1 and P4_RESET_STALE_GENERATION_E2E=1 to run the Reset stale-generation drill")
	}

	h := setupFaultHarness(t)
	pool := openMaintenanceDatabase(t, h.infra.databaseURL)
	runtimeStore := breakerstore.NewStore(h.redis, h.namespace)
	initialBalance := readResetInitialBalance(t, pool, h.seed)
	publishShortHalfOpenBreakerConfig(t, h, pool, runtimeStore)
	h.upstream.setMode(modeOpenAIChatStream)
	gate := h.upstream.blockNextChatStream()
	gate.FailTail()
	defer gate.Release()

	firstClientEvent := make(chan string, 1)
	streamResult := make(chan longStreamHTTPResult, 1)
	go runBlockedChatStreamRequest(h, firstClientEvent, streamResult)

	waitForSignal(t, gate.firstEventWritten, 5*time.Second, "old upstream first stream event")
	select {
	case first := <-firstClientEvent:
		if !strings.Contains(first, "data:") || !strings.Contains(first, "ok") {
			t.Fatalf("old customer first SSE event is incomplete: %q", first)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old customer did not receive the first SSE event")
	}

	oldRequest := waitForRunningRevisionStream(t, pool, h.seed, 5*time.Second)
	oldPermitKey, oldPermit := waitForOneActivePermit(t, h, 5*time.Second)
	oldRequestKey, _ := waitForActiveRequestToken(t, h, oldPermit["request_admission_id"], 5*time.Second)
	oldReservationID := waitForAuthorizedResetReservation(t, pool, oldRequest.requestID, 5*time.Second)
	originBeforeReset := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeOrigin, h.seed.originID)
	channelBeforeReset := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeChannel, h.seed.openAIChannelID)
	if oldPermit["origin_state_generation"] != fmt.Sprint(originBeforeReset.StateGeneration) ||
		oldPermit["channel_state_generation"] != fmt.Sprint(channelBeforeReset.StateGeneration) {
		t.Fatalf("old permit did not freeze the pre-Reset generations: permit=%v origin=%+v channel=%+v", oldPermit, originBeforeReset, channelBeforeReset)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	originGeneration, err := runtimeStore.Reset(ctx, breakerstore.ScopeOrigin, h.seed.originID)
	cancel()
	if err != nil {
		t.Fatalf("Reset Origin through production BreakerStore: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	channelGeneration, err := runtimeStore.Reset(ctx, breakerstore.ScopeChannel, h.seed.openAIChannelID)
	cancel()
	if err != nil {
		t.Fatalf("Reset Channel through production BreakerStore: %v", err)
	}
	if originGeneration != originBeforeReset.StateGeneration+1 ||
		channelGeneration != channelBeforeReset.StateGeneration+1 {
		t.Fatalf(
			"Reset generations origin=%d channel=%d want=%d/%d",
			originGeneration,
			channelGeneration,
			originBeforeReset.StateGeneration+1,
			channelBeforeReset.StateGeneration+1,
		)
	}

	openChannelBreaker(t, h, runtimeStore)
	opened := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeChannel, h.seed.openAIChannelID)
	if opened.State != breakerstore.StateOpen || opened.StateGeneration != channelGeneration+1 {
		t.Fatalf("current generation did not transition closed -> open after Reset: reset=%d opened=%+v", channelGeneration, opened)
	}
	waitForChannelOpenCooldown(t, h)

	firstPermitKey, firstPermit := runCapturedSuccessfulHalfOpenStream(t, h, h.gateways[1], oldPermitKey)
	if firstPermitKey == oldPermitKey || firstPermit["permit_id"] == oldPermit["permit_id"] {
		t.Fatalf("current generation reused the pre-Reset permit: old=%v first=%v", oldPermit, firstPermit)
	}
	assertHalfOpenSuccessCount(t, h, runtimeStore, 1)
	halfOpen := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeChannel, h.seed.openAIChannelID)
	if halfOpen.State != breakerstore.StateHalfOpen || halfOpen.HalfOpenBusy ||
		halfOpen.StateGeneration != opened.StateGeneration+1 ||
		firstPermit["channel_state_generation"] != fmt.Sprint(halfOpen.StateGeneration) {
		t.Fatalf("current generation did not transition open -> half_open with one success: opened=%+v half_open=%+v permit=%v", opened, halfOpen, firstPermit)
	}

	secondPermitKey, secondPermit := runCapturedSuccessfulHalfOpenStream(t, h, h.gateways[1], oldPermitKey)
	if secondPermitKey == oldPermitKey || secondPermitKey == firstPermitKey ||
		secondPermit["permit_id"] == oldPermit["permit_id"] ||
		secondPermit["permit_id"] == firstPermit["permit_id"] {
		t.Fatalf("current generation did not use two distinct recovery permits: old=%v first=%v second=%v", oldPermit, firstPermit, secondPermit)
	}
	if secondPermit["channel_state_generation"] != fmt.Sprint(halfOpen.StateGeneration) {
		t.Fatalf("second recovery permit did not belong to the current half-open generation: half_open=%+v permit=%v", halfOpen, secondPermit)
	}

	originBeforeOldFinish := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeOrigin, h.seed.originID)
	channelBeforeOldFinish := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeChannel, h.seed.openAIChannelID)
	if originBeforeOldFinish.State != breakerstore.StateClosed ||
		originBeforeOldFinish.StateGeneration != originGeneration ||
		channelBeforeOldFinish.State != breakerstore.StateClosed || channelBeforeOldFinish.HalfOpenBusy ||
		channelBeforeOldFinish.StateGeneration != halfOpen.StateGeneration+1 ||
		channelBeforeOldFinish.TTFTSamples != 2 {
		t.Fatalf("two current-generation permits did not close the Reset breaker: origin=%+v channel=%+v", originBeforeOldFinish, channelBeforeOldFinish)
	}

	gate.Release()
	var completed longStreamHTTPResult
	select {
	case completed = <-streamResult:
	case <-time.After(10 * time.Second):
		t.Fatal("old stream did not fail its tail after upstream release")
	}
	if completed.err != nil || completed.status != http.StatusOK {
		t.Fatalf("old customer response was rewritten after its first frame: status=%d err=%v body=%s", completed.status, completed.err, completed.body)
	}
	if !strings.Contains(completed.body, "ok") || strings.Contains(completed.body, "data: [DONE]") ||
		strings.Contains(completed.body, "service_unavailable") {
		t.Fatalf("old customer tail did not preserve the partial SSE response: %s", completed.body)
	}

	waitForResetRequestAdmissionFinished(t, h, oldRequestKey, 2*time.Second)
	waitForRevisionPermitFinished(
		t,
		h,
		oldPermitKey,
		breakerstore.DispositionStaleGeneration,
		breakerstore.DispositionStaleGeneration,
		2*time.Second,
	)
	originAfterOldFinish := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeOrigin, h.seed.originID)
	channelAfterOldFinish := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeChannel, h.seed.openAIChannelID)
	assertExactBreakerSnapshotUnchanged(t, "Origin", originBeforeOldFinish, originAfterOldFinish)
	assertExactBreakerSnapshotUnchanged(t, "Channel", channelBeforeOldFinish, channelAfterOldFinish)
	waitForResetPartialFacts(t, pool, oldRequest, h.seed, oldReservationID, initialBalance, 8*time.Second)
	assertNoLongStreamLeases(t, h)
}

func readResetInitialBalance(t *testing.T, pool *pgxpool.Pool, seed seedFacts) pgtype.Numeric {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var balance pgtype.Numeric
	var ready bool
	if err := pool.QueryRow(ctx, `
		SELECT balance, balance > 0 AND reserved_balance = 0
		FROM user_balances
		WHERE user_id = $1 AND currency = 'USD'
	`, seed.userID).Scan(&balance, &ready); err != nil {
		t.Fatalf("read initial Reset balance: %v", err)
	}
	if !balance.Valid || !ready {
		t.Fatalf("initial Reset balance is not available and unfrozen: %+v", balance)
	}
	return balance
}

func waitForAuthorizedResetReservation(
	t *testing.T,
	pool *pgxpool.Pool,
	requestID int64,
	timeout time.Duration,
) int64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastStatus string
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var reservationID int64
		var authorized bool
		lastErr = pool.QueryRow(ctx, `
			SELECT id, status,
				status = 'authorized' AND authorized_amount > 0 AND captured_amount = 0 AND released_amount = 0
				AND capture_ledger_entry_id IS NULL AND captured_at IS NULL AND released_at IS NULL
			FROM ledger_reservations
			WHERE request_record_id = $1
		`, requestID).Scan(&reservationID, &lastStatus, &authorized)
		cancel()
		if lastErr == nil && authorized {
			return reservationID
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("old stream ledger reservation was not authorized before Reset: request=%d status=%s err=%v", requestID, lastStatus, lastErr)
	return 0
}

func waitForResetRequestAdmissionFinished(
	t *testing.T,
	h *faultHarness,
	requestKey string,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last map[string]string
	for time.Now().Before(deadline) {
		last = readRedisHashForCompact(t, h, requestKey)
		if last["status"] == "finished" && last["terminal_result"] == "finished" && last["terminal_at_ms"] != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("old stream request-admission token did not finish before its tombstone TTL: key=%s token=%v", requestKey, last)
}

func waitForResetPartialFacts(
	t *testing.T,
	pool *pgxpool.Pool,
	old runningRevisionStream,
	seed seedFacts,
	reservationID int64,
	initialBalance pgtype.Numeric,
	timeout time.Duration,
) {
	t.Helper()
	queries := sqlc.New(pool)
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		request, requestErr := queries.GetRequestRecordForUpdate(ctx, old.requestID)
		attempts, attemptsErr := queries.ListRequestAttemptsByRequest(ctx, old.requestID)
		usage, usageErr := queries.GetUsageRecordByRequest(ctx, old.requestID)
		reservation, reservationErr := queries.GetLedgerReservationByRequestRecordID(ctx, old.requestID)
		var debitCount int64
		var debitMatchesReservation bool
		debitErr := pool.QueryRow(ctx, `
			SELECT COUNT(*), COALESCE(BOOL_AND(
				le.id = lr.capture_ledger_entry_id AND le.amount = lr.captured_amount
				AND le.balance_after = le.balance_before - le.amount
			), false)
			FROM ledger_entries le
			JOIN ledger_reservations lr ON lr.request_record_id = le.request_record_id
			WHERE le.request_record_id = $1 AND le.entry_type = 'debit'
		`, old.requestID).Scan(&debitCount, &debitMatchesReservation)
		var reservationAmountsClosed bool
		reservationMathErr := pool.QueryRow(ctx, `
			SELECT captured_amount > 0 AND captured_amount + released_amount = authorized_amount
			FROM ledger_reservations
			WHERE request_record_id = $1
		`, old.requestID).Scan(&reservationAmountsClosed)
		var balanceClosed bool
		balanceErr := pool.QueryRow(ctx, `
			SELECT ub.reserved_balance = 0 AND
				ub.balance + COALESCE((
					SELECT SUM(le.amount)
					FROM ledger_entries le
					WHERE le.user_id = $1 AND le.currency = 'USD' AND le.entry_type = 'debit'
				), 0) = $2::numeric
			FROM user_balances ub
			WHERE ub.user_id = $1 AND ub.currency = 'USD'
		`, seed.userID, initialBalance).Scan(&balanceClosed)
		cancel()
		if requestErr == nil && attemptsErr == nil && usageErr == nil && reservationErr == nil &&
			debitErr == nil && reservationMathErr == nil && balanceErr == nil && len(attempts) == 1 {
			attempt := attempts[0]
			last = fmt.Sprintf(
				"request=%s/%s request_error=%s request_times=%t/%t/%t attempt=%s attempt_error=%s "+
					"attempt_times=%t/%t/%t/%t finish=%s final_usage=%t origin=%s channel=%s usage=%s output=%d "+
					"reservation=%d/%s captured=%t debit=%d/%t balance_closed=%t",
				request.Status,
				request.DeliveryStatus,
				request.ErrorCode.String,
				request.ResponseStartedAt.Valid,
				request.ResponseCompletedAt.Valid,
				request.CompletedAt.Valid,
				attempt.Status,
				attempt.ErrorCode.String,
				attempt.ResponseStartedAt.Valid,
				attempt.UpstreamStartedAt.Valid,
				attempt.UpstreamFirstTokenAt.Valid,
				attempt.UpstreamCompletedAt.Valid,
				attempt.UpstreamFinishReason.String,
				attempt.FinalUsageReceived,
				attempt.BreakerOriginDisposition.String,
				attempt.BreakerChannelDisposition.String,
				usage.UsageSource,
				usage.OutputTokensTotal,
				reservation.ID,
				reservation.Status,
				reservationAmountsClosed,
				debitCount,
				debitMatchesReservation,
				balanceClosed,
			)
			if request.Status == "failed" && request.DeliveryStatus == "interrupted" && request.Stream &&
				request.ResponseStartedAt.Valid && !request.ResponseCompletedAt.Valid && request.CompletedAt.Valid &&
				request.FinalChannelID.Valid && request.FinalChannelID.Int64 == seed.openAIChannelID &&
				request.ErrorCode.Valid && request.ErrorCode.String == string(failure.CodeAdapterReadStreamFailed) &&
				attempt.ID == old.attemptID && attempt.Status == "failed" && attempt.CompletedAt.Valid &&
				attempt.UpstreamStartedAt.Valid && attempt.UpstreamFirstTokenAt.Valid && attempt.UpstreamCompletedAt.Valid &&
				attempt.UpstreamFinishReason.Valid && attempt.UpstreamFinishReason.String == lifecycle.PartialReasonInterrupted &&
				!attempt.FinalUsageReceived && attempt.ErrorCode.Valid && attempt.ErrorCode.String == string(failure.CodeAdapterReadStreamFailed) &&
				attempt.BreakerOriginDisposition.Valid &&
				attempt.BreakerOriginDisposition.String == string(breakerstore.DispositionStaleGeneration) &&
				attempt.BreakerChannelDisposition.Valid &&
				attempt.BreakerChannelDisposition.String == string(breakerstore.DispositionStaleGeneration) &&
				usage.UsageSource == "partial_stream_estimate" && usage.OutputTokensTotal > 0 &&
				reservation.ID == reservationID && reservation.Status == "captured" &&
				reservation.CaptureLedgerEntryID.Valid && reservation.CapturedAt.Valid && reservationAmountsClosed &&
				debitCount == 1 && debitMatchesReservation && balanceClosed {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Reset stale-generation partial facts did not settle: %s", last)
}

func assertExactBreakerSnapshotUnchanged(
	t *testing.T,
	name string,
	before breakerstore.ScopeSnapshot,
	after breakerstore.ScopeSnapshot,
) {
	t.Helper()
	if after != before {
		t.Fatalf("stale-generation Finish changed current %s runtime: before=%+v after=%+v", name, before, after)
	}
}
