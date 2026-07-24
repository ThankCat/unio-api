package p4fault_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	redislib "github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// TestP4ActiveOwnersAOFEpochRollbackSafetyBoundaryE2E deliberately stops at
// the recovering safety boundary. An AOF rollback brings an active ingress
// token and AttemptPermit back after PostgreSQL has advanced to a new epoch.
// Those old owners cannot be truthfully drained, so this test must not invent
// approved drain evidence or continue to Commit/Release.
func TestP4ActiveOwnersAOFEpochRollbackSafetyBoundaryE2E(t *testing.T) {
	if os.Getenv("P4_FAULT_E2E") != "1" || os.Getenv("P4_ACTIVE_EPOCH_ROLLBACK_E2E") != "1" {
		t.Skip("set P4_FAULT_E2E=1 and P4_ACTIVE_EPOCH_ROLLBACK_E2E=1 to run the active-owner AOF epoch rollback drill")
	}

	h := setupFaultHarness(t)
	maintenanceBinary := buildRuntimeStateMaintenanceBinary(t, h.root)
	pool := openMaintenanceDatabase(t, h.infra.databaseURL)
	queries := sqlc.New(pool)
	integrityStore := breakerstore.NewStore(h.redis, h.namespace)

	initialEpoch := readStateEpoch(t, queries)
	initialMarker := readStateIntegrity(t, integrityStore)
	if initialEpoch.Value.State != runtimecontrol.StateEpochReady ||
		!initialMarker.Ready(initialEpoch.Value.Epoch, initialEpoch.Revision) {
		t.Fatalf("initial runtime state is not ready: epoch=%+v marker=%+v", initialEpoch, initialMarker)
	}

	gate := h.upstream.blockNextChatStream()
	defer gate.Release()
	firstClientEvent := make(chan string, 1)
	streamResult := make(chan longStreamHTTPResult, 1)
	h.upstream.setMode(modeOpenAIChatStream)
	go runBlockedChatStreamRequest(h, firstClientEvent, streamResult)

	waitForSignal(t, gate.firstEventWritten, 5*time.Second, "upstream first stream event")
	select {
	case first := <-firstClientEvent:
		if !strings.Contains(first, "data:") || !strings.Contains(first, "ok") {
			t.Fatalf("customer first SSE event is incomplete: %q", first)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("customer did not receive the first SSE event")
	}

	permitKey, permit := waitForOneActivePermit(t, h, 5*time.Second)
	requestKey, requestToken := waitForActiveRequestToken(t, h, permit["request_admission_id"], 5*time.Second)
	assertOldActiveOwners(t, initialEpoch, requestToken, permit)

	pauseGatewayProcesses(t, h.gateways[:])
	gatewaysPaused := true
	defer func() {
		if gatewaysPaused {
			resumeGatewayProcesses(h.gateways[:])
		}
	}()

	archivedOwners := captureEpochRollbackOwnedState(t, h, requestKey, permitKey, requestToken, permit)
	aofSnapshot := archiveRedisAOF(t, h.infra)

	detectedAt := time.Now().UTC()
	recoveryID := "p4-active-owner-restore-" + randomSuffix(t)
	beginArgs := []string{
		"begin",
		"--reason", "restore",
		"--detected-at", detectedAt.Format(time.RFC3339Nano),
		"--not-before", detectedAt.Format(time.RFC3339Nano),
		"--operator-ref", maintenanceOperatorRef,
		"--recovery-id", recoveryID,
		"--expected-current-revision", strconv.FormatInt(initialEpoch.Revision, 10),
		"--confirm-state-loss",
		"--confirm-external-ingress-blocked",
	}
	assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, beginArgs...), "awaiting_maintenance")
	endpoint := readNonterminalEpochEndpoint(t, queries)
	transition := decodeEpochTransition(t, endpoint)
	recoveringEpoch := readStateEpoch(t, queries)
	assertRecoveringTransition(
		t,
		initialEpoch,
		recoveringEpoch,
		transition,
		endpoint,
		readStateIntegrity(t, integrityStore),
		recoveryID,
		runtimecontrol.StateEpochReasonRestore,
	)

	restoreRedisAOF(t, h.infra, aofSnapshot)
	assertSameEpochRecord(t, recoveringEpoch, readStateEpoch(t, queries), "active-owner AOF rollback")
	assertEndpointUnchanged(t, endpoint, readNonterminalEpochEndpoint(t, queries))
	restoredMarker := readStateIntegrity(t, integrityStore)
	if !restoredMarker.Ready(initialEpoch.Value.Epoch, initialEpoch.Revision) {
		t.Fatalf("AOF rollback did not restore the old ready marker: %+v", restoredMarker)
	}
	restoredOwners := captureEpochRollbackOwnedState(t, h, requestKey, permitKey, requestToken, permit)
	assertEpochRollbackOwnedStateEqual(t, archivedOwners, restoredOwners, "restored AOF")

	resumeGatewayProcesses(h.gateways[:])
	gatewaysPaused = false
	for _, gateway := range h.gateways {
		h.waitReadiness(t, gateway, http.StatusServiceUnavailable, 5*time.Second)
	}

	beforeRejectedRequest := h.upstream.snapshot()
	status, body := h.request(t, h.gateways[1], modeOpenAIChatNonStream)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("new request during recovering epoch status=%d want=503 body=%s", status, body)
	}
	if after := h.upstream.snapshot(); after.total != beforeRejectedRequest.total {
		t.Fatalf("new request reached upstream during recovering epoch: before=%d after=%d", beforeRejectedRequest.total, after.total)
	}
	waitForActiveRollbackGatewayMetricAtLeast(
		t,
		h,
		h.gateways[1],
		`unio_gateway_request_admission_endpoint_total{endpoint="acquire",result="runtime_state_lost"}`,
		1,
		2*time.Second,
	)
	afterRejectedRequest := captureEpochRollbackOwnedState(t, h, requestKey, permitKey, requestToken, permit)
	assertEpochRollbackOwnedStateEqual(t, restoredOwners, afterRejectedRequest, "new Acquire rejection")

	waitPastEpochRollbackOwnerRenewals(t, requestToken, permit, streamResult)
	waitForActiveRollbackGatewayMetricAtLeast(
		t,
		h,
		h.gateways[0],
		`unio_gateway_request_admission_endpoint_total{endpoint="renew",result="runtime_state_lost"}`,
		1,
		2*time.Second,
	)
	waitForActiveRollbackGatewayMetricAtLeast(
		t,
		h,
		h.gateways[0],
		`unio_gateway_breaker_permit_endpoint_total{endpoint="renew",result="runtime_state_lost"}`,
		1,
		2*time.Second,
	)
	afterRenewals := captureEpochRollbackOwnedState(t, h, requestKey, permitKey, requestToken, permit)
	assertEpochRollbackOwnedStateEqual(t, restoredOwners, afterRenewals, "old owner Renew rejection")

	gate.Release()
	var completed longStreamHTTPResult
	select {
	case completed = <-streamResult:
	case <-time.After(10 * time.Second):
		t.Fatal("long stream did not complete after releasing the upstream tail")
	}
	if completed.err != nil || completed.status != http.StatusOK ||
		!strings.Contains(completed.body, "[DONE]") {
		t.Fatalf("long stream result after epoch rollback: status=%d err=%v body=%s", completed.status, completed.err, completed.body)
	}

	waitForEpochRollbackLongStreamFacts(t, pool, h.seed)
	waitForActiveRollbackGatewayMetricAtLeast(
		t,
		h,
		h.gateways[0],
		`unio_gateway_request_admission_endpoint_total{endpoint="finish",result="runtime_state_lost"}`,
		1,
		2*time.Second,
	)
	waitForActiveRollbackGatewayMetricAtLeast(
		t,
		h,
		h.gateways[0],
		`unio_gateway_breaker_permit_endpoint_total{endpoint="finish",result="runtime_state_lost"}`,
		1,
		2*time.Second,
	)
	afterTerminalOwners := captureEpochRollbackOwnedState(t, h, requestKey, permitKey, requestToken, permit)
	assertEpochRollbackOwnedStateEqual(t, restoredOwners, afterTerminalOwners, "old owner Finish rejection")
	assertSameEpochRecord(t, recoveringEpoch, readStateEpoch(t, queries), "old owner completion")
	assertEndpointUnchanged(t, endpoint, readNonterminalEpochEndpoint(t, queries))
	if marker := readStateIntegrity(t, integrityStore); !marker.Ready(initialEpoch.Value.Epoch, initialEpoch.Revision) {
		t.Fatalf("old owner completion changed the restored marker: %+v", marker)
	}

	// The old resources remain active only inside the quarantined old epoch. The
	// nonterminal recovery endpoint and external ingress fence must remain in
	// place until operators can collect truthful drain/window evidence.
	finalRequest := readRedisHash(t, h.redis, requestKey)
	finalPermit := readRedisHash(t, h.redis, permitKey)
	if finalRequest["status"] != "active" || finalPermit["status"] != "active" {
		t.Fatalf("stale owners were rewritten as current terminal state: request=%v permit=%v", finalRequest, finalPermit)
	}
}

