package p4fault_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	adminchannel "github.com/ThankCat/unio-gateway/internal/service/admin/channel"
)

const routingE2EGate = "P4_ROUTING_E2E"

type routingHTTPResult struct {
	status int
	body   string
	header http.Header
	err    error
}

type routingTraceSnapshot struct {
	status              string
	baselineOrder       []int64
	actualScanOrder     []int64
	attemptedChannelIDs []int64
	selectedChannelID   int64
	finalResult         string
	stickyAction        string
	stickyBefore        int64
	stickyAfter         int64
	capacityWaitMS      int32
	capacityWaitResult  string
	payload             routingTracePayload
}

type routingTracePayload struct {
	Candidates []struct {
		ChannelID       int64   `json:"channel_id"`
		Eligible        bool    `json:"eligible"`
		TTFTScore       float64 `json:"ttft_score"`
		TTFTSampleCount int64   `json:"ttft_sample_count"`
		ErrorScore      float64 `json:"error_score"`
		FinalScore      float64 `json:"final_score"`
		Priority        int32   `json:"priority"`
	} `json:"candidates"`
	Sticky struct {
		Pinned             bool `json:"pinned"`
		PinnedNonPreferred bool `json:"pinned_non_preferred"`
	} `json:"sticky"`
}

func TestP4RoutingScoringFallbackStickyAndTraceE2E(t *testing.T) {
	requireRoutingE2E(t)
	h := setupFaultHarnessWithOptions(t, faultHarnessOptions{disableCircuitBreaker: true})
	pool := openMaintenanceDatabase(t, h.infra.databaseURL)
	h.upstream.setMode(modeOpenAIChatNonStream)

	const session = "routing-e2e-sticky-primary"
	first := requestOpenAIChat(h, h.gateways[0], session, false)
	assertRoutingStatus(t, first, http.StatusOK, "initial sticky bind")
	firstTrace := latestRoutingTrace(t, pool, h.seed.userID)
	if firstTrace.selectedChannelID != h.seed.openAIChannelID || firstTrace.stickyAction != "bind_if_absent" ||
		firstTrace.stickyAfter != h.seed.openAIChannelID {
		t.Fatalf("initial sticky bind trace mismatch: %+v", firstTrace)
	}

	secondary := newAtomicUpstream(t)
	secondary.setMode(modeOpenAIChatNonStream)
	secondaryID := addOpenAIRoutingChannel(t, h, pool, secondary.URL(), 0, nil, 5000, nil)

	beforePrimary := h.upstream.snapshot().chat
	stickyHit := requestOpenAIChat(h, h.gateways[1], session, false)
	assertRoutingStatus(t, stickyHit, http.StatusOK, "sticky hit")
	if got := h.upstream.snapshot().chat - beforePrimary; got != 1 {
		t.Fatalf("sticky request reached primary %d times, want 1", got)
	}
	hitTrace := latestRoutingTrace(t, pool, h.seed.userID)
	assertOrder(t, hitTrace.baselineOrder, []int64{secondaryID, h.seed.openAIChannelID}, "sticky baseline")
	assertOrder(t, hitTrace.actualScanOrder, []int64{h.seed.openAIChannelID}, "sticky actual scan")
	if hitTrace.stickyAction != "refresh_if_current" || !hitTrace.payload.Sticky.PinnedNonPreferred {
		t.Fatalf("sticky hit did not preserve the non-preferred binding: %+v", hitTrace)
	}

	unbound := requestOpenAIChat(h, h.gateways[0], "", false)
	assertRoutingStatus(t, unbound, http.StatusOK, "objective order")
	scoreTrace := latestRoutingTrace(t, pool, h.seed.userID)
	if scoreTrace.selectedChannelID != secondaryID {
		t.Fatalf("objective scoring selected the wrong channel: %+v", scoreTrace)
	}
	eligibleCount := 0
	for _, candidate := range scoreTrace.payload.Candidates {
		if !candidate.Eligible {
			continue
		}
		eligibleCount++
		if candidate.TTFTSampleCount != 0 || candidate.TTFTScore != 100 || candidate.ErrorScore != 100 {
			t.Fatalf("no-sample candidate did not receive full TTFT/error score: %+v", candidate)
		}
	}
	if eligibleCount != 2 {
		t.Fatalf("eligible candidate count=%d want=2 trace=%+v", eligibleCount, scoreTrace)
	}

	secondary.setFailure(true)
	fallback := requestOpenAIChat(h, h.gateways[0], "", false)
	secondary.setFailure(false)
	assertRoutingStatus(t, fallback, http.StatusOK, "fallback")
	fallbackTrace := latestRoutingTrace(t, pool, h.seed.userID)
	assertOrder(t, fallbackTrace.attemptedChannelIDs, []int64{secondaryID, h.seed.openAIChannelID}, "fallback attempts")
	if fallbackTrace.finalResult != "success" || fallbackTrace.status != "complete" {
		t.Fatalf("fallback trace did not close successfully: %+v", fallbackTrace)
	}

	h.upstream.setFailure(true)
	permanent := requestOpenAIChat(h, h.gateways[1], session, false)
	h.upstream.setFailure(false)
	assertRoutingStatus(t, permanent, http.StatusOK, "sticky permanent failure fallback")
	permanentTrace := latestRoutingTrace(t, pool, h.seed.userID)
	assertOrder(t, permanentTrace.attemptedChannelIDs, []int64{h.seed.openAIChannelID, secondaryID}, "sticky permanent failure")
	if permanentTrace.stickyAfter != secondaryID {
		t.Fatalf("permanent sticky failure did not rebind to the successful fallback: %+v", permanentTrace)
	}

	beforeSecondary := secondary.snapshot().chat
	rebound := requestOpenAIChat(h, h.gateways[0], session, false)
	assertRoutingStatus(t, rebound, http.StatusOK, "sticky rebound")
	if got := secondary.snapshot().chat - beforeSecondary; got != 1 {
		t.Fatalf("rebound session reached secondary %d times, want 1", got)
	}
}

