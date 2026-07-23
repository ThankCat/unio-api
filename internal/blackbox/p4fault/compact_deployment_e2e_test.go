package p4fault_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// TestP4CompactNativeFallbackDeploymentE2E drives the public Compact endpoint through a real
// gateway-server, PostgreSQL, Redis and HTTP upstream. Native 404/405 fallback is intentionally two
// transports under one ingress admission: each transport owns a permit and attempt, while only the
// successful synthetic result is settled.
func TestP4CompactNativeFallbackDeploymentE2E(t *testing.T) {
	if os.Getenv("P4_FAULT_E2E") != "1" {
		t.Skip("set P4_FAULT_E2E=1 to run the isolated P4 compact deployment test")
	}

	h := setupFaultHarnessWithOptions(t, faultHarnessOptions{openAIAdapterKey: "openai"})
	pool := openMaintenanceDatabase(t, h.infra.databaseURL)
	queries := sqlc.New(pool)

	for _, nativeStatus := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		mustRun(t, strconv.Itoa(nativeStatus), func(t *testing.T) {
			h.upstream.setCompactStatus(nativeStatus)
			beforeUpstream := h.upstream.snapshot()
			beforeRuntime := readCompactRuntimeSnapshot(t, h)
			beforeRequestID := maxCompactRequestID(t, pool)

			status, body := requestCompact(t, h, h.gateways[0])
			if status != http.StatusOK {
				t.Fatalf("compact status=%d want=200 body=%s gateway_log=%s", status, body, h.gateways[0].logs())
			}
			assertSyntheticCompactBody(t, body)

			afterUpstream := h.upstream.snapshot()
			if afterUpstream.total-beforeUpstream.total != 2 ||
				afterUpstream.compact-beforeUpstream.compact != 1 ||
				afterUpstream.chat-beforeUpstream.chat != 1 {
				t.Fatalf(
					"upstream transport delta total/compact/chat=%d/%d/%d, want 2/1/1",
					afterUpstream.total-beforeUpstream.total,
					afterUpstream.compact-beforeUpstream.compact,
					afterUpstream.chat-beforeUpstream.chat,
				)
			}

			afterRuntime := readCompactRuntimeSnapshot(t, h)
			assertCompactAdmissionDeltas(t, beforeRuntime, afterRuntime)
			assertCompactPermitChain(t, h, beforeRuntime, afterRuntime)
			assertCompactBreakerIgnoredThenSucceeded(t, h)

			requestID := singleCompactRequestIDAfter(t, pool, beforeRequestID)
			assertCompactDatabaseFacts(t, pool, queries, requestID, h.seed, nativeStatus)
		})
	}
}

func requestCompact(t *testing.T, h *faultHarness, gateway *gatewayProcess) (int, string) {
	t.Helper()
	h.upstream.setMode(modeOpenAIResponsesNonStream)
	body := fmt.Sprintf(
		`{"model":%q,"instructions":"compact the history","input":"long history to compact"}`,
		h.seed.modelID,
	)
	req, err := http.NewRequest(http.MethodPost, gateway.baseURL+"/v1/responses/compact", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build compact request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.seed.apiKey)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("compact through %s: %v", gateway.name, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("read compact response through %s: %v", gateway.name, err)
	}
	return resp.StatusCode, string(raw)
}

