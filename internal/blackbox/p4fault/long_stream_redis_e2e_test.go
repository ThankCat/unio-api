package p4fault_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// TestP4InFlightLongStreamRedisFaultE2E proves that a stream which has already
// started remains a customer/billing fact while Redis is unavailable. New work
// fails closed, and the same permit renewer resumes after data-preserving recovery.
func TestP4InFlightLongStreamRedisFaultE2E(t *testing.T) {
	if os.Getenv("P4_FAULT_E2E") != "1" || os.Getenv("P4_LONG_STREAM_E2E") != "1" {
		t.Skip("set P4_FAULT_E2E=1 and P4_LONG_STREAM_E2E=1 to run the in-flight stream Redis fault drill")
	}

	h := setupFaultHarness(t)
	pool := openMaintenanceDatabase(t, h.infra.databaseURL)
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

	permitKey, initialPermit := waitForOneActivePermit(t, h, 5*time.Second)
	initialLease := redisInt64Field(t, initialPermit, "lease_until_ms")
	acquiredAt := redisInt64Field(t, initialPermit, "acquired_at_ms")
	renewMs := redisInt64Field(t, initialPermit, "renew_ms")
	if initialPermit["channel_id"] != formatID(h.seed.openAIChannelID) || renewMs <= 0 {
		t.Fatalf("unexpected active long-stream permit: %v", initialPermit)
	}

	beforeRejectedRequest := h.upstream.snapshot()
	h.infra.stopRedis(t)
	for _, gateway := range h.gateways {
		h.waitReadiness(t, gateway, http.StatusServiceUnavailable, 5*time.Second)
	}
	status, body := h.request(t, h.gateways[1], modeOpenAIChatNonStream)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("new request during Redis fault status=%d want=503 body=%s", status, body)
	}
	if after := h.upstream.snapshot(); after.total != beforeRejectedRequest.total {
		t.Fatalf("new request reached upstream during Redis fault: before=%d after=%d", beforeRejectedRequest.total, after.total)
	}

	// Keep Redis down past the permit's first scheduled renewal. The failed renew
	// must not cancel or finish the already-started customer stream.
	waitUntil := time.UnixMilli(acquiredAt + renewMs).Add(1500 * time.Millisecond)
	if delay := time.Until(waitUntil); delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case result := <-streamResult:
			timer.Stop()
			t.Fatalf("in-flight stream ended during Redis outage: %+v", result)
		case <-timer.C:
		}
	}
	select {
	case result := <-streamResult:
		t.Fatalf("in-flight stream ended before Redis recovery: %+v", result)
	default:
	}

	h.infra.startRedis(t)
	h.waitRedis(t, 20*time.Second)
	for _, gateway := range h.gateways {
		h.waitReadiness(t, gateway, http.StatusOK, 20*time.Second)
	}
	renewedPermit := waitForPermitLeaseAdvance(t, h, permitKey, initialLease, 15*time.Second)
	if renewedPermit["status"] != "active" {
		t.Fatalf("long-stream permit stopped being active before upstream tail: %v", renewedPermit)
	}

	gate.Release()
	var completed longStreamHTTPResult
	select {
	case completed = <-streamResult:
	case <-time.After(10 * time.Second):
		t.Fatal("long stream did not complete after Redis recovery and upstream release")
	}
	if completed.err != nil || completed.status != http.StatusOK ||
		!strings.Contains(completed.body, "[DONE]") {
		t.Fatalf("long stream result after Redis recovery: status=%d err=%v body=%s", completed.status, completed.err, completed.body)
	}

	waitForLongStreamDatabaseFacts(t, pool, h.seed)
	assertLongStreamRuntimeReleased(t, h, permitKey)
}

type longStreamHTTPResult struct {
	status int
	body   string
	err    error
}

func runBlockedChatStreamRequest(
	h *faultHarness,
	firstClientEvent chan<- string,
	result chan<- longStreamHTTPResult,
) {
	runBlockedChatStreamRequestThrough(h, h.gateways[0], firstClientEvent, result)
}

