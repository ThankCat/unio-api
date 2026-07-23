package p4fault_test

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	redislib "github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/core/apikey"
	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

const (
	postgresImage = "postgres:16-alpine"
	redisImage    = "redis:7-alpine"
)

type protocolMode int32

const (
	modeOpenAIChatNonStream protocolMode = iota
	modeOpenAIChatStream
	modeOpenAIResponsesNonStream
	modeOpenAIResponsesStream
	modeAnthropicMessagesNonStream
	modeAnthropicMessagesStream
	modeCount
)

var allProtocolModes = []protocolMode{
	modeOpenAIChatNonStream,
	modeOpenAIChatStream,
	modeOpenAIResponsesNonStream,
	modeOpenAIResponsesStream,
	modeAnthropicMessagesNonStream,
	modeAnthropicMessagesStream,
}

func (m protocolMode) String() string {
	switch m {
	case modeOpenAIChatNonStream:
		return "openai_chat_non_stream"
	case modeOpenAIChatStream:
		return "openai_chat_stream"
	case modeOpenAIResponsesNonStream:
		return "openai_responses_non_stream"
	case modeOpenAIResponsesStream:
		return "openai_responses_stream"
	case modeAnthropicMessagesNonStream:
		return "anthropic_messages_non_stream"
	case modeAnthropicMessagesStream:
		return "anthropic_messages_stream"
	default:
		return fmt.Sprintf("unknown_mode_%d", m)
	}
}

type atomicUpstream struct {
	server *httptest.Server

	activeMode    atomic.Int32
	fail          atomic.Bool
	compactStatus atomic.Int32
	totalCalls    atomic.Int64
	chatCalls     atomic.Int64
	compactCalls  atomic.Int64
	messagesCalls atomic.Int64
	modeCalls     [modeCount]atomic.Int64

	streamGateMu       sync.Mutex
	nextChatStreamGate *chatStreamGate

	authorizationMu       sync.RWMutex
	authorizationRequired bool
	authorizationHash     [sha256.Size]byte
	authorizationMatched  atomic.Int64
	authorizationRejected atomic.Int64
}

type chatStreamGate struct {
	firstEventWritten chan struct{}
	release           chan struct{}
	releaseOnce       sync.Once
	failTail          atomic.Bool
}

func newChatStreamGate() *chatStreamGate {
	return &chatStreamGate{
		firstEventWritten: make(chan struct{}),
		release:           make(chan struct{}),
	}
}

func (g *chatStreamGate) Release() {
	if g != nil {
		g.releaseOnce.Do(func() { close(g.release) })
	}
}

func (g *chatStreamGate) FailTail() {
	if g != nil {
		g.failTail.Store(true)
	}
}

func newAtomicUpstream(t *testing.T) *atomicUpstream {
	t.Helper()

	u := &atomicUpstream{}
	u.server = httptest.NewServer(http.HandlerFunc(u.serveHTTP))
	t.Cleanup(u.server.Close)
	return u
}

func (u *atomicUpstream) URL() string {
	return u.server.URL
}

func (u *atomicUpstream) setMode(mode protocolMode) {
	u.activeMode.Store(int32(mode))
}

func (u *atomicUpstream) setFailure(enabled bool) {
	u.fail.Store(enabled)
}

func (u *atomicUpstream) setCompactStatus(status int) {
	u.compactStatus.Store(int32(status))
}

// requireAuthorization stores only a one-way digest. Test observations expose
// match/reject counts and never retain or print the supplied credential.
func (u *atomicUpstream) requireAuthorization(value string) {
	u.authorizationMu.Lock()
	u.authorizationRequired = true
	u.authorizationHash = sha256.Sum256([]byte(value))
	u.authorizationMu.Unlock()
}

func (u *atomicUpstream) authorizationSnapshot() (matched, rejected int64) {
	return u.authorizationMatched.Load(), u.authorizationRejected.Load()
}

func (u *atomicUpstream) authorizationAllowed(value string) bool {
	u.authorizationMu.RLock()
	required := u.authorizationRequired
	want := u.authorizationHash
	u.authorizationMu.RUnlock()
	if !required {
		return true
	}
	got := sha256.Sum256([]byte(value))
	if subtle.ConstantTimeCompare(got[:], want[:]) == 1 {
		u.authorizationMatched.Add(1)
		return true
	}
	u.authorizationRejected.Add(1)
	return false
}

