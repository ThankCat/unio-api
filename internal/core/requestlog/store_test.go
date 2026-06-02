package requestlog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/apikey"
	"github.com/ThankCat/unio-api/internal/core/credential"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testIdentity 保存 requestlog store 测试所需的身份数据。
type testIdentity struct {
	userID    int64
	projectID int64
	apiKeyID  int64
}

// newTestTx 创建带回滚事务的 requestlog store 测试依赖。
func newTestTx(t *testing.T) (context.Context, pgx.Tx, *sqlc.Queries, func()) {
	t.Helper()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		cancel()
		t.Fatalf("create postgres pool: %v", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		cancel()
		t.Fatalf("ping postgres: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		pool.Close()
		cancel()
		t.Fatalf("begin transaction: %v", err)
	}

	cleanup := func() {
		_ = tx.Rollback(context.Background())
		pool.Close()
		cancel()
	}

	return ctx, tx, sqlc.New(tx), cleanup
}

// createIdentity 创建 requestlog store 测试所需的 user、project 和 API key。
func createIdentity(t *testing.T, ctx context.Context, queries *sqlc.Queries) testIdentity {
	t.Helper()

	suffix := time.Now().UnixNano()

	user, err := queries.CreateUser(ctx, sqlc.CreateUserParams{
		Email:        fmt.Sprintf("requestlog-%d@example.com", suffix),
		PasswordHash: "test-password-hash",
		DisplayName:  "Requestlog User",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	project, err := queries.CreateProject(ctx, sqlc.CreateProjectParams{
		UserID: user.ID,
		Name:   fmt.Sprintf("requestlog-project-%d", suffix),
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	generatedKey, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}

	key, err := queries.CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{
		ProjectID: project.ID,
		Name:      "requestlog key",
		KeyPrefix: generatedKey.Prefix,
		KeyHash:   generatedKey.Hash,
		ExpiresAt: pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}

	return testIdentity{
		userID:    user.ID,
		projectID: project.ID,
		apiKeyID:  key.ID,
	}
}

// createProviderChannel 创建 requestlog store 测试所需的 provider 和 channel。
func createProviderChannel(t *testing.T, ctx context.Context, tx pgx.Tx) (int64, int64) {
	t.Helper()

	suffix := time.Now().UnixNano()

	var providerID int64
	err := tx.QueryRow(ctx, `
		INSERT INTO providers (slug, name, status)
		VALUES ($1, $2, $3)
		RETURNING id
	`, fmt.Sprintf("requestlog-provider-%d", suffix), "Requestlog Provider", "enabled").Scan(&providerID)
	if err != nil {
		t.Fatalf("insert provider: %v", err)
	}

	credentialEncrypted, err := credential.EncryptFixedTestCredential("sk-requestlog-test")
	if err != nil {
		t.Fatalf("encrypt channel credential: %v", err)
	}

	var channelID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO channels (provider_id, name, protocol, adapter_key, base_url, credential_encrypted, status, priority, timeout_ms)
		VALUES ($1, $2, 'openai', 'openai', $3, $4, $5, $6, $7)
		RETURNING id
	`, providerID, fmt.Sprintf("requestlog-channel-%d", suffix), "https://api.example.test/v1", credentialEncrypted, "enabled", 10, nil).Scan(&channelID)
	if err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	return providerID, channelID
}

func TestStoreRequestLifecycleMapsNullableFields(t *testing.T) {
	ctx, tx, queries, cleanup := newTestTx(t)
	defer cleanup()

	identity := createIdentity(t, ctx, queries)
	providerID, channelID := createProviderChannel(t, ctx, tx)
	store := NewStore(queries)
	startedAt := time.Now()

	record, err := store.CreateRequest(ctx, CreateRequestParams{
		RequestID:        fmt.Sprintf("requestlog-request-%d", startedAt.UnixNano()),
		UserID:           identity.userID,
		ProjectID:        identity.projectID,
		APIKeyID:         identity.apiKeyID,
		RequestedModelID: "deepseek-v4-pro",
		IngressProtocol:  ProtocolOpenAI,
		Operation:        OperationChatCompletions,
		Stream:           false,
		StartedAt:        startedAt,
	})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	if record.ID == 0 {
		t.Fatal("expected request record id")
	}
	if record.Status != RequestStatusPending {
		t.Fatalf("expected pending status, got %q", record.Status)
	}
	if record.ResponseModelID != nil || record.FinalProviderID != nil || record.InternalErrorDetail != nil || record.CompletedAt != nil {
		t.Fatalf("expected nullable fields to be nil on create, got response=%v provider=%v internal=%v completed=%v", record.ResponseModelID, record.FinalProviderID, record.InternalErrorDetail, record.CompletedAt)
	}

	running, err := store.MarkRequestRunning(ctx, record.ID)
	if err != nil {
		t.Fatalf("mark request running: %v", err)
	}
	if running.Status != RequestStatusRunning {
		t.Fatalf("expected running status, got %q", running.Status)
	}

	completedAt := time.Now()
	succeeded, err := store.MarkRequestSucceeded(ctx, MarkRequestSucceededParams{
		ID:               record.ID,
		ResponseModelID:  "deepseek-v4-pro",
		ResponseProtocol: ProtocolOpenAI,
		ResponseID:       "chatcmpl-requestlog",
		FinalProviderID:  providerID,
		FinalChannelID:   channelID,
		CompletedAt:      completedAt,
	})
	if err != nil {
		t.Fatalf("mark request succeeded: %v", err)
	}

	if succeeded.Status != RequestStatusSucceeded {
		t.Fatalf("expected succeeded status, got %q", succeeded.Status)
	}
	if succeeded.ResponseModelID == nil || *succeeded.ResponseModelID != "deepseek-v4-pro" {
		t.Fatalf("expected response model to be set, got %v", succeeded.ResponseModelID)
	}
	if succeeded.FinalProviderID == nil || *succeeded.FinalProviderID != providerID {
		t.Fatalf("expected final provider id %d, got %v", providerID, succeeded.FinalProviderID)
	}
	if succeeded.FinalChannelID == nil || *succeeded.FinalChannelID != channelID {
		t.Fatalf("expected final channel id %d, got %v", channelID, succeeded.FinalChannelID)
	}
	if succeeded.CompletedAt == nil {
		t.Fatal("expected completed_at to be set")
	}
}

func TestStoreRequestFailedPersistsSafeAndInternalError(t *testing.T) {
	ctx, _, queries, cleanup := newTestTx(t)
	defer cleanup()

	identity := createIdentity(t, ctx, queries)
	store := NewStore(queries)

	record, err := store.CreateRequest(ctx, CreateRequestParams{
		RequestID:        fmt.Sprintf("requestlog-failed-%d", time.Now().UnixNano()),
		UserID:           identity.userID,
		ProjectID:        identity.projectID,
		APIKeyID:         identity.apiKeyID,
		RequestedModelID: "deepseek-v4-pro",
		IngressProtocol:  ProtocolOpenAI,
		Operation:        OperationChatCompletions,
		Stream:           false,
		StartedAt:        time.Now(),
	})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if _, err := store.MarkRequestRunning(ctx, record.ID); err != nil {
		t.Fatalf("mark request running: %v", err)
	}

	failed, err := store.MarkRequestFailed(ctx, MarkRequestFailedParams{
		ID:                  record.ID,
		ErrorCode:           "adapter_error",
		ErrorMessage:        "Upstream provider request failed.",
		InternalErrorDetail: "upstream returned 502 with request id req_123",
		CompletedAt:         time.Now(),
	})
	if err != nil {
		t.Fatalf("mark request failed: %v", err)
	}
	if failed.ErrorMessage == nil || *failed.ErrorMessage != "Upstream provider request failed." {
		t.Fatalf("expected safe error message, got %v", failed.ErrorMessage)
	}
	if failed.InternalErrorDetail == nil || *failed.InternalErrorDetail != "upstream returned 502 with request id req_123" {
		t.Fatalf("expected internal error detail, got %v", failed.InternalErrorDetail)
	}
}

func TestStoreAttemptLifecycleMapsNullableFields(t *testing.T) {
	ctx, tx, queries, cleanup := newTestTx(t)
	defer cleanup()

	identity := createIdentity(t, ctx, queries)
	providerID, channelID := createProviderChannel(t, ctx, tx)
	store := NewStore(queries)

	record, err := store.CreateRequest(ctx, CreateRequestParams{
		RequestID:        fmt.Sprintf("requestlog-attempt-%d", time.Now().UnixNano()),
		UserID:           identity.userID,
		ProjectID:        identity.projectID,
		APIKeyID:         identity.apiKeyID,
		RequestedModelID: "deepseek-v4-pro",
		IngressProtocol:  ProtocolOpenAI,
		Operation:        OperationChatCompletions,
		Stream:           true,
		StartedAt:        time.Now(),
	})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	attempt, err := store.CreateAttempt(ctx, CreateAttemptParams{
		RequestRecordID:  record.ID,
		AttemptIndex:     0,
		ProviderID:       providerID,
		ChannelID:        channelID,
		AdapterKey:       "openai",
		UpstreamModel:    "deepseek-v4-pro",
		UpstreamProtocol: ProtocolOpenAI,
		StartedAt:        time.Now(),
	})
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}

	if attempt.Status != AttemptStatusRunning {
		t.Fatalf("expected running status, got %q", attempt.Status)
	}
	if attempt.UpstreamResponseModel != nil || attempt.UpstreamStatusCode != nil || attempt.InternalErrorDetail != nil || attempt.CompletedAt != nil {
		t.Fatalf("expected nullable attempt fields to be nil on create, got model=%v status=%v internal=%v completed=%v", attempt.UpstreamResponseModel, attempt.UpstreamStatusCode, attempt.InternalErrorDetail, attempt.CompletedAt)
	}

	succeeded, err := store.MarkAttemptSucceeded(ctx, MarkAttemptSucceededParams{
		ID:                    attempt.ID,
		UpstreamResponseID:    "chatcmpl-requestlog-attempt",
		UpstreamResponseModel: "deepseek-v4-pro-actual",
		UpstreamFinishReason:  "stop",
		FinishClass:           "stop",
		UpstreamStatusCode:    200,
		UpstreamRequestID:     stringValuePtr("upstream-request-id"),
		UsageMappingVersion:   "openai_chat_usage_v1",
		CompletedAt:           time.Now(),
	})
	if err != nil {
		t.Fatalf("mark attempt succeeded: %v", err)
	}

	if succeeded.Status != AttemptStatusSucceeded {
		t.Fatalf("expected succeeded status, got %q", succeeded.Status)
	}
	if succeeded.UpstreamResponseModel == nil || *succeeded.UpstreamResponseModel != "deepseek-v4-pro-actual" {
		t.Fatalf("expected upstream response model, got %v", succeeded.UpstreamResponseModel)
	}
	if succeeded.UpstreamStatusCode == nil || *succeeded.UpstreamStatusCode != 200 {
		t.Fatalf("expected upstream status 200, got %v", succeeded.UpstreamStatusCode)
	}
	if succeeded.UpstreamRequestID == nil || *succeeded.UpstreamRequestID != "upstream-request-id" {
		t.Fatalf("expected upstream request id, got %v", succeeded.UpstreamRequestID)
	}
	if succeeded.CompletedAt == nil {
		t.Fatal("expected completed_at to be set")
	}

	// succeeded 是终态，不能再转 failed（GAP-7-003 状态机守卫）。
	// 失败字段映射在一个独立的 running attempt 上验证。
	failingAttempt, err := store.CreateAttempt(ctx, CreateAttemptParams{
		RequestRecordID:  record.ID,
		AttemptIndex:     1,
		ProviderID:       providerID,
		ChannelID:        channelID,
		AdapterKey:       "openai",
		UpstreamModel:    "deepseek-v4-pro",
		UpstreamProtocol: ProtocolOpenAI,
		StartedAt:        time.Now(),
	})
	if err != nil {
		t.Fatalf("create failing attempt: %v", err)
	}

	failed, err := store.MarkAttemptFailed(ctx, MarkAttemptFailedParams{
		ID:                  failingAttempt.ID,
		UpstreamStatusCode:  intValuePtr(502),
		UpstreamRequestID:   stringValuePtr("failed-upstream-request-id"),
		ErrorCode:           "upstream_bad_gateway",
		ErrorMessage:        "upstream bad gateway",
		InternalErrorDetail: "provider returned 502 bad gateway",
		CompletedAt:         time.Now(),
	})
	if err != nil {
		t.Fatalf("mark attempt failed: %v", err)
	}
	if failed.Status != AttemptStatusFailed {
		t.Fatalf("expected failed status, got %q", failed.Status)
	}
	if failed.ErrorCode == nil || *failed.ErrorCode != "upstream_bad_gateway" {
		t.Fatalf("expected error code, got %v", failed.ErrorCode)
	}
	if failed.InternalErrorDetail == nil || *failed.InternalErrorDetail != "provider returned 502 bad gateway" {
		t.Fatalf("expected internal error detail, got %v", failed.InternalErrorDetail)
	}
}

// stringValuePtr 返回字符串指针，用于构造可选测试字段。
func stringValuePtr(value string) *string {
	return &value
}

// intValuePtr 返回整数指针，用于构造可选测试字段。
func intValuePtr(value int) *int {
	return &value
}