func runBlockedChatStreamRequestThrough(
	h *faultHarness,
	gateway *gatewayProcess,
	firstClientEvent chan<- string,
	result chan<- longStreamHTTPResult,
) {
	path, body := requestForMode(modeOpenAIChatStream, h.seed.modelID)
	req, err := http.NewRequest(http.MethodPost, gateway.baseURL+path, strings.NewReader(body))
	if err != nil {
		result <- longStreamHTTPResult{err: err}
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.seed.apiKey)
	client := &http.Client{Timeout: time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		result <- longStreamHTTPResult{err: err}
		return
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var response strings.Builder
	for {
		line, readErr := reader.ReadString('\n')
		response.WriteString(line)
		if readErr != nil {
			result <- longStreamHTTPResult{status: resp.StatusCode, body: response.String(), err: readErr}
			return
		}
		if line == "\n" {
			break
		}
	}
	firstClientEvent <- response.String()
	tail, readErr := io.ReadAll(io.LimitReader(reader, 1<<20))
	response.Write(tail)
	result <- longStreamHTTPResult{status: resp.StatusCode, body: response.String(), err: readErr}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, timeout time.Duration, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitForOneActivePermit(t *testing.T, h *faultHarness, timeout time.Duration) (string, map[string]string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	pattern := h.namespace + ":breaker:v2:permit:*"
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		keys, err := h.redis.Keys(ctx, pattern).Result()
		cancel()
		if err == nil {
			var activeKey string
			var activePermit map[string]string
			activeCount := 0
			for _, key := range keys {
				permit := readRedisHashForCompact(t, h, key)
				if permit["status"] == "active" {
					activeKey = key
					activePermit = permit
					activeCount++
				}
			}
			if activeCount == 1 {
				return activeKey, activePermit
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("did not find exactly one active permit among keys matching %s", pattern)
	return "", nil
}

func waitForPermitLeaseAdvance(
	t *testing.T,
	h *faultHarness,
	permitKey string,
	initialLease int64,
	timeout time.Duration,
) map[string]string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last map[string]string
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		last, lastErr = h.redis.HGetAll(ctx, permitKey).Result()
		cancel()
		if lastErr == nil && redisInt64FieldValue(last, "lease_until_ms") > initialLease {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("permit lease did not advance after Redis recovery: initial=%d last=%v err=%v", initialLease, last, lastErr)
	return nil
}

func redisInt64FieldValue(value map[string]string, field string) int64 {
	var parsed int64
	_, _ = fmt.Sscan(value[field], &parsed)
	return parsed
}

func waitForLongStreamDatabaseFacts(t *testing.T, pool *pgxpool.Pool, seed seedFacts) {
	t.Helper()
	queries := sqlc.New(pool)
	deadline := time.Now().Add(8 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var requestID int64
		err := pool.QueryRow(ctx, `
			SELECT id
			FROM request_records
			WHERE user_id = $1
			ORDER BY id DESC
			LIMIT 1
		`, seed.userID).Scan(&requestID)
		if err == nil {
			request, requestErr := queries.GetRequestRecordForUpdate(ctx, requestID)
			attempts, attemptsErr := queries.ListRequestAttemptsByRequest(ctx, requestID)
			var usageCount, debitCount int64
			factsErr := pool.QueryRow(ctx, `
				SELECT
					(SELECT COUNT(*) FROM usage_records WHERE request_record_id = $1),
					(SELECT COUNT(*) FROM ledger_entries WHERE request_record_id = $1 AND entry_type = 'debit')
			`, requestID).Scan(&usageCount, &debitCount)
			if requestErr == nil && attemptsErr == nil && factsErr == nil && len(attempts) == 1 {
				attempt := attempts[0]
				last = fmt.Sprintf(
					"request=%s/%s attempt=%s usage=%d debit=%d",
					request.Status, request.DeliveryStatus, attempt.Status, usageCount, debitCount,
				)
				if request.Status == "succeeded" && request.DeliveryStatus == "completed" && request.Stream &&
					request.IngressProtocol == "openai" && request.Endpoint == "chat_completions" &&
					request.ResponseStartedAt.Valid && request.ResponseCompletedAt.Valid &&
					request.FinalChannelID.Valid && request.FinalChannelID.Int64 == seed.openAIChannelID &&
					attempt.Status == "succeeded" && attempt.UpstreamEndpoint == "chat_completions" &&
					attempt.UpstreamStartedAt.Valid && attempt.UpstreamFirstTokenAt.Valid && attempt.UpstreamCompletedAt.Valid &&
					attempt.FinalUsageReceived && attempt.BreakerOriginDisposition.Valid &&
					attempt.BreakerOriginDisposition.String == "applied" && attempt.BreakerChannelDisposition.Valid &&
					attempt.BreakerChannelDisposition.String == "applied" && usageCount == 1 && debitCount == 1 {
					cancel()
					return
				}
			}
		}
		cancel()
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("long-stream database facts did not settle: %s", last)
}

func assertLongStreamRuntimeReleased(t *testing.T, h *faultHarness, permitKey string) {
	t.Helper()
	permit := readRedisHashForCompact(t, h, permitKey)
	if permit["status"] != "finished" || permit["origin_disposition"] != "applied" ||
		permit["channel_disposition"] != "applied" {
		t.Fatalf("long-stream permit did not finish after recovery: %v", permit)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	channelConcurrency := h.namespace + ":breaker:v2:channel:" + formatID(h.seed.openAIChannelID) + ":conc"
	requestConcurrency := h.namespace + ":admission:v1:ru-conc:" + formatID(h.seed.routeID) + ":" + formatID(h.seed.userID)
	for _, key := range []string{channelConcurrency, requestConcurrency} {
		used, err := h.redis.ZCard(ctx, key).Result()
		if err != nil || used != 0 {
			t.Fatalf("long-stream concurrency was not released: key=%s used=%d err=%v", key, used, err)
		}
	}
}