func TestP4RoutingConcurrencyWaitAndStickyBypassE2E(t *testing.T) {
	requireRoutingE2E(t)
	one := int64(1)
	waitMS := int64(350)
	h := setupFaultHarnessWithOptions(t, faultHarnessOptions{
		openAIConcurrencyLimit: &one,
		capacityWaitTimeoutMS:  &waitMS,
		disableCircuitBreaker:  true,
	})
	pool := openMaintenanceDatabase(t, h.infra.databaseURL)
	h.upstream.setMode(modeOpenAIChatNonStream)

	const session = "routing-e2e-capacity-sticky"
	assertRoutingStatus(t, requestOpenAIChat(h, h.gateways[0], session, false), http.StatusOK, "capacity sticky bind")
	secondary := newAtomicUpstream(t)
	secondary.setMode(modeOpenAIChatNonStream)
	secondaryID := addOpenAIRoutingChannel(t, h, pool, secondary.URL(), 0, &one, 5000, nil)

	primaryGate := h.upstream.blockNextChatNonStream()
	heldPrimary := make(chan routingHTTPResult, 1)
	go func() { heldPrimary <- requestOpenAIChat(h, h.gateways[0], session, false) }()
	waitForSignal(t, primaryGate.started, 3*time.Second, "primary capacity holder")

	temporaryBypass := requestOpenAIChat(h, h.gateways[1], session, false)
	assertRoutingStatus(t, temporaryBypass, http.StatusOK, "sticky temporary capacity bypass")
	bypassTrace := latestRoutingTrace(t, pool, h.seed.userID)
	if bypassTrace.selectedChannelID != secondaryID || bypassTrace.stickyAction != "preserve_on_temporary_bypass" ||
		bypassTrace.stickyBefore != h.seed.openAIChannelID || bypassTrace.stickyAfter != h.seed.openAIChannelID {
		t.Fatalf("temporary bypass changed the sticky binding: %+v", bypassTrace)
	}
	primaryGate.Release()
	assertRoutingStatus(t, receiveRoutingResult(t, heldPrimary, "primary capacity holder"), http.StatusOK, "primary capacity holder")

	beforePrimary := h.upstream.snapshot().chat
	assertRoutingStatus(t, requestOpenAIChat(h, h.gateways[1], session, false), http.StatusOK, "sticky after bypass")
	if got := h.upstream.snapshot().chat - beforePrimary; got != 1 {
		t.Fatalf("sticky binding was not preserved after capacity bypass: primary delta=%d", got)
	}

	primaryGate = h.upstream.blockNextChatNonStream()
	secondaryGate := secondary.blockNextChatNonStream()
	heldPrimary = make(chan routingHTTPResult, 1)
	heldSecondary := make(chan routingHTTPResult, 1)
	go func() { heldPrimary <- requestOpenAIChat(h, h.gateways[0], session, false) }()
	waitForSignal(t, primaryGate.started, 3*time.Second, "primary all-full holder")
	go func() { heldSecondary <- requestOpenAIChat(h, h.gateways[1], "", false) }()
	waitForSignal(t, secondaryGate.started, 3*time.Second, "secondary all-full holder")

	waited := make(chan routingHTTPResult, 1)
	go func() { waited <- requestOpenAIChat(h, h.gateways[1], "", false) }()
	time.Sleep(140 * time.Millisecond)
	secondaryGate.Release()
	assertRoutingStatus(t, receiveRoutingResult(t, heldSecondary, "secondary released holder"), http.StatusOK, "secondary released holder")
	assertRoutingStatus(t, receiveRoutingResult(t, waited, "bounded wait acquisition"), http.StatusOK, "bounded wait acquisition")
	acquiredTrace := latestRoutingTrace(t, pool, h.seed.userID)
	if acquiredTrace.capacityWaitResult != "acquired" || acquiredTrace.capacityWaitMS < 100 || acquiredTrace.selectedChannelID != secondaryID {
		t.Fatalf("capacity wait did not acquire released capacity: %+v", acquiredTrace)
	}
	primaryGate.Release()
	assertRoutingStatus(t, receiveRoutingResult(t, heldPrimary, "primary all-full holder"), http.StatusOK, "primary all-full holder")

	primaryGate = h.upstream.blockNextChatNonStream()
	secondaryGate = secondary.blockNextChatNonStream()
	heldPrimary = make(chan routingHTTPResult, 1)
	heldSecondary = make(chan routingHTTPResult, 1)
	go func() { heldPrimary <- requestOpenAIChat(h, h.gateways[0], session, false) }()
	waitForSignal(t, primaryGate.started, 3*time.Second, "primary exhaustion holder")
	go func() { heldSecondary <- requestOpenAIChat(h, h.gateways[1], "", false) }()
	waitForSignal(t, secondaryGate.started, 3*time.Second, "secondary exhaustion holder")

	started := time.Now()
	exhausted := requestOpenAIChat(h, h.gateways[0], "", false)
	elapsed := time.Since(started)
	if exhausted.status != http.StatusServiceUnavailable || exhausted.header.Get("Retry-After") != "1" {
		t.Fatalf("all-full response status=%d retry_after=%q body=%s", exhausted.status, exhausted.header.Get("Retry-After"), exhausted.body)
	}
	if elapsed < 250*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("all-full bounded wait elapsed=%v, want roughly %dms", elapsed, waitMS)
	}
	exhaustedTrace := latestRoutingTrace(t, pool, h.seed.userID)
	if exhaustedTrace.capacityWaitResult != "capacity_exhausted" || exhaustedTrace.finalResult != "capacity_exhausted" {
		t.Fatalf("capacity exhaustion trace mismatch: %+v", exhaustedTrace)
	}
	primaryGate.Release()
	secondaryGate.Release()
	assertRoutingStatus(t, receiveRoutingResult(t, heldPrimary, "primary exhaustion holder"), http.StatusOK, "primary exhaustion holder")
	assertRoutingStatus(t, receiveRoutingResult(t, heldSecondary, "secondary exhaustion holder"), http.StatusOK, "secondary exhaustion holder")
}