type epochRollbackOwnedState struct {
	RequestKeys []string
	PermitKeys  []string
	Hashes      map[string]map[string]string
	Counters    map[string]string
	ZSets       map[string][]string
}

func waitForActiveRequestToken(
	t *testing.T,
	h *faultHarness,
	requestAdmissionID string,
	timeout time.Duration,
) (string, map[string]string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	pattern := h.namespace + ":admission:v1:request:*"
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		keys, err := h.redis.Keys(ctx, pattern).Result()
		cancel()
		if err == nil && len(keys) == 1 {
			token := readRedisHash(t, h.redis, keys[0])
			if token["status"] == "active" && token["reserve_state"] == "reserved" &&
				strings.TrimPrefix(keys[0], h.namespace+":admission:v1:request:") == requestAdmissionID {
				return keys[0], token
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("did not find one active reserved request token for permit admission %q", requestAdmissionID)
	return "", nil
}

func assertOldActiveOwners(
	t *testing.T,
	epoch runtimecontrol.StateEpochRecord,
	requestToken map[string]string,
	permit map[string]string,
) {
	t.Helper()
	wantRevision := strconv.FormatInt(epoch.Revision, 10)
	for name, owner := range map[string]map[string]string{"request token": requestToken, "attempt permit": permit} {
		if owner["status"] != "active" ||
			owner["runtime_integrity_epoch"] != epoch.Value.Epoch ||
			owner["runtime_integrity_revision"] != wantRevision {
			t.Fatalf("%s is not active in the old ready epoch: %v", name, owner)
		}
	}
	if permit["request_admission_id"] == "" {
		t.Fatalf("attempt permit is not bound to the request token: %v", permit)
	}
}

func captureEpochRollbackOwnedState(
	t *testing.T,
	h *faultHarness,
	requestKey, permitKey string,
	requestToken, permit map[string]string,
) epochRollbackOwnedState {
	t.Helper()
	requestKeys := activeRollbackRedisKeys(t, h.redis, h.namespace+":admission:v1:request:*")
	permitKeys := activeRollbackRedisKeys(t, h.redis, h.namespace+":breaker:v2:permit:*")

	hashKeys := []string{
		h.namespace + ":runtime-control:v1:state-integrity-marker",
		requestKey,
		permitKey,
		h.namespace + ":breaker:v2:origin:" + formatID(h.seed.originID),
		h.namespace + ":breaker:v2:channel:" + formatID(h.seed.openAIChannelID),
	}
	hashes := make(map[string]map[string]string, len(hashKeys))
	for _, key := range hashKeys {
		hashes[key] = readRedisHash(t, h.redis, key)
	}

	counterFields := []string{
		requestToken["rpm_bucket"],
		requestToken["rpd_bucket"],
		requestToken["reserved_tpm_bucket"],
		permit["ch_rpm_bucket"],
		permit["ch_rpd_bucket"],
		permit["ch_tpm_bucket"],
	}
	counters := make(map[string]string, len(counterFields))
	for _, key := range counterFields {
		if key == "" {
			t.Fatalf("active owner is missing a stable counter key: request=%v permit=%v", requestToken, permit)
		}
		counters[key] = activeRollbackRedisString(t, h.redis, key)
	}

	zsetKeys := []string{
		requestToken["conc_key"],
		h.namespace + ":breaker:v2:channel:" + formatID(h.seed.openAIChannelID) + ":conc",
	}
	zsets := make(map[string][]string, len(zsetKeys))
	for _, key := range zsetKeys {
		if key == "" {
			t.Fatalf("active owner is missing a concurrency key: request=%v permit=%v", requestToken, permit)
		}
		zsets[key] = activeRollbackRedisZSet(t, h.redis, key)
	}

	return epochRollbackOwnedState{
		RequestKeys: requestKeys,
		PermitKeys:  permitKeys,
		Hashes:      hashes,
		Counters:    counters,
		ZSets:       zsets,
	}
}

func assertEpochRollbackOwnedStateEqual(
	t *testing.T,
	want, got epochRollbackOwnedState,
	stage string,
) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s mutated quarantined old-epoch owners:\nwant=%+v\ngot=%+v", stage, want, got)
	}
}