func assertSyntheticCompactBody(t *testing.T, body string) {
	t.Helper()
	var response struct {
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Status  string `json:"status"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("decode compact response: %v body=%s", err, body)
	}
	if len(response.Output) != 1 || response.Output[0].Type != "message" ||
		response.Output[0].Role != "assistant" || response.Output[0].Status != "completed" ||
		len(response.Output[0].Content) != 1 || response.Output[0].Content[0].Type != "output_text" ||
		response.Output[0].Content[0].Text != "ok" {
		t.Fatalf("unexpected synthetic compact response: %+v", response.Output)
	}
}

type compactRuntimeSnapshot struct {
	requestRPM int64
	requestRPD int64
	requestTPM int64
	channelRPM int64
	channelRPD int64
	channelTPM int64

	requestTokens map[string]struct{}
	permits       map[string]struct{}
}

func readCompactRuntimeSnapshot(t *testing.T, h *faultHarness) compactRuntimeSnapshot {
	t.Helper()
	requestPrefix := h.namespace + ":admission:v1:ru-"
	requestSuffix := formatID(h.seed.routeID) + ":" + formatID(h.seed.userID) + ":"
	channelSuffix := formatID(h.seed.openAIChannelID) + ":"
	return compactRuntimeSnapshot{
		requestRPM: redisCounterPrefix(t, h, requestPrefix+"rpm:"+requestSuffix),
		requestRPD: redisCounterPrefix(t, h, requestPrefix+"rpd:"+requestSuffix),
		requestTPM: redisCounterPrefix(t, h, requestPrefix+"tpm:"+requestSuffix),
		channelRPM: redisCounterPrefix(t, h, h.namespace+":admission:v1:ch-rpm:"+channelSuffix),
		channelRPD: redisCounterPrefix(t, h, h.namespace+":admission:v1:ch-rpd:"+channelSuffix),
		channelTPM: redisCounterPrefix(t, h, h.namespace+":admission:v1:ch-tpm:"+channelSuffix),

		requestTokens: redisKeySet(t, h, h.namespace+":admission:v1:request:*"),
		permits:       redisKeySet(t, h, h.namespace+":breaker:v2:permit:*"),
	}
}

func redisCounterPrefix(t *testing.T, h *faultHarness, prefix string) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	keys, err := h.redis.Keys(ctx, prefix+"*").Result()
	if err != nil {
		t.Fatalf("list Redis counters for %s: %v", prefix, err)
	}
	var total int64
	for _, key := range keys {
		value, err := h.redis.Get(ctx, key).Int64()
		if err != nil {
			t.Fatalf("read Redis counter %s: %v", key, err)
		}
		total += value
	}
	return total
}

func redisKeySet(t *testing.T, h *faultHarness, pattern string) map[string]struct{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	keys, err := h.redis.Keys(ctx, pattern).Result()
	if err != nil {
		t.Fatalf("list Redis keys for %s: %v", pattern, err)
	}
	result := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		result[key] = struct{}{}
	}
	return result
}

func newRedisKeys(before, after map[string]struct{}) []string {
	result := make([]string, 0, len(after))
	for key := range after {
		if _, exists := before[key]; !exists {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}

func assertCompactAdmissionDeltas(t *testing.T, before, after compactRuntimeSnapshot) {
	t.Helper()
	if after.requestRPM-before.requestRPM != 1 || after.requestRPD-before.requestRPD != 1 {
		t.Fatalf(
			"request admission RPM/RPD delta=%d/%d, want 1/1",
			after.requestRPM-before.requestRPM,
			after.requestRPD-before.requestRPD,
		)
	}
	if after.channelRPM-before.channelRPM != 2 || after.channelRPD-before.channelRPD != 2 {
		t.Fatalf(
			"channel RPM/RPD delta=%d/%d, want 2/2",
			after.channelRPM-before.channelRPM,
			after.channelRPD-before.channelRPD,
		)
	}
	if after.requestTPM-before.requestTPM != 3 || after.channelTPM-before.channelTPM != 3 {
		t.Fatalf(
			"settled request/channel TPM delta=%d/%d, want authoritative 3/3",
			after.requestTPM-before.requestTPM,
			after.channelTPM-before.channelTPM,
		)
	}
	if got := len(newRedisKeys(before.requestTokens, after.requestTokens)); got != 1 {
		t.Fatalf("new request-admission tokens=%d, want 1", got)
	}
	if got := len(newRedisKeys(before.permits, after.permits)); got != 2 {
		t.Fatalf("new attempt permits=%d, want 2", got)
	}
}

func assertCompactPermitChain(t *testing.T, h *faultHarness, before, after compactRuntimeSnapshot) {
	t.Helper()
	requestKeys := newRedisKeys(before.requestTokens, after.requestTokens)
	permitKeys := newRedisKeys(before.permits, after.permits)
	if len(requestKeys) != 1 || len(permitKeys) != 2 {
		t.Fatalf("unexpected compact Redis chain request=%v permits=%v", requestKeys, permitKeys)
	}

	requestPrefix := h.namespace + ":admission:v1:request:"
	requestAdmissionID := strings.TrimPrefix(requestKeys[0], requestPrefix)
	requestToken := readRedisHashForCompact(t, h, requestKeys[0])
	if requestAdmissionID == requestKeys[0] || requestToken["status"] != "finished" {
		t.Fatalf("request admission did not finish exactly once: key=%s token=%v", requestKeys[0], requestToken)
	}

	permitsByOperation := make(map[string]map[string]string, len(permitKeys))
	for _, key := range permitKeys {
		permit := readRedisHashForCompact(t, h, key)
		if permit["status"] != "finished" || permit["request_admission_id"] != requestAdmissionID {
			t.Fatalf("permit not bound to the one finished request admission: key=%s permit=%v", key, permit)
		}
		operation := permit["upstream_operation"]
		if _, duplicate := permitsByOperation[operation]; duplicate {
			t.Fatalf("duplicate compact permit operation %q", operation)
		}
		permitsByOperation[operation] = permit
	}

	native := permitsByOperation["responses_compact"]
	synthetic := permitsByOperation["chat_completions"]
	if native == nil || synthetic == nil {
		t.Fatalf("permit operations=%v, want responses_compact and chat_completions", permitsByOperation)
	}
	if native["endpoint_disposition"] != "not_applicable" || native["channel_disposition"] != "not_applicable" {
		t.Fatalf("native unsupported permit was not breaker-ignored: %v", native)
	}
	if synthetic["endpoint_disposition"] != "applied" || synthetic["channel_disposition"] != "applied" {
		t.Fatalf("synthetic success permit was not applied: %v", synthetic)
	}
	nativeTerminal := redisInt64Field(t, native, "terminal_at_ms")
	syntheticAcquired := redisInt64Field(t, synthetic, "acquired_at_ms")
	if nativeTerminal > syntheticAcquired {
		t.Fatalf("synthetic permit acquired before native permit finished: native_terminal=%d synthetic_acquired=%d", nativeTerminal, syntheticAcquired)
	}
}

func readRedisHashForCompact(t *testing.T, h *faultHarness, key string) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	value, err := h.redis.HGetAll(ctx, key).Result()
	if err != nil {
		t.Fatalf("read Redis hash %s: %v", key, err)
	}
	return value
}

func redisInt64Field(t *testing.T, value map[string]string, field string) int64 {
	t.Helper()
	parsed, err := strconv.ParseInt(value[field], 10, 64)
	if err != nil {
		t.Fatalf("parse Redis field %s=%q: %v", field, value[field], err)
	}
	return parsed
}

func assertCompactBreakerIgnoredThenSucceeded(t *testing.T, h *faultHarness) {
	t.Helper()
	for _, key := range []string{
		h.namespace + ":breaker:v2:endpoint:" + formatID(h.seed.endpointID),
		h.namespace + ":breaker:v2:channel:" + formatID(h.seed.openAIChannelID),
	} {
		state := readRedisHashForCompact(t, h, key)
		if state["state"] != "closed" || redisInt64Field(t, state, "eligible_failures") != 0 ||
			redisInt64Field(t, state, "consecutive_eligible_failures") != 0 ||
			redisInt64Field(t, state, "eligible_successes") < 1 {
			t.Fatalf("breaker state includes native 404/405 as a failure: key=%s state=%v", key, state)
		}
	}
}

func maxCompactRequestID(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var id int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM request_records`).Scan(&id); err != nil {
		t.Fatalf("read max compact request ID: %v", err)
	}
	return id
}