func (u *atomicUpstream) blockNextChatStream() *chatStreamGate {
	gate := newChatStreamGate()
	u.streamGateMu.Lock()
	defer u.streamGateMu.Unlock()
	if u.nextChatStreamGate != nil {
		panic("chat stream gate is already armed")
	}
	u.nextChatStreamGate = gate
	return gate
}

func (u *atomicUpstream) takeChatStreamGate() *chatStreamGate {
	u.streamGateMu.Lock()
	defer u.streamGateMu.Unlock()
	gate := u.nextChatStreamGate
	u.nextChatStreamGate = nil
	return gate
}

func (u *atomicUpstream) snapshot() upstreamCounts {
	result := upstreamCounts{
		total:    u.totalCalls.Load(),
		chat:     u.chatCalls.Load(),
		compact:  u.compactCalls.Load(),
		messages: u.messagesCalls.Load(),
	}
	for mode := protocolMode(0); mode < modeCount; mode++ {
		result.byMode[mode] = u.modeCalls[mode].Load()
	}
	return result
}

func (u *atomicUpstream) serveHTTP(w http.ResponseWriter, r *http.Request) {
	mode := protocolMode(u.activeMode.Load())
	u.totalCalls.Add(1)
	if mode >= 0 && mode < modeCount {
		u.modeCalls[mode].Add(1)
	}
	if !u.authorizationAllowed(r.Header.Get("Authorization")) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"invalid credential"}}`)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		return
	}
	if u.fail.Load() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"type":"server_error","code":"server_error","message":"injected upstream failure"}}`)
		return
	}

	var payload struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	switch r.URL.Path {
	case "/v1/chat/completions":
		u.chatCalls.Add(1)
		if payload.Stream {
			writeChatStream(w, u.takeChatStreamGate())
			return
		}
		writeChatResponse(w)
	case "/v1/responses/compact":
		u.compactCalls.Add(1)
		status := int(u.compactStatus.Load())
		if status == 0 {
			status = http.StatusNotFound
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "p4-fault-compact-unsupported")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"compact unsupported"}}`)
	case "/v1/messages":
		u.messagesCalls.Add(1)
		if payload.Stream {
			writeMessagesStream(w)
			return
		}
		writeMessagesResponse(w)
	default:
		http.Error(w, "unexpected path", http.StatusNotFound)
	}
}

type upstreamCounts struct {
	total    int64
	chat     int64
	compact  int64
	messages int64
	byMode   [modeCount]int64
}

func writeChatResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", "p4-fault-chat")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      "chatcmpl-p4-fault",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "p4-fault-model",
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": "ok",
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     2,
			"completion_tokens": 1,
			"total_tokens":      3,
		},
	})
}

func writeChatStream(w http.ResponseWriter, gate *chatStreamGate) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Request-ID", "p4-fault-chat-stream")
	flusher, _ := w.(http.Flusher)
	writeSSEData(w, flusher, map[string]any{
		"id": "chatcmpl-p4-fault-stream", "object": "chat.completion.chunk",
		"created": time.Now().Unix(), "model": "p4-fault-model",
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": nil}},
	})
	if gate != nil {
		close(gate.firstEventWritten)
		<-gate.release
		if gate.failTail.Load() {
			panic(http.ErrAbortHandler)
		}
	}
	writeSSEData(w, flusher, map[string]any{
		"id": "chatcmpl-p4-fault-stream", "object": "chat.completion.chunk",
		"created": time.Now().Unix(), "model": "p4-fault-model",
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
	})
	writeSSEData(w, flusher, map[string]any{
		"id": "chatcmpl-p4-fault-stream", "object": "chat.completion.chunk",
		"created": time.Now().Unix(), "model": "p4-fault-model", "choices": []any{},
		"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3},
	})
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func writeSSEData(w http.ResponseWriter, flusher http.Flusher, payload any) {
	raw, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	if flusher != nil {
		flusher.Flush()
	}
}

func writeMessagesResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("request-id", "p4-fault-messages")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "msg_p4_fault", "type": "message", "role": "assistant",
		"model": "p4-fault-model", "stop_reason": "end_turn", "stop_sequence": nil,
		"content": []map[string]any{{"type": "text", "text": "ok"}},
		"usage":   map[string]any{"input_tokens": 2, "output_tokens": 1},
	})
}

func writeMessagesStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("request-id", "p4-fault-messages-stream")
	flusher, _ := w.(http.Flusher)
	writeNamedEvent(w, flusher, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_p4_fault_stream", "type": "message", "role": "assistant",
			"model": "p4-fault-model", "content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 2, "output_tokens": 0},
		},
	})
	writeNamedEvent(w, flusher, "content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	writeNamedEvent(w, flusher, "content_block_delta", map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "text_delta", "text": "ok"},
	})
	writeNamedEvent(w, flusher, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	writeNamedEvent(w, flusher, "message_delta", map[string]any{
		"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": 1},
	})
	writeNamedEvent(w, flusher, "message_stop", map[string]any{"type": "message_stop"})
}

func writeNamedEvent(w http.ResponseWriter, flusher http.Flusher, name string, payload any) {
	raw, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, raw)
	if flusher != nil {
		flusher.Flush()
	}
}

type isolatedInfra struct {
	postgresContainer string
	postgresVolume    string
	redisContainer    string
	redisVolume       string
	postgresPort      int
	redisPort         int
	databaseURL       string
	redisAddr         string
}

func startIsolatedInfra(t *testing.T, suffix string) *isolatedInfra {
	t.Helper()
	requireExecutable(t, "docker")
	runCommand(t, 20*time.Second, "docker", "info", "--format", "{{.ServerVersion}}")

	infra := &isolatedInfra{
		postgresContainer: "unio-p4-fault-pg-" + suffix,
		postgresVolume:    "unio-p4-fault-pg-" + suffix,
		redisContainer:    "unio-p4-fault-redis-" + suffix,
		redisVolume:       "unio-p4-fault-redis-" + suffix,
		postgresPort:      reservePort(t),
	}
	for infra.redisPort == 0 || infra.redisPort == infra.postgresPort {
		infra.redisPort = reservePort(t)
	}
	infra.databaseURL = fmt.Sprintf(
		"postgres://p4_fault:p4_fault_password@127.0.0.1:%d/p4_fault?sslmode=disable",
		infra.postgresPort,
	)
	infra.redisAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(infra.redisPort))

	runCommand(t, 20*time.Second, "docker", "volume", "create", infra.postgresVolume)
	runCommand(t, 20*time.Second, "docker", "volume", "create", infra.redisVolume)
	t.Cleanup(func() {
		bestEffortCommand("docker", "rm", "-f", infra.postgresContainer)
		bestEffortCommand("docker", "rm", "-f", infra.redisContainer)
		bestEffortCommand("docker", "volume", "rm", "-f", infra.postgresVolume)
		bestEffortCommand("docker", "volume", "rm", "-f", infra.redisVolume)
	})

	runCommand(t, 2*time.Minute, "docker", "run", "-d",
		"--name", infra.postgresContainer,
		"-e", "POSTGRES_DB=p4_fault",
		"-e", "POSTGRES_USER=p4_fault",
		"-e", "POSTGRES_PASSWORD=p4_fault_password",
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", infra.postgresPort),
		"-v", infra.postgresVolume+":/var/lib/postgresql/data",
		postgresImage,
	)
	runCommand(t, 2*time.Minute, "docker", "run", "-d",
		"--name", infra.redisContainer,
		"-p", fmt.Sprintf("127.0.0.1:%d:6379", infra.redisPort),
		"-v", infra.redisVolume+":/data",
		redisImage,
		"redis-server", "--appendonly", "yes", "--appendfsync", "always",
	)

	waitForPostgres(t, infra.databaseURL, 30*time.Second)
	waitForRedis(t, infra.redisAddr, 30*time.Second)
	return infra
}

func (i *isolatedInfra) stopRedis(t *testing.T) {
	t.Helper()
	runCommand(t, 20*time.Second, "docker", "stop", "--time", "2", i.redisContainer)
}

func (i *isolatedInfra) startRedis(t *testing.T) {
	t.Helper()
	runCommand(t, 20*time.Second, "docker", "start", i.redisContainer)
}

func waitForPostgres(t *testing.T, databaseURL string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		pool, err := pgxpool.New(ctx, databaseURL)
		if err == nil {
			err = pool.Ping(ctx)
			pool.Close()
		}
		cancel()
		if err == nil {
			return
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("isolated postgres did not become ready: %v", lastErr)
}

func waitForRedis(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	client := redislib.NewClient(&redislib.Options{Addr: addr, DialTimeout: 250 * time.Millisecond})
	defer client.Close()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		lastErr = client.Ping(ctx).Err()
		cancel()
		if lastErr == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("isolated redis did not become ready: %v", lastErr)
}

type seedFacts struct {
	apiKey             string
	modelID            string
	userID             int64
	routeID            int64
	endpointID         int64
	openAIChannelID    int64
	anthropicChannelID int64
}

type faultHarnessOptions struct {
	openAIAdapterKey string
}

func migrateAndSeed(t *testing.T, root, databaseURL, upstreamURL string, options faultHarnessOptions) seedFacts {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open isolated postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	migrations, err := filepath.Glob(filepath.Join(root, "migrations", "*.up.sql"))
	if err != nil || len(migrations) == 0 {
		t.Fatalf("discover migrations: count=%d err=%v", len(migrations), err)
	}
	sort.Strings(migrations)
	for _, path := range migrations {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read migration %s: %v", filepath.Base(path), readErr)
		}
		if _, execErr := pool.Exec(ctx, string(raw)); execErr != nil {
			t.Fatalf("apply migration %s: %v", filepath.Base(path), execErr)
		}
	}

	queries := sqlc.New(pool)
	generatedKey, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate fixture api key: %v", err)
	}
	user, err := queries.CreateUser(ctx, sqlc.CreateUserParams{
		Email: "p4-fault-e2e@example.test", PasswordHash: "not-a-real-password-hash", DisplayName: "P4 Fault E2E",
	})
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	route, err := queries.CreateRoute(ctx, sqlc.CreateRouteParams{
		Name: "p4-fault-route", Mode: "balanced", Status: "enabled",
		PriceRatio: pgtype.Numeric{Int: big.NewInt(1), Exp: 0, Valid: true},
	})
	if err != nil {
		t.Fatalf("seed route: %v", err)
	}
	if _, err := queries.CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{
		UserID: user.ID, Name: "p4 fault e2e key", KeyPrefix: generatedKey.Prefix,
		KeyHash: generatedKey.Hash, ExpiresAt: pgtype.Timestamptz{Valid: false}, RouteID: route.ID,
	}); err != nil {
		t.Fatalf("seed api key: %v", err)
	}

	var providerID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO providers (slug, name, status)
		VALUES ('p4-fault-provider', 'P4 Fault Provider', 'enabled')
		RETURNING id
	`).Scan(&providerID); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	var endpointID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO provider_endpoints (provider_id, name, base_url, status)
		VALUES ($1, 'P4 Fault Endpoint', $2, 'enabled')
		RETURNING id
	`, providerID, upstreamURL).Scan(&endpointID); err != nil {
		t.Fatalf("seed provider endpoint: %v", err)
	}

	const modelID = "p4-fault-model"
	var modelDBID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO models (model_id, display_name, owned_by, status)
		VALUES ($1, 'P4 Fault Model', 'p4-fault', 'enabled')
		RETURNING id
	`, modelID).Scan(&modelDBID); err != nil {
		t.Fatalf("seed model: %v", err)
	}

	openAIAdapterKey := options.openAIAdapterKey
	if openAIAdapterKey == "" {
		openAIAdapterKey = "deepseek"
	}
	seedChannel := func(protocol, adapterKey string, priority int32) int64 {
		var channelID int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO channels (
				provider_id, provider_endpoint_id, name, protocol, adapter_key,
				credential, status, priority, timeout_ms
			)
			VALUES ($1, $2, $3, $4, $5, 'p4-fault-upstream-key', 'enabled', $6, 5000)
			RETURNING id
		`, providerID, endpointID, "P4 Fault "+protocol, protocol, adapterKey, priority).Scan(&channelID); err != nil {
			t.Fatalf("seed %s channel: %v", protocol, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO channel_models (channel_id, model_id, upstream_model, status)
			VALUES ($1, $2, $3, 'enabled')
		`, channelID, modelDBID, modelID); err != nil {
			t.Fatalf("seed %s channel model: %v", protocol, err)
		}
		if err := queries.AddRouteChannel(ctx, sqlc.AddRouteChannelParams{RouteID: route.ID, ChannelID: channelID}); err != nil {
			t.Fatalf("bind %s channel to route: %v", protocol, err)
		}
		if _, err := queries.CreateChannelPrice(ctx, sqlc.CreateChannelPriceParams{
			ChannelID: channelID, ModelID: modelDBID, Currency: "USD", PricingUnit: billing.PricingUnitPer1MTokens,
			UncachedInputCost: numericMinor(1_0000000000), OutputCost: numericMinor(4_0000000000),
			CacheReadInputCost: numericMinor(0_2500000000), ReasoningOutputCost: numericMinor(6_0000000000),
			Status: "enabled", EffectiveFrom: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
			EffectiveTo: pgtype.Timestamptz{Valid: false},
		}); err != nil {
			t.Fatalf("seed %s channel price: %v", protocol, err)
		}
		return channelID
	}
	openAIChannelID := seedChannel("openai", openAIAdapterKey, 10)
	anthropicChannelID := seedChannel("anthropic", "deepseek", 20)

	if _, err := queries.CreateModelPrice(ctx, sqlc.CreateModelPriceParams{
		ModelID: modelDBID, Currency: "USD", PricingUnit: billing.PricingUnitPer1MTokens,
		UncachedInputPrice: numericMinor(2_0000000000), OutputPrice: numericMinor(8_0000000000),
		CacheReadInputPrice: numericMinor(0_5000000000), ReasoningOutputPrice: numericMinor(12_0000000000),
		Status: "enabled", EffectiveFrom: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
		EffectiveTo: pgtype.Timestamptz{Valid: false},
	}); err != nil {
		t.Fatalf("seed model price: %v", err)
	}
	if err := queries.EnsureUserBalance(ctx, sqlc.EnsureUserBalanceParams{UserID: user.ID, Currency: "USD"}); err != nil {
		t.Fatalf("seed user balance row: %v", err)
	}
	if _, err := queries.AddUserBalance(ctx, sqlc.AddUserBalanceParams{
		Amount: numericMinor(10 * 1_0000000000), UserID: user.ID, Currency: "USD",
	}); err != nil {
		t.Fatalf("seed user balance: %v", err)
	}

	return seedFacts{
		apiKey: generatedKey.Plaintext, modelID: modelID, userID: user.ID, routeID: route.ID, endpointID: endpointID,
		openAIChannelID: openAIChannelID, anthropicChannelID: anthropicChannelID,
	}
}

