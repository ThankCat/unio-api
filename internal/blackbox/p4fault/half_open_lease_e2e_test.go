package p4fault_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	redislib "github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

const (
	halfOpenPermitTTLMs   = int64(300)
	halfOpenPermitRenewMs = int64(100)
	halfOpenTerminalTTLMs = int64(3000)
	halfOpenFirstCooldown = int64(400)
	halfOpenStreamTimeout = 10 * time.Second
)

// TestP4HalfOpenLeaseRenewalAndGatewayTakeoverE2E proves two deployment
// boundaries with real Gateway processes and authoritative Redis time:
//
//   - a half-open stream remains the only current probe while its permit,
//     Channel holder, and concurrency leases renew across multiple TTLs;
//   - after that Gateway is killed without cleanup, the expired holder is
//     reclaimed and another Gateway completes two-permit recovery.
func TestP4HalfOpenLeaseRenewalAndGatewayTakeoverE2E(t *testing.T) {
	if os.Getenv("P4_FAULT_E2E") != "1" || os.Getenv("P4_HALF_OPEN_LEASE_E2E") != "1" {
		t.Skip("set P4_FAULT_E2E=1 and P4_HALF_OPEN_LEASE_E2E=1 to run the half-open lease drill")
	}

	h := setupFaultHarness(t)
	pool := openMaintenanceDatabase(t, h.infra.databaseURL)
	runtimeStore := breakerstore.NewStore(h.redis, h.namespace)
	publishShortHalfOpenBreakerConfig(t, h, pool, runtimeStore)

	opened := openChannelBreaker(t, h, runtimeStore)
	waitForChannelOpenCooldown(t, h)

	longGate := h.upstream.blockNextChatStream()
	defer longGate.Release()
	h.upstream.setMode(modeOpenAIChatStream)
	firstClientEvent := make(chan string, 1)
	longResult := make(chan longStreamHTTPResult, 1)
	go runBlockedChatStreamRequestThrough(h, h.gateways[0], firstClientEvent, longResult)

	waitForSignal(t, longGate.firstEventWritten, 5*time.Second, "long half-open upstream first event")
	assertFirstStreamEvent(t, firstClientEvent, "long half-open stream")
	longRequest := waitForRunningRevisionStream(t, pool, h.seed, 5*time.Second)
	longPermitKey, initialPermit := waitForOneActivePermit(t, h, 5*time.Second)
	assertCurrentChannelHalfOpenPermit(t, h, initialPermit)
	assertOpenToHalfOpenGeneration(t, h, runtimeStore, opened, initialPermit)
	longRequestKey := requestAdmissionKey(h, initialPermit["request_admission_id"])
	initialOwners := readActiveOwnerLeaseSnapshot(t, h, longPermitKey, longRequestKey)
	initialLease := initialOwners.permitLease
	acquiredAt := redisInt64Field(t, initialOwners.permit, "acquired_at_ms")

	beforeBusy := h.upstream.snapshot()
	status, body := h.request(t, h.gateways[1], modeOpenAIChatNonStream)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("Gateway B request while half-open probe is leased status=%d want=503 body=%s", status, body)
	}
	if afterBusy := h.upstream.snapshot(); afterBusy.total != beforeBusy.total {
		t.Fatalf("Gateway B reached upstream while half-open probe was leased: before=%d after=%d", beforeBusy.total, afterBusy.total)
	}

	waitAcrossPermitTTLs(t, h, longResult, acquiredAt+4*halfOpenPermitTTLMs)
	renewedOwners := readActiveOwnerLeaseSnapshot(t, h, longPermitKey, longRequestKey)
	assertOwnerLeasesAndPhysicalTTLsRenewed(t, initialOwners, renewedOwners)
	renewedPermit := renewedOwners.permit
	renewedLease := renewedOwners.permitLease
	redisNow := redisNowMillis(t, h)
	if renewedPermit["status"] != "active" || renewedLease <= redisNow || renewedLease <= initialLease {
		t.Fatalf(
			"long half-open permit was not current after multiple TTLs: now=%d initial_lease=%d renewed=%v",
			redisNow,
			initialLease,
			renewedPermit,
		)
	}
	assertCurrentChannelHalfOpenPermit(t, h, renewedPermit)
	waitForActiveRollbackGatewayMetricAtLeast(
		t,
		h,
		h.gateways[0],
		`unio_gateway_breaker_permit_operation_total{operation="renew",result="renewed"}`,
		3,
		2*time.Second,
	)
	waitForActiveRollbackGatewayMetricAtLeast(
		t,
		h,
		h.gateways[0],
		`unio_gateway_request_admission_operation_total{operation="renew",result="renewed"}`,
		3,
		2*time.Second,
	)

	longGate.Release()
	assertCompletedStreamResult(t, longResult, "long half-open stream")
	waitForRevisionFencedStreamFacts(
		t,
		pool,
		longRequest,
		h.seed,
		breakerstore.DispositionApplied,
		breakerstore.DispositionApplied,
		8*time.Second,
	)
	waitForRevisionPermitFinished(
		t,
		h,
		longPermitKey,
		breakerstore.DispositionApplied,
		breakerstore.DispositionApplied,
		5*time.Second,
	)
	assertHalfOpenSuccessCount(t, h, runtimeStore, 1)
	assertNoHalfOpenHolder(t, h)
	assertRuntimeConcurrencyEmpty(t, h)

	secondPermitKey, secondPermit := runCapturedSuccessfulHalfOpenStream(t, h, h.gateways[1], "")
	if secondPermitKey == longPermitKey || secondPermit["permit_id"] == initialPermit["permit_id"] {
		t.Fatalf("two half-open successes reused one permit: first=%v second=%v", initialPermit, secondPermit)
	}
	closed := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeChannel, h.seed.openAIChannelID)
	if closed.State != breakerstore.StateClosed || closed.HalfOpenBusy {
		t.Fatalf("two distinct successful permits did not close Channel breaker: %+v", closed)
	}
	assertNoHalfOpenHolder(t, h)
	assertRuntimeConcurrencyEmpty(t, h)

	reopened := openChannelBreaker(t, h, runtimeStore)
	waitForChannelOpenCooldown(t, h)

	killedGate := h.upstream.blockNextChatStream()
	defer killedGate.Release()
	h.upstream.setMode(modeOpenAIChatStream)
	killedFirstClientEvent := make(chan string, 1)
	killedResult := make(chan longStreamHTTPResult, 1)
	go runBlockedChatStreamRequestThrough(h, h.gateways[0], killedFirstClientEvent, killedResult)

	waitForSignal(t, killedGate.firstEventWritten, 5*time.Second, "killed half-open upstream first event")
	assertFirstStreamEvent(t, killedFirstClientEvent, "killed half-open stream")
	killedPermitKey, killedPermitBefore := waitForOneActivePermit(t, h, 5*time.Second)
	assertCurrentChannelHalfOpenPermit(t, h, killedPermitBefore)
	assertOpenToHalfOpenGeneration(t, h, runtimeStore, reopened, killedPermitBefore)

	h.gateways[0].killAbruptly(t)
	killedPermit := readRedisHashForCompact(t, h, killedPermitKey)
	frozenLease := redisInt64Field(t, killedPermit, "lease_until_ms")
	killedRequestKey := h.namespace + ":admission:v1:request:" + killedPermit["request_admission_id"]
	killedRequest := readRedisHashForCompact(t, h, killedRequestKey)
	frozenRequestLease := redisInt64Field(t, killedRequest, "lease_until_ms")
	killedGate.Release()
	assertKilledStreamResult(t, killedResult)

	waitForRedisMillis(t, h, max(frozenLease, frozenRequestLease)+1)
	assertKilledOwnersExpiredWithoutTerminal(t, h, killedPermitKey, killedRequestKey)
	preTakeover := readRedisHashForCompact(t, h, channelStateKey(h))
	if preTakeover["state"] != string(breakerstore.StateHalfOpen) ||
		preTakeover["half_open_permit_id"] != killedPermit["permit_id"] ||
		redisInt64Field(t, preTakeover, "half_open_lease_until_ms") != frozenLease {
		t.Fatalf("killed Gateway holder changed before lease reclamation: %v", preTakeover)
	}

	firstReplacementKey, firstReplacement := runCapturedSuccessfulHalfOpenStream(
		t,
		h,
		h.gateways[1],
		killedPermitKey,
	)
	if firstReplacement["permit_id"] == killedPermit["permit_id"] {
		t.Fatalf("Gateway B reused killed Gateway permit: killed=%v replacement=%v", killedPermit, firstReplacement)
	}
	assertHalfOpenSuccessCount(t, h, runtimeStore, 1)
	assertNoHalfOpenHolder(t, h)

	secondReplacementKey, secondReplacement := runCapturedSuccessfulHalfOpenStream(
		t,
		h,
		h.gateways[1],
		killedPermitKey,
	)
	if secondReplacementKey == firstReplacementKey ||
		secondReplacement["permit_id"] == firstReplacement["permit_id"] ||
		secondReplacement["permit_id"] == killedPermit["permit_id"] {
		t.Fatalf(
			"Gateway B recovery permits are not distinct: killed=%v first=%v second=%v",
			killedPermit,
			firstReplacement,
			secondReplacement,
		)
	}

	closedAfterTakeover := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeChannel, h.seed.openAIChannelID)
	if closedAfterTakeover.State != breakerstore.StateClosed || closedAfterTakeover.HalfOpenBusy {
		t.Fatalf("Gateway B did not close Channel after two replacement permits: %+v", closedAfterTakeover)
	}
	assertNoHalfOpenHolder(t, h)
	assertRuntimeConcurrencyEmpty(t, h)
}

