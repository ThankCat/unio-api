package p4fault_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/bootstrap"
	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	adminchannel "github.com/ThankCat/unio-gateway/internal/service/admin/channel"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channeltest"
	"github.com/ThankCat/unio-gateway/internal/service/admin/providerendpoint"
)

// TestP4LongStreamRevisionFencesE2E proves that BaseURL and credential changes
// affect new work immediately without canceling an already-started customer
// stream. The old stream still settles, but its frozen permit cannot mutate the
// new Endpoint/Channel breaker or TTFT state.
func TestP4LongStreamRevisionFencesE2E(t *testing.T) {
	if os.Getenv("P4_FAULT_E2E") != "1" || os.Getenv("P4_LONG_STREAM_E2E") != "1" {
		t.Skip("set P4_FAULT_E2E=1 and P4_LONG_STREAM_E2E=1 to run the long-stream revision fence drill")
	}

	h := setupFaultHarness(t)
	pool := openMaintenanceDatabase(t, h.infra.databaseURL)
	runtimeStore := breakerstore.NewStore(h.redis, h.namespace)

	const (
		initialAuthorization = "Bearer p4-fault-upstream-key"
		rotatedCredential    = "p4-fault-rotated-key"
		rotatedAuthorization = "Bearer " + rotatedCredential
	)
	h.upstream.requireAuthorization(initialAuthorization)
	h.upstream.setMode(modeOpenAIChatStream)
	gate := h.upstream.blockNextChatStream()
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
	permitKey, oldPermit := waitForOneActivePermit(t, h, 5*time.Second)
	if oldPermit["endpoint_base_url_revision"] != fmt.Sprint(oldRequest.endpointRevision) ||
		oldPermit["channel_config_revision"] != fmt.Sprint(oldRequest.channelRevision) {
		t.Fatalf("old permit did not freeze the running attempt revisions: permit=%v attempt=%+v", oldPermit, oldRequest)
	}
	if matched, rejected := h.upstream.authorizationSnapshot(); matched != 1 || rejected != 0 {
		t.Fatalf("old upstream authorization counts matched=%d rejected=%d, want 1/0", matched, rejected)
	}

	newUpstream := newAtomicUpstream(t)
	newUpstream.requireAuthorization(initialAuthorization)
	newUpstream.setMode(modeOpenAIChatStream)

	endpointService := providerendpoint.NewService(sqlc.New(pool), runtimeStore).
		WithTransactionalDB(pool).
		WithFencer(providerendpoint.NewEndpointFencer(
			runtimecontrol.NewEndpointFencePublisher(pool),
			runtimeStore,
		))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	updatedEndpoint, err := endpointService.UpdateBaseURL(ctx, h.seed.endpointID, newUpstream.URL())
	cancel()
	if err != nil {
		t.Fatalf("update Endpoint BaseURL through production fence: %v", err)
	}
	if updatedEndpoint.BaseURL != newUpstream.URL() ||
		updatedEndpoint.BaseURLRevision != oldRequest.endpointRevision+1 ||
		updatedEndpoint.RuntimeSyncPending {
		t.Fatalf("unexpected committed Endpoint fence: base_url_revision=%d pending=%t", updatedEndpoint.BaseURLRevision, updatedEndpoint.RuntimeSyncPending)
	}

	credentialGate := newUpstream.blockNextChatStream()
	defer credentialGate.Release()
	credentialFirstClientEvent := make(chan string, 1)
	credentialStreamResult := make(chan longStreamHTTPResult, 1)
	go runBlockedChatStreamRequest(h, credentialFirstClientEvent, credentialStreamResult)
	waitForSignal(t, credentialGate.firstEventWritten, 5*time.Second, "old-credential first stream event")
	select {
	case first := <-credentialFirstClientEvent:
		if !strings.Contains(first, "data:") || !strings.Contains(first, "ok") {
			t.Fatalf("old-credential customer first SSE event is incomplete: %q", first)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old-credential customer did not receive the first SSE event")
	}
	credentialRequest := waitForRunningRevisionStream(t, pool, h.seed, 5*time.Second)
	if credentialRequest.requestID == oldRequest.requestID ||
		credentialRequest.endpointRevision != updatedEndpoint.BaseURLRevision ||
		credentialRequest.channelRevision != oldRequest.channelRevision {
		t.Fatalf("old-credential stream froze unexpected revisions: %+v", credentialRequest)
	}
	credentialPermitKey, credentialPermit := waitForActivePermitExcluding(t, h, permitKey, 5*time.Second)
	if credentialPermit["endpoint_base_url_revision"] != fmt.Sprint(credentialRequest.endpointRevision) ||
		credentialPermit["channel_config_revision"] != fmt.Sprint(credentialRequest.channelRevision) {
		t.Fatalf("old-credential permit did not freeze the running attempt revisions: %v", credentialPermit)
	}
	if matched, rejected := newUpstream.authorizationSnapshot(); matched != 1 || rejected != 0 {
		t.Fatalf("old-credential authorization counts matched=%d rejected=%d, want 1/0", matched, rejected)
	}
	newUpstream.requireAuthorization(rotatedAuthorization)
	newUpstream.setMode(modeOpenAIChatNonStream)

	registry, err := bootstrap.NewAdapterRegistry(&http.Client{Timeout: 5 * time.Second}, zap.NewNop())
	if err != nil {
		t.Fatalf("build production adapter registry: %v", err)
	}
	rotationService := channeltest.NewService(sqlc.New(pool), registry, nil)
	ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	rotation, err := rotationService.RotateCredentialAndTest(ctx, adminchannel.RotateCredentialInput{
		ID:         h.seed.openAIChannelID,
		Credential: rotatedCredential,
	})
	cancel()
	if err != nil {
		t.Fatalf("rotate credential through production probe flow: %v", err)
	}
	if !rotation.CredentialSaved || !rotation.CredentialChanged ||
		rotation.Verification.State != adminchannel.CredentialVerificationPassed ||
		!rotation.Verification.StateChangeApplied || !rotation.Verification.CredentialValidAfter ||
		rotation.CurrentConfigRevision <= oldRequest.channelRevision {
		t.Fatalf(
			"unexpected credential rotation result: saved=%t changed=%t state=%s applied=%t valid=%t saved_rev=%d current_rev=%d",
			rotation.CredentialSaved,
			rotation.CredentialChanged,
			rotation.Verification.State,
			rotation.Verification.StateChangeApplied,
			rotation.Verification.CredentialValidAfter,
			rotation.SavedConfigRevision,
			rotation.CurrentConfigRevision,
		)
	}
	if counts := h.upstream.snapshot(); counts.total != 1 {
		t.Fatalf("old address received calls after BaseURL revision commit: total=%d", counts.total)
	}
	if matched, rejected := newUpstream.authorizationSnapshot(); matched != 2 || rejected != 0 {
		t.Fatalf("new-address authorization counts after probe matched=%d rejected=%d, want 2/0", matched, rejected)
	}

	newUpstream.setMode(modeOpenAIChatStream)
	status, body := h.request(t, h.gateways[1], modeOpenAIChatStream)
	if status != http.StatusOK || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("new-revision stream status=%d body=%s", status, body)
	}
	if counts := h.upstream.snapshot(); counts.total != 1 {
		t.Fatalf("new-revision request used the old address: old_total=%d", counts.total)
	}
	if counts := newUpstream.snapshot(); counts.total != 3 {
		t.Fatalf("new address call count=%d, want old-credential stream + probe + new customer request", counts.total)
	}
	if matched, rejected := newUpstream.authorizationSnapshot(); matched != 3 || rejected != 0 {
		t.Fatalf("new address authorization counts matched=%d rejected=%d, want 3/0", matched, rejected)
	}

	endpointBeforeOldFinish := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeEndpoint, h.seed.endpointID)
	channelBeforeOldFinish := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeChannel, h.seed.openAIChannelID)
	if endpointBeforeOldFinish.BaseURLRevision != updatedEndpoint.BaseURLRevision ||
		channelBeforeOldFinish.BaseURLRevision != updatedEndpoint.BaseURLRevision ||
		channelBeforeOldFinish.ChannelConfigRevision != rotation.CurrentConfigRevision {
		t.Fatalf(
			"new runtime revisions are not active: endpoint_base=%d channel_base=%d channel_config=%d",
			endpointBeforeOldFinish.BaseURLRevision,
			channelBeforeOldFinish.BaseURLRevision,
			channelBeforeOldFinish.ChannelConfigRevision,
		)
	}
	if endpointBeforeOldFinish.SampleCount != 1 || channelBeforeOldFinish.SampleCount != 1 ||
		channelBeforeOldFinish.TTFTSamples != 1 {
		t.Fatalf("new stream did not establish the expected clean runtime sample: endpoint=%+v channel=%+v", endpointBeforeOldFinish, channelBeforeOldFinish)
	}

	gate.Release()
	var completed longStreamHTTPResult
	select {
	case completed = <-streamResult:
	case <-time.After(10 * time.Second):
		t.Fatal("old stream did not complete after upstream release")
	}
	if completed.err != nil || completed.status != http.StatusOK || !strings.Contains(completed.body, "data: [DONE]") {
		t.Fatalf("old stream result after revisions: status=%d err=%v body=%s", completed.status, completed.err, completed.body)
	}

	waitForRevisionFencedStreamFacts(
		t,
		pool,
		oldRequest,
		h.seed,
		breakerstore.DispositionStaleRevision,
		breakerstore.DispositionStaleRevision,
		8*time.Second,
	)
	waitForRevisionPermitFinished(
		t,
		h,
		permitKey,
		breakerstore.DispositionStaleRevision,
		breakerstore.DispositionStaleRevision,
		5*time.Second,
	)
	endpointAfterOldFinish := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeEndpoint, h.seed.endpointID)
	channelAfterOldFinish := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeChannel, h.seed.openAIChannelID)
	assertBreakerSnapshotUnchanged(t, "Endpoint", endpointBeforeOldFinish, endpointAfterOldFinish)
	assertBreakerSnapshotUnchanged(t, "Channel", channelBeforeOldFinish, channelAfterOldFinish)

	credentialGate.Release()
	var credentialCompleted longStreamHTTPResult
	select {
	case credentialCompleted = <-credentialStreamResult:
	case <-time.After(10 * time.Second):
		t.Fatal("old-credential stream did not complete after upstream release")
	}
	if credentialCompleted.err != nil || credentialCompleted.status != http.StatusOK ||
		!strings.Contains(credentialCompleted.body, "data: [DONE]") {
		t.Fatalf(
			"old-credential stream result after rotation: status=%d err=%v body=%s",
			credentialCompleted.status,
			credentialCompleted.err,
			credentialCompleted.body,
		)
	}
	waitForRevisionFencedStreamFacts(
		t,
		pool,
		credentialRequest,
		h.seed,
		breakerstore.DispositionApplied,
		breakerstore.DispositionStaleConfigRev,
		8*time.Second,
	)
	waitForRevisionPermitFinished(
		t,
		h,
		credentialPermitKey,
		breakerstore.DispositionApplied,
		breakerstore.DispositionStaleConfigRev,
		5*time.Second,
	)
	endpointAfterCredentialFinish := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeEndpoint, h.seed.endpointID)
	channelAfterCredentialFinish := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeChannel, h.seed.openAIChannelID)
	if endpointAfterCredentialFinish.State != endpointAfterOldFinish.State ||
		endpointAfterCredentialFinish.StateGeneration != endpointAfterOldFinish.StateGeneration ||
		endpointAfterCredentialFinish.EligibleSuccesses != endpointAfterOldFinish.EligibleSuccesses+1 ||
		endpointAfterCredentialFinish.EligibleFailures != endpointAfterOldFinish.EligibleFailures ||
		endpointAfterCredentialFinish.BaseURLRevision != endpointAfterOldFinish.BaseURLRevision {
		t.Fatalf(
			"old-credential success did not apply only to the current Endpoint: before=%+v after=%+v",
			endpointAfterOldFinish,
			endpointAfterCredentialFinish,
		)
	}
	assertBreakerSnapshotUnchanged(t, "Channel", channelAfterOldFinish, channelAfterCredentialFinish)
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	currentChannel, err := sqlc.New(pool).GetChannel(ctx, h.seed.openAIChannelID)
	cancel()
	if err != nil {
		t.Fatalf("read channel after old-credential Finish: %v", err)
	}
	if currentChannel.Credential != rotatedCredential || !currentChannel.CredentialValid ||
		currentChannel.ConfigRevision != rotation.CurrentConfigRevision {
		t.Fatalf(
			"old-credential Finish changed current credential state: valid=%t config_revision=%d want_revision=%d",
			currentChannel.CredentialValid,
			currentChannel.ConfigRevision,
			rotation.CurrentConfigRevision,
		)
	}
	assertNoLongStreamLeases(t, h)
}