func singleCompactRequestIDAfter(t *testing.T, pool *pgxpool.Pool, previousID int64) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rows, err := pool.Query(ctx, `SELECT id FROM request_records WHERE id > $1 ORDER BY id`, previousID)
	if err != nil {
		t.Fatalf("list compact requests after %d: %v", previousID, err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan compact request ID: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate compact request IDs: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("new request records=%v, want exactly one", ids)
	}
	return ids[0]
}

func assertCompactDatabaseFacts(
	t *testing.T,
	pool *pgxpool.Pool,
	queries *sqlc.Queries,
	requestID int64,
	seed seedFacts,
	nativeStatus int,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	request, err := queries.GetRequestRecordForUpdate(ctx, requestID)
	if err != nil {
		t.Fatalf("read compact request record: %v", err)
	}
	if request.Status != "succeeded" || request.DeliveryStatus != "completed" || request.Stream ||
		request.IngressProtocol != "openai" || request.Operation != "responses" ||
		request.UserID != seed.userID || request.RequestedModelID != seed.modelID ||
		!request.RouteID.Valid || request.RouteID.Int64 != seed.routeID ||
		!request.ResponseModelID.Valid || request.ResponseModelID.String != seed.modelID ||
		!request.ResponseProtocol.Valid || request.ResponseProtocol.String != "openai" ||
		!request.FinalChannelID.Valid || request.FinalChannelID.Int64 != seed.openAIChannelID {
		t.Fatalf("unexpected compact request terminal facts: %+v", request)
	}

	attempts, err := queries.ListRequestAttemptsByRequest(ctx, requestID)
	if err != nil {
		t.Fatalf("list compact attempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("compact attempts=%d, want 2: %+v", len(attempts), attempts)
	}
	native, synthetic := attempts[0], attempts[1]
	if native.RequestRecordID != requestID || native.AttemptIndex != 0 || native.Status != "failed" ||
		native.ChannelID != seed.openAIChannelID || native.ProviderEndpointID != seed.endpointID || native.AdapterKey != "openai" ||
		native.UpstreamOperation != "responses_compact" ||
		!native.UpstreamStatusCode.Valid || int(native.UpstreamStatusCode.Int32) != nativeStatus ||
		!native.UpstreamRequestID.Valid || native.UpstreamRequestID.String != "p4-fault-compact-unsupported" ||
		!native.BreakerEndpointDisposition.Valid || native.BreakerEndpointDisposition.String != "not_applicable" ||
		!native.BreakerChannelDisposition.Valid || native.BreakerChannelDisposition.String != "not_applicable" ||
		!native.FaultParty.Valid || native.FaultParty.String != "client" {
		t.Fatalf("unexpected native compact attempt: %+v", native)
	}
	if synthetic.RequestRecordID != requestID || synthetic.AttemptIndex != 1 || synthetic.Status != "succeeded" ||
		synthetic.ChannelID != seed.openAIChannelID || synthetic.ProviderEndpointID != seed.endpointID || synthetic.AdapterKey != "openai" ||
		synthetic.UpstreamOperation != "chat_completions" ||
		!synthetic.UpstreamStatusCode.Valid || synthetic.UpstreamStatusCode.Int32 != http.StatusOK ||
		!synthetic.UpstreamRequestID.Valid || synthetic.UpstreamRequestID.String != "p4-fault-chat" ||
		!synthetic.BreakerEndpointDisposition.Valid || synthetic.BreakerEndpointDisposition.String != "applied" ||
		!synthetic.BreakerChannelDisposition.Valid || synthetic.BreakerChannelDisposition.String != "applied" ||
		!synthetic.FinalUsageReceived {
		t.Fatalf("unexpected synthetic compact attempt: %+v", synthetic)
	}
	for _, attempt := range attempts {
		if !attempt.UpstreamStartedAt.Valid || !attempt.UpstreamCompletedAt.Valid || attempt.UpstreamFirstTokenAt.Valid {
			t.Fatalf("non-stream compact transport timing is incomplete: %+v", attempt)
		}
	}
	if !native.CompletedAt.Valid || !synthetic.StartedAt.Valid || native.CompletedAt.Time.After(synthetic.StartedAt.Time) {
		t.Fatalf("attempts are not sequential: native_completed=%v synthetic_started=%v", native.CompletedAt, synthetic.StartedAt)
	}

	usageRecord, err := queries.GetUsageRecordByRequest(ctx, requestID)
	if err != nil {
		t.Fatalf("read compact usage record: %v", err)
	}
	if usageRecord.UncachedInputTokens != 2 || usageRecord.UncachedInputTokensState != "known" ||
		usageRecord.CacheReadInputTokens != 0 || usageRecord.OutputTokensTotal != 1 ||
		usageRecord.OutputTokensTotalState != "known" || usageRecord.UsageSource != "upstream_response" ||
		usageRecord.UsageMappingVersion != "openai.v2" {
		t.Fatalf("unexpected compact usage record: %+v", usageRecord)
	}

	reservation, err := queries.GetLedgerReservationByRequestRecordID(ctx, requestID)
	if err != nil {
		t.Fatalf("read compact ledger reservation: %v", err)
	}
	if reservation.Status != "captured" || !reservation.CaptureLedgerEntryID.Valid ||
		!reservation.CapturedAt.Valid {
		t.Fatalf("compact reservation was not captured once: %+v", reservation)
	}
	recoveryJob, err := queries.GetSettlementRecoveryJobByRequest(ctx, requestID)
	if err != nil {
		t.Fatalf("read compact settlement recovery audit: %v", err)
	}
	if recoveryJob.Status != "succeeded" || !recoveryJob.CompletedAt.Valid ||
		recoveryJob.AttemptID != synthetic.ID || recoveryJob.UsageUncachedInputTokens != 2 ||
		recoveryJob.UsageOutputTokensTotal != 1 {
		t.Fatalf("compact settlement recovery audit is not terminal: %+v", recoveryJob)
	}

	assertCompactRowCount(t, ctx, pool, "usage_records", requestID, 1)
	assertCompactRowCount(t, ctx, pool, "price_snapshots", requestID, 1)
	assertCompactRowCount(t, ctx, pool, "cost_snapshots", requestID, 1)
	assertCompactRowCount(t, ctx, pool, "ledger_reservations", requestID, 1)
	assertCompactRowCount(t, ctx, pool, "ledger_billing_exceptions", requestID, 0)
	assertCompactRowCount(t, ctx, pool, "channel_cost_exposures", requestID, 0)
	assertCompactRowCount(t, ctx, pool, "settlement_recovery_jobs", requestID, 1)

	var debitCount int64
	var positiveDebit, positiveCost bool
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(BOOL_AND(amount > 0), false)
		FROM ledger_entries
		WHERE request_record_id = $1 AND entry_type = 'debit'
	`, requestID).Scan(&debitCount, &positiveDebit); err != nil {
		t.Fatalf("read compact debit facts: %v", err)
	}
	if debitCount != 1 || !positiveDebit {
		t.Fatalf("compact debit count/positive=%d/%v, want 1/true", debitCount, positiveDebit)
	}
	if err := pool.QueryRow(ctx, `
		SELECT total_cost_amount > 0
		FROM cost_snapshots
		WHERE request_record_id = $1
	`, requestID).Scan(&positiveCost); err != nil {
		t.Fatalf("read compact cost facts: %v", err)
	}
	if !positiveCost {
		t.Fatal("compact final synthetic usage produced a non-positive cost")
	}
}

func assertCompactRowCount(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	table string,
	requestID int64,
	want int64,
) {
	t.Helper()
	allowed := map[string]bool{
		"usage_records":             true,
		"price_snapshots":           true,
		"cost_snapshots":            true,
		"ledger_reservations":       true,
		"ledger_billing_exceptions": true,
		"channel_cost_exposures":    true,
		"settlement_recovery_jobs":  true,
	}
	if !allowed[table] {
		t.Fatalf("unsupported compact row-count table %q", table)
	}
	var got int64
	query := "SELECT COUNT(*) FROM " + table + " WHERE request_record_id = $1"
	if err := pool.QueryRow(ctx, query, requestID).Scan(&got); err != nil {
		t.Fatalf("count %s for compact request: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s rows=%d, want %d", table, got, want)
	}
}