func publishShortHalfOpenBreakerConfig(
	t *testing.T,
	h *faultHarness,
	pool *pgxpool.Pool,
	runtimeStore *breakerstore.Store,
) {
	t.Helper()
	queries := sqlc.New(pool)
	settingsStore := appsettings.NewSettingsStore(
		queries,
		h.redis,
		h.namespace,
		appsettings.DefaultRegistry(),
		zap.NewNop(),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	record, err := settingsStore.Record(ctx, appsettings.GatewayCircuitBreakerKey)
	cancel()
	if err != nil {
		t.Fatalf("read current circuit-breaker setting: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(record.Value, &document); err != nil {
		t.Fatalf("decode current circuit-breaker setting: %v", err)
	}
	document["attempt_permit_ttl_ms"] = halfOpenPermitTTLMs
	document["attempt_permit_renew_interval_ms"] = halfOpenPermitRenewMs
	document["attempt_permit_terminal_ttl_ms"] = halfOpenTerminalTTLMs
	document["open_durations_ms"] = []int64{halfOpenFirstCooldown, 800, 1200}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode short circuit-breaker setting: %v", err)
	}

	publisher := runtimecontrol.NewPublisher(pool, runtimeStore)
	service := appsettings.NewServiceWithRuntimeControl(settingsStore, publisher, runtimeStore)
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	result, err := service.SetRawWithResult(ctx, appsettings.GatewayCircuitBreakerKey, raw)
	cancel()
	if err != nil {
		t.Fatalf("publish short circuit-breaker setting through production service: %v", err)
	}
	if result.State != "active" || result.Revision != record.Revision+1 || result.ActiveRevision != result.Revision {
		t.Fatalf("short circuit-breaker setting is not active: before=%d result=%+v", record.Revision, result)
	}
}

func openChannelBreaker(
	t *testing.T,
	h *faultHarness,
	runtimeStore *breakerstore.Store,
) breakerstore.ScopeSnapshot {
	t.Helper()
	h.upstream.setFailure(true)
	defer h.upstream.setFailure(false)
	for attempt := 1; attempt <= 3; attempt++ {
		before := h.upstream.snapshot()
		status, body := h.request(t, h.gateways[0], modeOpenAIChatNonStream)
		if status < 500 {
			t.Fatalf("Channel breaker trigger %d status=%d want=5xx body=%s", attempt, status, body)
		}
		if after := h.upstream.snapshot(); after.total != before.total+1 {
			t.Fatalf("Channel breaker trigger %d upstream delta=%d want=1", attempt, after.total-before.total)
		}
	}
	h.upstream.setFailure(false)
	channel := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeChannel, h.seed.openAIChannelID)
	endpoint := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeEndpoint, h.seed.endpointID)
	if channel.State != breakerstore.StateOpen {
		t.Fatalf("three attributable failures did not open Channel: %+v", channel)
	}
	if endpoint.State != breakerstore.StateClosed {
		t.Fatalf("single-Channel HTTP 500 evidence unexpectedly opened Endpoint: %+v", endpoint)
	}
	return channel
}