type runningRevisionStream struct {
	requestID        int64
	attemptID        int64
	endpointRevision int64
	channelRevision  int64
}

func waitForRunningRevisionStream(
	t *testing.T,
	pool *pgxpool.Pool,
	seed seedFacts,
	timeout time.Duration,
) runningRevisionStream {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var result runningRevisionStream
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		lastErr = pool.QueryRow(ctx, `
			SELECT rr.id, ra.id, ra.provider_endpoint_base_url_revision, ra.channel_config_revision
			FROM request_records rr
			JOIN request_attempts ra ON ra.request_record_id = rr.id
			WHERE rr.user_id = $1
			  AND rr.status = 'running'
			  AND rr.stream
			  AND ra.channel_id = $2
			ORDER BY rr.id DESC
			LIMIT 1
		`, seed.userID, seed.openAIChannelID).Scan(
			&result.requestID,
			&result.attemptID,
			&result.endpointRevision,
			&result.channelRevision,
		)
		cancel()
		if lastErr == nil {
			return result
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("running old stream attempt was not persisted: %v", lastErr)
	return runningRevisionStream{}
}

func waitForRevisionFencedStreamFacts(
	t *testing.T,
	pool *pgxpool.Pool,
	old runningRevisionStream,
	seed seedFacts,
	expectedEndpointDisposition breakerstore.Disposition,
	expectedChannelDisposition breakerstore.Disposition,
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
		var usageCount, debitCount int64
		factsErr := pool.QueryRow(ctx, `
			SELECT
				(SELECT COUNT(*) FROM usage_records WHERE request_record_id = $1),
				(SELECT COUNT(*) FROM ledger_entries WHERE request_record_id = $1 AND entry_type = 'debit')
		`, old.requestID).Scan(&usageCount, &debitCount)
		cancel()
		if requestErr == nil && attemptsErr == nil && factsErr == nil && len(attempts) == 1 {
			attempt := attempts[0]
			last = fmt.Sprintf(
				"request=%s/%s attempt=%s endpoint=%s channel=%s usage=%d debit=%d",
				request.Status,
				request.DeliveryStatus,
				attempt.Status,
				attempt.BreakerEndpointDisposition.String,
				attempt.BreakerChannelDisposition.String,
				usageCount,
				debitCount,
			)
			if request.Status == "succeeded" && request.DeliveryStatus == "completed" && request.Stream &&
				request.ResponseStartedAt.Valid && request.ResponseCompletedAt.Valid &&
				request.FinalChannelID.Valid && request.FinalChannelID.Int64 == seed.openAIChannelID &&
				attempt.ID == old.attemptID && attempt.Status == "succeeded" && attempt.FinalUsageReceived &&
				attempt.ProviderEndpointBaseUrlRevision == old.endpointRevision &&
				attempt.ChannelConfigRevision == old.channelRevision &&
				attempt.BreakerEndpointDisposition.Valid &&
				attempt.BreakerEndpointDisposition.String == string(expectedEndpointDisposition) &&
				attempt.BreakerChannelDisposition.Valid &&
				attempt.BreakerChannelDisposition.String == string(expectedChannelDisposition) &&
				usageCount == 1 && debitCount == 1 {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("old revision stream facts did not settle: %s", last)
}

func waitForRevisionPermitFinished(
	t *testing.T,
	h *faultHarness,
	permitKey string,
	expectedEndpointDisposition breakerstore.Disposition,
	expectedChannelDisposition breakerstore.Disposition,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last map[string]string
	for time.Now().Before(deadline) {
		last = readRedisHashForCompact(t, h, permitKey)
		if last["status"] == "finished" &&
			last["endpoint_disposition"] == string(expectedEndpointDisposition) &&
			last["channel_disposition"] == string(expectedChannelDisposition) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("old revision permit did not finish stale: %v", last)
}

func waitForActivePermitExcluding(
	t *testing.T,
	h *faultHarness,
	excludedKey string,
	timeout time.Duration,
) (string, map[string]string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	pattern := h.namespace + ":breaker:v2:permit:*"
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		keys, err := h.redis.Keys(ctx, pattern).Result()
		cancel()
		if err == nil {
			for _, key := range keys {
				if key == excludedKey {
					continue
				}
				permit := readRedisHashForCompact(t, h, key)
				if permit["status"] == "active" {
					return key, permit
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("did not find a second active permit for pattern %s", pattern)
	return "", nil
}

func mustScopeSnapshot(
	t *testing.T,
	store *breakerstore.Store,
	scope breakerstore.Scope,
	id int64,
) breakerstore.ScopeSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snapshot, err := store.Snapshot(ctx, scope, id)
	if err != nil {
		t.Fatalf("snapshot %s %d: %v", scope, id, err)
	}
	return snapshot
}

func assertBreakerSnapshotUnchanged(
	t *testing.T,
	name string,
	before breakerstore.ScopeSnapshot,
	after breakerstore.ScopeSnapshot,
) {
	t.Helper()
	if after.State != before.State || after.StateGeneration != before.StateGeneration ||
		after.EligibleSuccesses != before.EligibleSuccesses ||
		after.EligibleFailures != before.EligibleFailures ||
		after.ConsecutiveFailures != before.ConsecutiveFailures ||
		after.TTFTEWMAMs != before.TTFTEWMAMs || after.TTFTSamples != before.TTFTSamples ||
		after.BaseURLRevision != before.BaseURLRevision || after.StatusRevision != before.StatusRevision ||
		after.ChannelConfigRevision != before.ChannelConfigRevision {
		t.Fatalf("old Finish changed current %s runtime: before=%+v after=%+v", name, before, after)
	}
}

func assertNoLongStreamLeases(t *testing.T, h *faultHarness) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	keys := []string{
		h.namespace + ":breaker:v2:channel:" + formatID(h.seed.openAIChannelID) + ":conc",
		h.namespace + ":admission:v1:ru-conc:" + formatID(h.seed.routeID) + ":" + formatID(h.seed.userID),
	}
	for _, key := range keys {
		used, err := h.redis.ZCard(ctx, key).Result()
		if err != nil || used != 0 {
			t.Fatalf("long-stream concurrency was not released: key=%s used=%d err=%v", key, used, err)
		}
	}
	permitKeys, err := h.redis.Keys(ctx, h.namespace+":breaker:v2:permit:*").Result()
	if err != nil {
		t.Fatalf("list terminal permits: %v", err)
	}
	for _, key := range permitKeys {
		if status, statusErr := h.redis.HGet(ctx, key, "status").Result(); statusErr != nil || status == "active" {
			t.Fatalf("active permit remained after both streams: key=%s status=%s err=%v", key, status, statusErr)
		}
	}
}