func numericMinor(units int64) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(units), Exp: -10, Valid: true}
}

type gatewayProcess struct {
	name    string
	baseURL string
	cmd     *exec.Cmd
	done    chan error
	logPath string
	logFile *os.File
	stop    sync.Once
}

func buildGatewayBinary(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway-server")
	cmd := exec.Command("go", "build", "-o", path, "./cmd/gateway-server")
	cmd.Dir = root
	cmd.Env = withoutEnvironment(os.Environ(), "LOG_FORMAT")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build gateway-server: %v\n%s", err, output)
	}
	return path
}

func startGateway(
	t *testing.T,
	root, binary, name, databaseURL, redisAddr, namespace string,
) *gatewayProcess {
	t.Helper()
	port := reservePort(t)
	logPath := filepath.Join(t.TempDir(), name+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create %s log: %v", name, err)
	}

	cmd := exec.Command(binary)
	cmd.Dir = root
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = minimalGatewayEnvironment(databaseURL, redisAddr, namespace, name, port)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start %s: %v", name, err)
	}
	gateway := &gatewayProcess{
		name: name, baseURL: fmt.Sprintf("http://127.0.0.1:%d", port), cmd: cmd,
		done: make(chan error, 1), logPath: logPath, logFile: logFile,
	}
	go func() {
		gateway.done <- cmd.Wait()
		close(gateway.done)
	}()
	t.Cleanup(gateway.shutdown)
	return gateway
}