func waitForChannelOpenCooldown(t *testing.T, h *faultHarness) {
	t.Helper()
	state := readRedisHashForCompact(t, h, channelStateKey(h))
	if state["state"] != string(breakerstore.StateOpen) {
		t.Fatalf("Channel is not open before cooldown wait: %v", state)
	}
	openUntil := redisInt64Field(t, state, "open_until_ms")
	if openUntil <= 0 {
		t.Fatalf("Channel open state has no deadline: %v", state)
	}
	waitForRedisMillis(t, h, openUntil+1)
}

func waitAcrossPermitTTLs(
	t *testing.T,
	h *faultHarness,
	streamResult <-chan longStreamHTTPResult,
	targetMs int64,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case result := <-streamResult:
			t.Fatalf("half-open stream ended before crossing multiple permit TTLs: %+v", result)
		default:
		}
		if redisNowMillis(t, h) >= targetMs {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Redis time did not reach %d while half-open stream was active", targetMs)
}

func runCapturedSuccessfulHalfOpenStream(
	t *testing.T,
	h *faultHarness,
	gateway *gatewayProcess,
	excludedPermitKey string,
) (string, map[string]string) {
	t.Helper()
	h.upstream.setMode(modeOpenAIChatStream)
	gate := h.upstream.blockNextChatStream()
	defer gate.Release()
	firstClientEvent := make(chan string, 1)
	result := make(chan longStreamHTTPResult, 1)
	go runBlockedChatStreamRequestThrough(h, gateway, firstClientEvent, result)
	waitForSignal(t, gate.firstEventWritten, 5*time.Second, gateway.name+" half-open first event")
	assertFirstStreamEvent(t, firstClientEvent, gateway.name+" half-open stream")

	var permitKey string
	var permit map[string]string
	if excludedPermitKey == "" {
		permitKey, permit = waitForOneActivePermit(t, h, 5*time.Second)
	} else {
		permitKey, permit = waitForActivePermitExcluding(t, h, excludedPermitKey, 5*time.Second)
	}
	assertCurrentChannelHalfOpenPermit(t, h, permit)
	gate.Release()
	assertCompletedStreamResult(t, result, gateway.name+" half-open stream")
	waitForRevisionPermitFinished(
		t,
		h,
		permitKey,
		breakerstore.DispositionApplied,
		breakerstore.DispositionApplied,
		5*time.Second,
	)
	return permitKey, permit
}