func activeRollbackRedisKeys(t *testing.T, client *redislib.Client, pattern string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	keys, err := client.Keys(ctx, pattern).Result()
	if err != nil {
		t.Fatalf("read Redis keys for %s: %v", pattern, err)
	}
	sort.Strings(keys)
	return keys
}

func activeRollbackRedisString(t *testing.T, client *redislib.Client, key string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	value, err := client.Get(ctx, key).Result()
	if err != nil {
		t.Fatalf("read Redis string %s: %v", key, err)
	}
	return value
}

func activeRollbackRedisZSet(t *testing.T, client *redislib.Client, key string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	entries, err := client.ZRangeWithScores(ctx, key, 0, -1).Result()
	if err != nil {
		t.Fatalf("read Redis zset %s: %v", key, err)
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		result = append(result, fmt.Sprintf("%s=%s", entry.Member, strconv.FormatFloat(entry.Score, 'f', -1, 64)))
	}
	return result
}

func waitPastEpochRollbackOwnerRenewals(
	t *testing.T,
	requestToken, permit map[string]string,
	streamResult <-chan longStreamHTTPResult,
) {
	t.Helper()
	requestRenew := redisInt64Field(t, requestToken, "renew_ms")
	permitRenew := redisInt64Field(t, permit, "renew_ms")
	wait := max(requestRenew, permitRenew)
	if wait <= 0 {
		t.Fatalf("invalid owner renew intervals request=%d permit=%d", requestRenew, permitRenew)
	}
	timer := time.NewTimer(time.Duration(wait)*time.Millisecond + 1500*time.Millisecond)
	defer timer.Stop()
	select {
	case result := <-streamResult:
		t.Fatalf("in-flight stream ended while waiting for stale owner renewals: %+v", result)
	case <-timer.C:
	}
}