func minimalGatewayEnvironment(databaseURL, redisAddr, namespace, instance string, port int) []string {
	env := make([]string, 0, 24)
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "SSL_CERT_FILE", "SSL_CERT_DIR", "TZ"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return append(env,
		"UNIO_SKIP_DOTENV=true",
		"DATABASE_URL="+databaseURL,
		"POSTGRES_MAX_CONNS=4",
		"POSTGRES_MIN_CONNS=1",
		"REDIS_ADDR="+redisAddr,
		"REDIS_DB=0",
		"REDIS_KEY_NAMESPACE="+namespace,
		"REDIS_DIAL_TIMEOUT=250ms",
		"REDIS_READ_TIMEOUT=250ms",
		"REDIS_WRITE_TIMEOUT=250ms",
		"REDIS_MAX_RETRIES=0",
		"REDIS_POOL_SIZE=8",
		fmt.Sprintf("GATEWAY_HTTP_ADDR=127.0.0.1:%d", port),
		"GATEWAY_INSTANCE_ID="+instance,
		"HTTP_READ_TIMEOUT=5s",
		"HTTP_SHUTDOWN_TIMEOUT=2s",
		"LOG_LEVEL=error",
	)
}

func (g *gatewayProcess) shutdown() {
	g.stop.Do(func() {
		if g.cmd.Process != nil {
			_ = g.cmd.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-g.done:
		case <-time.After(4 * time.Second):
			if g.cmd.Process != nil {
				_ = g.cmd.Process.Kill()
			}
			<-g.done
		}
		_ = g.logFile.Close()
	})
}