func TestP4RoutingAllCooldownReturns429E2E(t *testing.T) {
	requireRoutingE2E(t)
	cooldownMS := int64(5000)
	h := setupFaultHarnessWithOptions(t, faultHarnessOptions{
		channelCooldownMS:     &cooldownMS,
		disableCircuitBreaker: true,
	})
	pool := openMaintenanceDatabase(t, h.infra.databaseURL)
	h.upstream.setMode(modeOpenAIChatNonStream)
	secondary := newAtomicUpstream(t)
	secondary.setMode(modeOpenAIChatNonStream)
	secondaryID := addOpenAIRoutingChannel(t, h, pool, secondary.URL(), 0, nil, 5000, nil)
	h.upstream.setRateLimited(true)
	secondary.setRateLimited(true)

	first := requestOpenAIChat(h, h.gateways[0], "", false)
	if first.status != http.StatusTooManyRequests {
		t.Fatalf("first all-429 request status=%d want=429 body=%s", first.status, first.body)
	}
	firstTrace := latestRoutingTrace(t, pool, h.seed.userID)
	assertOrder(t, firstTrace.attemptedChannelIDs, []int64{secondaryID, h.seed.openAIChannelID}, "all-429 attempts")
	controls := breakerstore.NewStore(h.redis, h.namespace)
	for _, channelID := range []int64{secondaryID, h.seed.openAIChannelID} {
		remaining, err := controls.Channel429CooldownRemainingMs(context.Background(), channelID)
		if err != nil || remaining <= 0 {
			t.Fatalf("channel %d cooldown remaining=%d err=%v", channelID, remaining, err)
		}
	}

	before := h.upstream.snapshot().total + secondary.snapshot().total
	second := requestOpenAIChat(h, h.gateways[1], "", false)
	after := h.upstream.snapshot().total + secondary.snapshot().total
	if second.status != http.StatusTooManyRequests || second.header.Get("Retry-After") == "" {
		t.Fatalf("cooldown-only response status=%d retry_after=%q body=%s", second.status, second.header.Get("Retry-After"), second.body)
	}
	if after != before {
		t.Fatalf("cooldown-only request reached upstream: before=%d after=%d", before, after)
	}
	secondTrace := latestRoutingTrace(t, pool, h.seed.userID)
	if len(secondTrace.attemptedChannelIDs) != 0 || secondTrace.finalResult != "rate_limited" {
		t.Fatalf("cooldown-only trace mismatch: %+v", secondTrace)
	}
}