func assertFirstStreamEvent(t *testing.T, firstClientEvent <-chan string, name string) {
	t.Helper()
	select {
	case first := <-firstClientEvent:
		if !strings.Contains(first, "data:") || !strings.Contains(first, "ok") {
			t.Fatalf("%s first SSE event is incomplete: %q", name, first)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not reach the customer", name)
	}
}

func assertCompletedStreamResult(t *testing.T, result <-chan longStreamHTTPResult, name string) {
	t.Helper()
	select {
	case completed := <-result:
		if completed.err != nil || completed.status != http.StatusOK || !strings.Contains(completed.body, "data: [DONE]") {
			t.Fatalf("%s did not complete: status=%d err=%v body=%s", name, completed.status, completed.err, completed.body)
		}
	case <-time.After(halfOpenStreamTimeout):
		t.Fatalf("%s did not complete after upstream release", name)
	}
}

func assertKilledStreamResult(t *testing.T, result <-chan longStreamHTTPResult) {
	t.Helper()
	select {
	case completed := <-result:
		if completed.status != http.StatusOK || !strings.Contains(completed.body, "ok") ||
			strings.Contains(completed.body, "data: [DONE]") {
			t.Fatalf("killed Gateway stream unexpectedly completed: status=%d err=%v body=%s", completed.status, completed.err, completed.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("customer connection did not close after Gateway SIGKILL")
	}
}

func assertCurrentChannelHalfOpenPermit(t *testing.T, h *faultHarness, permit map[string]string) {
	t.Helper()
	permitID := permit["permit_id"]
	permitKey := h.namespace + ":breaker:v2:permit:" + permitID
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var permitCmd *redislib.MapStringStringCmd
	var stateCmd *redislib.MapStringStringCmd
	var scoreCmd *redislib.FloatCmd
	_, err := h.redis.TxPipelined(ctx, func(pipe redislib.Pipeliner) error {
		permitCmd = pipe.HGetAll(ctx, permitKey)
		stateCmd = pipe.HGetAll(ctx, channelStateKey(h))
		scoreCmd = pipe.ZScore(ctx, channelConcurrencyKey(h), permitID)
		return nil
	})
	if err != nil {
		t.Fatalf("read coherent half-open ownership snapshot: %v", err)
	}
	permit = permitCmd.Val()
	state := stateCmd.Val()
	score, scoreErr := scoreCmd.Result()
	if permit["status"] != "active" ||
		permit["channel_id"] != formatID(h.seed.openAIChannelID) ||
		permit["channel_half_open_probe"] != "1" ||
		permit["endpoint_half_open_probe"] != "0" ||
		redisInt64Field(t, permit, "permit_ttl_ms") != halfOpenPermitTTLMs ||
		redisInt64Field(t, permit, "renew_ms") != halfOpenPermitRenewMs ||
		redisInt64Field(t, permit, "terminal_ttl_ms") != halfOpenTerminalTTLMs {
		t.Fatalf("unexpected current Channel half-open permit: %v", permit)
	}
	lease := redisInt64Field(t, permit, "lease_until_ms")
	if state["state"] != string(breakerstore.StateHalfOpen) ||
		state["half_open_permit_id"] != permit["permit_id"] ||
		redisInt64Field(t, state, "half_open_lease_until_ms") != lease {
		t.Fatalf("Channel half-open ownership does not match permit: state=%v permit=%v", state, permit)
	}
	if scoreErr != nil || int64(score) != lease {
		t.Fatalf("Channel concurrency lease does not match permit: score=%v lease=%d err=%v", score, lease, scoreErr)
	}
}

type activeOwnerLeaseSnapshot struct {
	capturedAt time.Time
	permit     map[string]string
	request    map[string]string

	permitLease  int64
	requestLease int64
	channelScore int64
	routeScore   int64
	pttls        map[string]time.Duration
}

func readActiveOwnerLeaseSnapshot(
	t *testing.T,
	h *faultHarness,
	permitKey string,
	requestKey string,
) activeOwnerLeaseSnapshot {
	t.Helper()
	permitID := strings.TrimPrefix(permitKey, h.namespace+":breaker:v2:permit:")
	requestID := strings.TrimPrefix(requestKey, h.namespace+":admission:v1:request:")
	if permitID == permitKey || requestID == requestKey || permitID == "" || requestID == "" {
		t.Fatalf("invalid owner keys permit=%q request=%q", permitKey, requestKey)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var permitCmd, requestCmd *redislib.MapStringStringCmd
	var channelScoreCmd, routeScoreCmd *redislib.FloatCmd
	var permitPTTLCmd, requestPTTLCmd, channelPTTLCmd, routePTTLCmd *redislib.DurationCmd
	_, err := h.redis.TxPipelined(ctx, func(pipe redislib.Pipeliner) error {
		permitCmd = pipe.HGetAll(ctx, permitKey)
		requestCmd = pipe.HGetAll(ctx, requestKey)
		channelScoreCmd = pipe.ZScore(ctx, channelConcurrencyKey(h), permitID)
		routeScoreCmd = pipe.ZScore(ctx, routeConcurrencyKey(h), requestID)
		permitPTTLCmd = pipe.PTTL(ctx, permitKey)
		requestPTTLCmd = pipe.PTTL(ctx, requestKey)
		channelPTTLCmd = pipe.PTTL(ctx, channelConcurrencyKey(h))
		routePTTLCmd = pipe.PTTL(ctx, routeConcurrencyKey(h))
		return nil
	})
	if err != nil {
		t.Fatalf("read coherent active-owner lease snapshot: %v", err)
	}

	snapshot := activeOwnerLeaseSnapshot{
		capturedAt:   time.Now(),
		permit:       permitCmd.Val(),
		request:      requestCmd.Val(),
		channelScore: int64(channelScoreCmd.Val()),
		routeScore:   int64(routeScoreCmd.Val()),
		pttls: map[string]time.Duration{
			"permit":              permitPTTLCmd.Val(),
			"request":             requestPTTLCmd.Val(),
			"channel_concurrency": channelPTTLCmd.Val(),
			"route_concurrency":   routePTTLCmd.Val(),
		},
	}
	snapshot.permitLease = redisInt64Field(t, snapshot.permit, "lease_until_ms")
	snapshot.requestLease = redisInt64Field(t, snapshot.request, "lease_until_ms")
	if snapshot.permit["status"] != "active" || snapshot.permit["permit_id"] != permitID ||
		snapshot.permit["request_admission_id"] != requestID {
		t.Fatalf("attempt permit is not the expected active owner: %v", snapshot.permit)
	}
	if snapshot.request["status"] != "active" || snapshot.request["reserve_state"] != "reserved" {
		t.Fatalf("request token is not active and reserved: %v", snapshot.request)
	}
	if snapshot.channelScore != snapshot.permitLease || snapshot.routeScore != snapshot.requestLease {
		t.Fatalf("owner leases and concurrency scores differ: %+v", snapshot)
	}
	for name, pttl := range snapshot.pttls {
		if pttl <= 0 {
			t.Fatalf("active owner resource %s has no physical TTL: %+v", name, snapshot)
		}
	}
	return snapshot
}

func assertOwnerLeasesAndPhysicalTTLsRenewed(
	t *testing.T,
	before activeOwnerLeaseSnapshot,
	after activeOwnerLeaseSnapshot,
) {
	t.Helper()
	elapsedMs := after.capturedAt.Sub(before.capturedAt).Milliseconds()
	if elapsedMs < 3*halfOpenPermitTTLMs {
		t.Fatalf("owner snapshots did not span multiple permit TTLs: elapsed_ms=%d", elapsedMs)
	}
	if after.permitLease <= before.permitLease || after.requestLease <= before.requestLease ||
		after.channelScore <= before.channelScore || after.routeScore <= before.routeScore {
		t.Fatalf("not every owner lease advanced: before=%+v after=%+v", before, after)
	}

	for name, beforePTTL := range before.pttls {
		afterPTTL, ok := after.pttls[name]
		if !ok {
			t.Fatalf("renewed owner snapshot omitted %s PTTL", name)
		}
		beforeMs := beforePTTL.Milliseconds()
		afterMs := afterPTTL.Milliseconds()
		refreshMs := afterMs - (beforeMs - elapsedMs)
		if beforeMs <= halfOpenTerminalTTLMs || afterMs <= halfOpenTerminalTTLMs ||
			refreshMs < 2*halfOpenPermitTTLMs {
			t.Fatalf(
				"%s physical TTL was not refreshed: before_ms=%d after_ms=%d elapsed_ms=%d refresh_ms=%d",
				name,
				beforeMs,
				afterMs,
				elapsedMs,
				refreshMs,
			)
		}
	}
}

func assertOpenToHalfOpenGeneration(
	t *testing.T,
	h *faultHarness,
	runtimeStore *breakerstore.Store,
	opened breakerstore.ScopeSnapshot,
	permit map[string]string,
) {
	t.Helper()
	halfOpen := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeChannel, h.seed.openAIChannelID)
	permitGeneration := redisInt64Field(t, permit, "channel_state_generation")
	if opened.State != breakerstore.StateOpen || halfOpen.State != breakerstore.StateHalfOpen ||
		halfOpen.StateGeneration != opened.StateGeneration+1 ||
		permitGeneration != halfOpen.StateGeneration {
		t.Fatalf("Channel open->half-open generation is not monotonic: open=%+v half_open=%+v permit=%v", opened, halfOpen, permit)
	}
}

func assertHalfOpenSuccessCount(
	t *testing.T,
	h *faultHarness,
	runtimeStore *breakerstore.Store,
	want int64,
) {
	t.Helper()
	snapshot := mustScopeSnapshot(t, runtimeStore, breakerstore.ScopeChannel, h.seed.openAIChannelID)
	state := readRedisHashForCompact(t, h, channelStateKey(h))
	if snapshot.State != breakerstore.StateHalfOpen ||
		state["state"] != string(breakerstore.StateHalfOpen) ||
		redisInt64Field(t, state, "half_open_successes") != want {
		t.Fatalf("Channel did not retain %d half-open success(es): snapshot=%+v state=%v", want, snapshot, state)
	}
}

func assertNoHalfOpenHolder(t *testing.T, h *faultHarness) {
	t.Helper()
	state := readRedisHashForCompact(t, h, channelStateKey(h))
	if state["half_open_permit_id"] != "" || state["half_open_lease_until_ms"] != "" {
		t.Fatalf("Channel retained a half-open holder: %v", state)
	}
}

func assertRuntimeConcurrencyEmpty(t *testing.T, h *faultHarness) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	keys := []string{
		channelConcurrencyKey(h),
		routeConcurrencyKey(h),
	}
	for _, key := range keys {
		used, err := h.redis.ZCard(ctx, key).Result()
		if err != nil || used != 0 {
			t.Fatalf("runtime concurrency was not released: key=%s used=%d err=%v", key, used, err)
		}
	}
}

func assertKilledOwnersExpiredWithoutTerminal(
	t *testing.T,
	h *faultHarness,
	permitKey string,
	requestKey string,
) {
	t.Helper()
	now := redisNowMillis(t, h)
	permit := readRedisHashForCompact(t, h, permitKey)
	request := readRedisHashForCompact(t, h, requestKey)
	if permit["status"] != "active" || redisInt64Field(t, permit, "lease_until_ms") >= now {
		t.Fatalf("killed attempt permit was completed or did not expire: now=%d permit=%v", now, permit)
	}
	if request["status"] != "active" || redisInt64Field(t, request, "lease_until_ms") >= now {
		t.Fatalf("killed request admission was completed or did not expire: now=%d request=%v", now, request)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	permitPTTL, permitTTLErr := h.redis.PTTL(ctx, permitKey).Result()
	requestPTTL, requestTTLErr := h.redis.PTTL(ctx, requestKey).Result()
	channelScore, channelErr := h.redis.ZScore(ctx, channelConcurrencyKey(h), permit["permit_id"]).Result()
	routeScore, routeErr := h.redis.ZScore(ctx, routeConcurrencyKey(h), permit["request_admission_id"]).Result()
	if permitTTLErr != nil || requestTTLErr != nil || permitPTTL <= 0 || requestPTTL <= 0 {
		t.Fatalf(
			"expired killed owners disappeared before takeover: permit_pttl=%s permit_err=%v request_pttl=%s request_err=%v",
			permitPTTL,
			permitTTLErr,
			requestPTTL,
			requestTTLErr,
		)
	}
	if channelErr != nil || int64(channelScore) != redisInt64Field(t, permit, "lease_until_ms") ||
		routeErr != nil || int64(routeScore) != redisInt64Field(t, request, "lease_until_ms") {
		t.Fatalf(
			"killed expired leases were not retained for takeover reclamation: channel_score=%v channel_err=%v route_score=%v route_err=%v",
			channelScore,
			channelErr,
			routeScore,
			routeErr,
		)
	}
}

func waitForRedisMillis(t *testing.T, h *faultHarness, target int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if redisNowMillis(t, h) >= target {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Redis time did not reach %d", target)
}

func redisNowMillis(t *testing.T, h *faultHarness) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	now, err := h.redis.Time(ctx).Result()
	if err != nil {
		t.Fatalf("read authoritative Redis time: %v", err)
	}
	return now.UnixMilli()
}

func channelStateKey(h *faultHarness) string {
	return h.namespace + ":breaker:v2:channel:" + formatID(h.seed.openAIChannelID)
}

func channelConcurrencyKey(h *faultHarness) string {
	return channelStateKey(h) + ":conc"
}

func requestAdmissionKey(h *faultHarness, requestAdmissionID string) string {
	return h.namespace + ":admission:v1:request:" + requestAdmissionID
}

func routeConcurrencyKey(h *faultHarness) string {
	return h.namespace + ":admission:v1:ru-conc:" + formatID(h.seed.routeID) + ":" + formatID(h.seed.userID)
}