func (g *gatewayProcess) killAbruptly(t *testing.T) {
	t.Helper()
	if g.cmd.Process == nil {
		t.Fatalf("%s process is unavailable before abrupt kill", g.name)
	}
	if err := g.cmd.Process.Kill(); err != nil {
		// Do not consume stop: the registered cleanup must retain its independent
		// TERM/KILL/wait fallback when this explicit fault injection fails.
		t.Fatalf("SIGKILL %s: %v\n%s", g.name, err, g.logs())
	}
	select {
	case <-g.done:
		// The process is confirmed gone, so cleanup only needs to close the log.
		g.stop.Do(func() { _ = g.logFile.Close() })
	case <-time.After(4 * time.Second):
		// Leave stop untouched so t.Cleanup can retry termination and wait.
		t.Fatalf("%s did not exit after SIGKILL\n%s", g.name, g.logs())
	}
}

func (g *gatewayProcess) logs() string {
	raw, err := os.ReadFile(g.logPath)
	if err != nil {
		return fmt.Sprintf("<read log failed: %v>", err)
	}
	return string(raw)
}

func (g *gatewayProcess) exited() (bool, error) {
	select {
	case err := <-g.done:
		return true, err
	default:
		return false, nil
	}
}

type faultHarness struct {
	root      string
	namespace string
	infra     *isolatedInfra
	upstream  *atomicUpstream
	redis     *redislib.Client
	seed      seedFacts
	gateways  [2]*gatewayProcess
	client    *http.Client
}