func TestP4RoutingTimeoutPhasesE2E(t *testing.T) {
	requireRoutingE2E(t)
	firstTokenMS := int32(180)
	streamIdleMS := int64(120)
	h := setupFaultHarnessWithOptions(t, faultHarnessOptions{
		openAIResponseTimeoutMS:   140,
		openAIFirstTokenTimeoutMS: &firstTokenMS,
		streamIdleTimeoutMS:       &streamIdleMS,
		disableCircuitBreaker:     true,
	})
	pool := openMaintenanceDatabase(t, h.infra.databaseURL)
	h.upstream.setMode(modeOpenAIChatNonStream)

	tests := []struct {
		name       string
		timing     upstreamTimingMode
		delay      time.Duration
		stream     bool
		wantStatus int
		wantPhase  string
	}{
		{name: "response_header", timing: upstreamTimingResponseHeader, delay: 360 * time.Millisecond, wantStatus: http.StatusGatewayTimeout, wantPhase: "response_header"},
		{name: "response_body", timing: upstreamTimingResponseBody, delay: 360 * time.Millisecond, wantStatus: http.StatusGatewayTimeout, wantPhase: "response_body"},
		{name: "first_token", timing: upstreamTimingFirstToken, delay: 360 * time.Millisecond, stream: true, wantStatus: http.StatusGatewayTimeout, wantPhase: "first_token"},
		{name: "stream_idle", timing: upstreamTimingStreamIdle, delay: 360 * time.Millisecond, stream: true, wantStatus: http.StatusOK, wantPhase: "stream_idle"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h.upstream.setMode(modeOpenAIChatNonStream)
			if tc.stream {
				h.upstream.setMode(modeOpenAIChatStream)
			}
			h.upstream.setTiming(tc.timing, tc.delay)
			result := requestOpenAIChat(h, h.gateways[0], "", tc.stream)
			h.upstream.setTiming(upstreamTimingNormal, 0)
			if result.err != nil || result.status != tc.wantStatus {
				t.Fatalf("timeout %s status=%d want=%d err=%v body=%s", tc.name, result.status, tc.wantStatus, result.err, result.body)
			}
			if phase := latestAttemptTimeoutPhase(t, pool, h.seed.userID); phase != tc.wantPhase {
				t.Fatalf("timeout %s stored phase=%q want=%q", tc.name, phase, tc.wantPhase)
			}
			trace := latestRoutingTrace(t, pool, h.seed.userID)
			if trace.status != "complete" || trace.finalResult != "upstream_failed" {
				t.Fatalf("timeout %s trace did not close: %+v", tc.name, trace)
			}
		})
	}
}