func waitForActiveRollbackGatewayMetricAtLeast(
	t *testing.T,
	h *faultHarness,
	gateway *gatewayProcess,
	sample string,
	want float64,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastBody string
	var lastErr error
	for time.Now().Before(deadline) {
		status, body, err := h.get(gateway.baseURL + "/metrics")
		lastBody, lastErr = body, err
		if err == nil && status == http.StatusOK {
			for _, line := range strings.Split(body, "\n") {
				if !strings.HasPrefix(line, sample+" ") {
					continue
				}
				value, parseErr := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, sample)), 64)
				if parseErr == nil && value >= want {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Gateway metric %q did not reach %g: err=%v body=%s", sample, want, lastErr, lastBody)
}

func waitForEpochRollbackLongStreamFacts(t *testing.T, pool *pgxpool.Pool, seed seedFacts) {
	t.Helper()
	queries := sqlc.New(pool)
	deadline := time.Now().Add(8 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var requestID, requestCount, attemptCount, usageCount, debitCount int64
		err := pool.QueryRow(ctx, `
			SELECT
				COALESCE(MAX(id), 0),
				COUNT(*)
			FROM request_records
			WHERE user_id = $1
		`, seed.userID).Scan(&requestID, &requestCount)
		if err == nil && requestID > 0 {
			request, requestErr := queries.GetRequestRecordForUpdate(ctx, requestID)
			attempts, attemptsErr := queries.ListRequestAttemptsByRequest(ctx, requestID)
			factsErr := pool.QueryRow(ctx, `
				SELECT
					(SELECT COUNT(*) FROM request_attempts WHERE request_record_id = $1),
					(SELECT COUNT(*) FROM usage_records WHERE request_record_id = $1),
					(SELECT COUNT(*) FROM ledger_entries WHERE request_record_id = $1 AND entry_type = 'debit')
			`, requestID).Scan(&attemptCount, &usageCount, &debitCount)
			if requestErr == nil && attemptsErr == nil && factsErr == nil && len(attempts) == 1 {
				attempt := attempts[0]
				last = fmt.Sprintf(
					"requests=%d request=%s/%s attempts=%d attempt=%s breaker=%s/%s usage=%d debit=%d",
					requestCount,
					request.Status,
					request.DeliveryStatus,
					attemptCount,
					attempt.Status,
					attempt.BreakerOriginDisposition.String,
					attempt.BreakerChannelDisposition.String,
					usageCount,
					debitCount,
				)
				if requestCount == 1 && request.Status == "succeeded" && request.DeliveryStatus == "completed" && request.Stream &&
					request.IngressProtocol == "openai" && request.Endpoint == "chat_completions" &&
					request.ResponseStartedAt.Valid && request.ResponseCompletedAt.Valid &&
					request.FinalChannelID.Valid && request.FinalChannelID.Int64 == seed.openAIChannelID &&
					attemptCount == 1 && attempt.Status == "succeeded" && attempt.UpstreamEndpoint == "chat_completions" &&
					attempt.UpstreamStartedAt.Valid && attempt.UpstreamFirstTokenAt.Valid && attempt.UpstreamCompletedAt.Valid &&
					attempt.FinalUsageReceived && attempt.BreakerOriginDisposition.Valid &&
					attempt.BreakerOriginDisposition.String == "result_unknown" &&
					attempt.BreakerChannelDisposition.Valid && attempt.BreakerChannelDisposition.String == "result_unknown" &&
					usageCount == 1 && debitCount == 1 {
					cancel()
					return
				}
			}
		}
		cancel()
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("epoch-rollback long-stream database facts did not settle: %s", last)
}