func setupFaultHarness(t *testing.T) *faultHarness {
	return setupFaultHarnessWithOptions(t, faultHarnessOptions{})
}

func setupFaultHarnessWithOptions(t *testing.T, options faultHarnessOptions) *faultHarness {
	t.Helper()
	root := findRepoRoot(t)
	binary := buildGatewayBinary(t, root)
	suffix := randomSuffix(t)
	upstream := newAtomicUpstream(t)
	infra := startIsolatedInfra(t, suffix)
	seed := migrateAndSeed(t, root, infra.databaseURL, upstream.URL(), options)
	namespace := "unio:p4:fault:" + suffix
	redisClient := redislib.NewClient(&redislib.Options{
		Addr: infra.redisAddr, DialTimeout: 250 * time.Millisecond,
		ReadTimeout: 250 * time.Millisecond, WriteTimeout: 250 * time.Millisecond, MaxRetries: 0,
	})
	t.Cleanup(func() { _ = redisClient.Close() })

	h := &faultHarness{
		root: root, namespace: namespace, infra: infra, upstream: upstream, redis: redisClient, seed: seed,
		client: &http.Client{Timeout: 5 * time.Second},
	}
	h.gateways[0] = startGateway(t, root, binary, "p4-fault-gateway-a", infra.databaseURL, infra.redisAddr, namespace)
	h.waitReadiness(t, h.gateways[0], http.StatusOK, 20*time.Second)
	h.gateways[1] = startGateway(t, root, binary, "p4-fault-gateway-b", infra.databaseURL, infra.redisAddr, namespace)
	h.waitReadiness(t, h.gateways[1], http.StatusOK, 20*time.Second)
	return h
}