func requireRoutingE2E(t *testing.T) {
	t.Helper()
	if testing.Short() || os.Getenv(routingE2EGate) != "1" || os.Getenv("P4_FAULT_E2E") != "1" {
		t.Skip("set P4_FAULT_E2E=1 and P4_ROUTING_E2E=1 to run routing balance/sticky E2E")
	}
}

func requestOpenAIChat(h *faultHarness, gateway *gatewayProcess, session string, stream bool) routingHTTPResult {
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"stream":%t`, h.seed.modelID, stream)
	if stream {
		body += `,"stream_options":{"include_usage":true}`
	}
	body += "}"
	req, err := http.NewRequest(http.MethodPost, gateway.baseURL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		return routingHTTPResult{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.seed.apiKey)
	if session != "" {
		req.Header.Set("session-id", session)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return routingHTTPResult{err: err}
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return routingHTTPResult{status: resp.StatusCode, body: string(raw), header: resp.Header.Clone(), err: readErr}
}

func addOpenAIRoutingChannel(
	t *testing.T,
	h *faultHarness,
	pool *pgxpool.Pool,
	origin string,
	priority int32,
	concurrencyLimit *int64,
	responseTimeoutMS int32,
	firstTokenTimeoutMS *int32,
) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var providerID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO providers (slug, name, origin, status)
		VALUES ($1, $2, $3, 'enabled') RETURNING id
	`, "routing-e2e-provider-"+randomSuffix(t), "Routing E2E Provider", origin).Scan(&providerID); err != nil {
		t.Fatalf("insert routing E2E provider: %v", err)
	}
	var modelID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM models WHERE model_id = $1`, h.seed.modelID).Scan(&modelID); err != nil {
		t.Fatalf("read routing E2E model: %v", err)
	}
	var adapterKey string
	if err := pool.QueryRow(ctx, `SELECT adapter_key FROM channels WHERE id = $1`, h.seed.openAIChannelID).Scan(&adapterKey); err != nil {
		t.Fatalf("read primary adapter key: %v", err)
	}
	var channelID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO channels (
			provider_id, name, protocol, adapter_key, credential, status, priority,
			response_timeout_ms, first_token_timeout_ms, concurrency_limit
		) VALUES ($1, $2, 'openai', $3, 'routing-e2e-upstream-key', 'enabled', $4, $5, $6, $7)
		RETURNING id
	`, providerID, "Routing E2E Channel", adapterKey, priority, responseTimeoutMS, firstTokenTimeoutMS, concurrencyLimit).Scan(&channelID); err != nil {
		t.Fatalf("insert routing E2E channel: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO channel_models (channel_id, model_id, upstream_model, status)
		VALUES ($1, $2, $3, 'enabled')
	`, channelID, modelID, h.seed.modelID); err != nil {
		t.Fatalf("insert routing E2E channel model: %v", err)
	}
	if err := sqlc.New(pool).AddRouteChannel(ctx, sqlc.AddRouteChannelParams{RouteID: h.seed.routeID, ChannelID: channelID}); err != nil {
		t.Fatalf("bind routing E2E route channel: %v", err)
	}
	if _, err := sqlc.New(pool).CreateChannelPrice(ctx, sqlc.CreateChannelPriceParams{
		ChannelID: channelID, ModelID: modelID, Currency: "USD", PricingUnit: "per_1m_tokens",
		UncachedInputCost: numericMinor(1_0000000000), OutputCost: numericMinor(4_0000000000),
		CacheReadInputCost: numericMinor(0_2500000000), ReasoningOutputCost: numericMinor(6_0000000000),
		Status: "enabled", EffectiveFrom: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("insert routing E2E channel cost: %v", err)
	}

	controls := breakerstore.NewStore(h.redis, h.namespace)
	if _, err := controls.InitProviderControl(ctx, providerID, 1, 1, "enabled"); err != nil {
		t.Fatalf("initialize routing E2E provider control: %v", err)
	}
	row, err := sqlc.New(pool).GetChannel(ctx, channelID)
	if err != nil {
		t.Fatalf("read routing E2E channel: %v", err)
	}
	payload, err := adminchannel.CanonicalCapacityPayloadFromChannel(row)
	if err != nil {
		t.Fatalf("encode routing E2E capacity: %v", err)
	}
	if _, err := controls.RestoreMissingControl(ctx, controls.ChannelCapacityControl(channelID), row.CapacityRevision, payload); err != nil {
		t.Fatalf("initialize routing E2E capacity control: %v", err)
	}
	return channelID
}

func latestRoutingTrace(t *testing.T, pool *pgxpool.Pool, userID int64) routingTraceSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var out routingTraceSnapshot
	var payload []byte
	if err := pool.QueryRow(ctx, `
		SELECT t.trace_status, t.baseline_order, t.actual_scan_order, t.attempted_channel_ids,
			COALESCE(t.selected_channel_id, 0), COALESCE(t.final_result, ''),
			COALESCE(t.sticky_action, ''), COALESCE(t.sticky_before_channel_id, 0),
			COALESCE(t.sticky_after_channel_id, 0), COALESCE(t.capacity_wait_ms, -1),
			COALESCE(t.capacity_wait_result, ''), t.trace_payload
		FROM routing_decision_traces t
		JOIN request_records r ON r.id = t.request_record_id
		WHERE r.user_id = $1
		ORDER BY t.id DESC LIMIT 1
	`, userID).Scan(
		&out.status, &out.baselineOrder, &out.actualScanOrder, &out.attemptedChannelIDs,
		&out.selectedChannelID, &out.finalResult, &out.stickyAction, &out.stickyBefore,
		&out.stickyAfter, &out.capacityWaitMS, &out.capacityWaitResult, &payload,
	); err != nil {
		t.Fatalf("read latest routing trace: %v", err)
	}
	if err := json.Unmarshal(payload, &out.payload); err != nil {
		t.Fatalf("decode latest routing trace: %v", err)
	}
	return out
}

func latestAttemptTimeoutPhase(t *testing.T, pool *pgxpool.Pool, userID int64) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var phase string
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(a.upstream_timeout_phase, '')
		FROM request_attempts a
		JOIN request_records r ON r.id = a.request_record_id
		WHERE r.user_id = $1
		ORDER BY a.id DESC LIMIT 1
	`, userID).Scan(&phase); err != nil {
		t.Fatalf("read latest attempt timeout phase: %v", err)
	}
	return phase
}

func assertRoutingStatus(t *testing.T, result routingHTTPResult, want int, label string) {
	t.Helper()
	if result.err != nil || result.status != want {
		t.Fatalf("%s status=%d want=%d err=%v body=%s", label, result.status, want, result.err, result.body)
	}
}

func assertOrder(t *testing.T, got, want []int64, label string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s order=%v want=%v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s order=%v want=%v", label, got, want)
		}
	}
}

func receiveRoutingResult(t *testing.T, ch <-chan routingHTTPResult, label string) routingHTTPResult {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return routingHTTPResult{}
	}
}