func (h *faultHarness) waitReadiness(t *testing.T, gateway *gatewayProcess, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastStatus int
	var lastErr error
	for time.Now().Before(deadline) {
		if exited, exitErr := gateway.exited(); exited {
			t.Fatalf("%s exited before readiness=%d: %v\n%s", gateway.name, want, exitErr, gateway.logs())
		}
		status, _, err := h.get(gateway.baseURL + "/readyz")
		lastStatus, lastErr = status, err
		if err == nil && status == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s readiness status=%d err=%v, want=%d\n%s", gateway.name, lastStatus, lastErr, want, gateway.logs())
}

func (h *faultHarness) assertReadiness(t *testing.T, gateway *gatewayProcess, want int) {
	t.Helper()
	status, body, err := h.get(gateway.baseURL + "/readyz")
	if err != nil {
		t.Fatalf("%s readiness request: %v", gateway.name, err)
	}
	if status != want {
		t.Fatalf("%s readiness=%d want=%d body=%s", gateway.name, status, want, body)
	}
}

func (h *faultHarness) get(url string) (int, string, error) {
	resp, err := h.client.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(raw), readErr
}

func (h *faultHarness) request(t *testing.T, gateway *gatewayProcess, mode protocolMode) (int, string) {
	t.Helper()
	h.upstream.setMode(mode)
	path, body := requestForMode(mode, h.seed.modelID)
	req, err := http.NewRequest(http.MethodPost, gateway.baseURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build %s request: %v", mode, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.seed.apiKey)
	req.Header.Set("x-api-key", h.seed.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("%s through %s: %v", mode, gateway.name, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("read %s response through %s: %v", mode, gateway.name, err)
	}
	return resp.StatusCode, string(raw)
}

func requestForMode(mode protocolMode, model string) (string, string) {
	switch mode {
	case modeOpenAIChatNonStream:
		return "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"stream":false}`, model)
	case modeOpenAIChatStream:
		return "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"stream":true,"stream_options":{"include_usage":true}}`, model)
	case modeOpenAIResponsesNonStream:
		return "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"ping","stream":false}`, model)
	case modeOpenAIResponsesStream:
		return "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"ping","stream":true}`, model)
	case modeAnthropicMessagesNonStream:
		return "/v1/messages", fmt.Sprintf(`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":"ping"}],"stream":false}`, model)
	case modeAnthropicMessagesStream:
		return "/v1/messages", fmt.Sprintf(`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":"ping"}],"stream":true}`, model)
	default:
		panic("unsupported protocol mode")
	}
}

func (h *faultHarness) waitRedis(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		lastErr = h.redis.Ping(ctx).Err()
		cancel()
		if lastErr == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("isolated redis did not recover: %v", lastErr)
}

func (h *faultHarness) redisDelete(t *testing.T, key string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.redis.Del(ctx, key).Err(); err != nil {
		t.Fatalf("delete isolated redis key: %v", err)
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repository root")
		}
		dir = parent
	}
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	var raw [6]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		t.Fatalf("generate isolation suffix: %v", err)
	}
	return hex.EncodeToString(raw[:])
}

func reservePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve local port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release local port reservation: %v", err)
	}
	return port
}

func requireExecutable(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Fatalf("%s is required when P4_FAULT_E2E=1: %v", name, err)
	}
}

func runCommand(t *testing.T, timeout time.Duration, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("command %s timed out", name)
	}
	if err != nil {
		t.Fatalf("command %s failed: %v\n%s", name, err, output)
	}
	return strings.TrimSpace(string(output))
}

func bestEffortCommand(name string, args ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, name, args...).Run()
}

func withoutEnvironment(env []string, keys ...string) []string {
	blocked := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		blocked[key] = struct{}{}
	}
	result := make([]string, 0, len(env))
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if _, found := blocked[key]; !found {
			result = append(result, item)
		}
	}
	return result
}
