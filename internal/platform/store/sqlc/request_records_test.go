package sqlc_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/apikey"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// requestRecordIdentity 保存请求记录测试所需的用户、项目和 API Key。
type requestRecordIdentity struct {
	user    sqlc.User
	project sqlc.Project
	apiKey  sqlc.ApiKey
}

// createRequestRecordIdentity 创建请求记录测试所需的身份数据。
func createRequestRecordIdentity(t *testing.T, ctx context.Context, queries *sqlc.Queries) requestRecordIdentity {
	t.Helper()

	suffix := time.Now().UnixNano()

	user, err := queries.CreateUser(ctx, sqlc.CreateUserParams{
		Email:        fmt.Sprintf("request-record-%d@example.com", suffix),
		PasswordHash: "test-password-hash",
		DisplayName:  "Request Record User",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	project, err := queries.CreateProject(ctx, sqlc.CreateProjectParams{
		UserID: user.ID,
		Name:   fmt.Sprintf("request-record-project-%d", suffix),
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	generatedKey, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}

	apiKey, err := queries.CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{
		ProjectID: project.ID,
		Name:      "request record key",
		KeyPrefix: generatedKey.Prefix,
		KeyHash:   generatedKey.Hash,
		ExpiresAt: pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}

	return requestRecordIdentity{
		user:    user,
		project: project,
		apiKey:  apiKey,
	}
}

// createRequestRecordForTest 创建一条 pending 请求记录。
func createRequestRecordForTest(t *testing.T, ctx context.Context, queries *sqlc.Queries, identity requestRecordIdentity, requestID string) sqlc.RequestRecord {
	t.Helper()

	record, err := queries.CreateRequestRecord(ctx, sqlc.CreateRequestRecordParams{
		RequestID:           requestID,
		UserID:              identity.user.ID,
		ProjectID:           identity.project.ID,
		ApiKeyID:            identity.apiKey.ID,
		RequestedModelID:    "deepseek-v4-pro",
		IngressProtocol:     "openai",
		Operation:           "chat_completions",
		ResponseModelID:     pgtype.Text{Valid: false},
		ResponseProtocol:    pgtype.Text{Valid: false},
		ResponseID:          pgtype.Text{Valid: false},
		Stream:              false,
		Status:              "pending",
		FinalProviderID:     pgtype.Int8{Valid: false},
		FinalChannelID:      pgtype.Int8{Valid: false},
		ErrorCode:           pgtype.Text{Valid: false},
		ErrorMessage:        pgtype.Text{Valid: false},
		InternalErrorDetail: pgtype.Text{Valid: false},
		DeliveryStatus:      "not_started",
		ResponseStartedAt:   pgtype.Timestamptz{Valid: false},
		ResponseCompletedAt: pgtype.Timestamptz{Valid: false},
		StartedAt:           pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CompletedAt:         pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create request record: %v", err)
	}

	return record
}

// isUniqueViolation 判断数据库错误是否是唯一约束冲突。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func TestRequestRecordLifecycle(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("request-record-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("request-record-channel-%d", suffix), "enabled", 10, nil)

	record := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("request-record-%d", suffix))
	if record.Status != "pending" {
		t.Fatalf("expected pending status, got %q", record.Status)
	}
	if record.CompletedAt.Valid {
		t.Fatal("expected pending request completed_at to be null")
	}

	running, err := queries.MarkRequestRunning(ctx, record.ID)
	if err != nil {
		t.Fatalf("mark request running: %v", err)
	}
	if running.Status != "running" {
		t.Fatalf("expected running status, got %q", running.Status)
	}

	responseStartedAt := time.Now()
	completedAt := responseStartedAt.Add(250 * time.Millisecond)
	succeeded, err := queries.MarkRequestSucceeded(ctx, sqlc.MarkRequestSucceededParams{
		ResponseModelID:   pgtype.Text{String: "deepseek-v4-pro", Valid: true},
		ResponseProtocol:  pgtype.Text{String: "openai", Valid: true},
		ResponseID:        pgtype.Text{String: "chatcmpl-request-record", Valid: true},
		FinalProviderID:   pgtype.Int8{Int64: providerID, Valid: true},
		FinalChannelID:    pgtype.Int8{Int64: channelID, Valid: true},
		ResponseStartedAt: pgtype.Timestamptz{Time: responseStartedAt, Valid: true},
		CompletedAt:       pgtype.Timestamptz{Time: completedAt, Valid: true},
		RequestRecordID:   record.ID,
	})
	if err != nil {
		t.Fatalf("mark request succeeded: %v", err)
	}
	if succeeded.Status != "succeeded" {
		t.Fatalf("expected succeeded status, got %q", succeeded.Status)
	}
	if !succeeded.CompletedAt.Valid {
		t.Fatal("expected succeeded request completed_at to be set")
	}
	if !succeeded.ResponseStartedAt.Valid {
		t.Fatal("expected succeeded request response_started_at to be set")
	}
	// response_completed_at 由交付状态机负责（delivery_status='completed' 时），结算不写，应保持 NULL。
	if succeeded.ResponseCompletedAt.Valid {
		t.Fatal("expected succeeded request response_completed_at to stay null at settlement")
	}
	if succeeded.FinalProviderID.Int64 != providerID || succeeded.FinalChannelID.Int64 != channelID {
		t.Fatalf("expected final provider/channel %d/%d, got %d/%d", providerID, channelID, succeeded.FinalProviderID.Int64, succeeded.FinalChannelID.Int64)
	}
}

func TestRequestRecordFailedStoresSafeAndInternalError(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	record := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("request-failed-detail-%d", time.Now().UnixNano()))
	if _, err := queries.MarkRequestRunning(ctx, record.ID); err != nil {
		t.Fatalf("mark request running: %v", err)
	}

	failed, err := queries.MarkRequestFailed(ctx, sqlc.MarkRequestFailedParams{
		ErrorCode:           pgtype.Text{String: "adapter_error", Valid: true},
		ErrorMessage:        pgtype.Text{String: "Upstream provider request failed.", Valid: true},
		InternalErrorDetail: pgtype.Text{String: "dial tcp 127.0.0.1: upstream refused", Valid: true},
		CompletedAt:         pgtype.Timestamptz{Time: time.Now(), Valid: true},
		RequestRecordID:     record.ID,
	})
	if err != nil {
		t.Fatalf("mark request failed: %v", err)
	}
	if failed.ErrorMessage.String != "Upstream provider request failed." {
		t.Fatalf("expected safe error message, got %q", failed.ErrorMessage.String)
	}
	if failed.InternalErrorDetail.String != "dial tcp 127.0.0.1: upstream refused" {
		t.Fatalf("expected internal error detail, got %q", failed.InternalErrorDetail.String)
	}
}

func TestRequestRecordStateMachineKeepsTerminalFacts(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("request-state-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("request-state-channel-%d", suffix), "enabled", 10, nil)
	otherProviderID := insertProvider(t, ctx, tx, fmt.Sprintf("request-state-provider-other-%d", suffix), "enabled")
	otherChannelID := insertChannel(t, ctx, tx, otherProviderID, fmt.Sprintf("request-state-channel-other-%d", suffix), "enabled", 20, nil)

	record := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("request-state-%d", suffix))
	if _, err := queries.MarkRequestRunning(ctx, record.ID); err != nil {
		t.Fatalf("mark request running: %v", err)
	}

	firstResponseStartedAt := time.Now()
	firstSucceeded, err := queries.MarkRequestSucceeded(ctx, sqlc.MarkRequestSucceededParams{
		ResponseModelID:   pgtype.Text{String: "deepseek-v4-pro", Valid: true},
		ResponseProtocol:  pgtype.Text{String: "openai", Valid: true},
		ResponseID:        pgtype.Text{String: "chatcmpl-request-state", Valid: true},
		FinalProviderID:   pgtype.Int8{Int64: providerID, Valid: true},
		FinalChannelID:    pgtype.Int8{Int64: channelID, Valid: true},
		ResponseStartedAt: pgtype.Timestamptz{Time: firstResponseStartedAt, Valid: true},
		CompletedAt:       pgtype.Timestamptz{Time: firstResponseStartedAt.Add(100 * time.Millisecond), Valid: true},
		RequestRecordID:   record.ID,
	})
	if err != nil {
		t.Fatalf("mark request succeeded: %v", err)
	}

	repeatedSucceeded, err := queries.MarkRequestSucceeded(ctx, sqlc.MarkRequestSucceededParams{
		ResponseModelID:   pgtype.Text{String: "should-not-overwrite", Valid: true},
		ResponseProtocol:  pgtype.Text{String: "anthropic", Valid: true},
		ResponseID:        pgtype.Text{String: "should-not-overwrite", Valid: true},
		FinalProviderID:   pgtype.Int8{Int64: otherProviderID, Valid: true},
		FinalChannelID:    pgtype.Int8{Int64: otherChannelID, Valid: true},
		ResponseStartedAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		CompletedAt:       pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		RequestRecordID:   record.ID,
	})
	if err != nil {
		t.Fatalf("repeat mark request succeeded: %v", err)
	}
	if repeatedSucceeded.ResponseModelID.String != firstSucceeded.ResponseModelID.String {
		t.Fatalf("expected repeated succeeded to keep response model %q, got %q", firstSucceeded.ResponseModelID.String, repeatedSucceeded.ResponseModelID.String)
	}
	if repeatedSucceeded.FinalProviderID.Int64 != providerID || repeatedSucceeded.FinalChannelID.Int64 != channelID {
		t.Fatalf("expected repeated succeeded to keep provider/channel %d/%d, got %d/%d", providerID, channelID, repeatedSucceeded.FinalProviderID.Int64, repeatedSucceeded.FinalChannelID.Int64)
	}
	if !repeatedSucceeded.ResponseStartedAt.Valid || !repeatedSucceeded.ResponseStartedAt.Time.Equal(firstSucceeded.ResponseStartedAt.Time) {
		t.Fatalf("expected repeated succeeded to keep response_started_at %v, got %v", firstSucceeded.ResponseStartedAt.Time, repeatedSucceeded.ResponseStartedAt.Time)
	}

	_, err = queries.MarkRequestFailed(ctx, sqlc.MarkRequestFailedParams{
		ErrorCode:       pgtype.Text{String: "should_not_overwrite", Valid: true},
		ErrorMessage:    pgtype.Text{String: "should not overwrite", Valid: true},
		CompletedAt:     pgtype.Timestamptz{Time: time.Now(), Valid: true},
		RequestRecordID: record.ID,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected succeeded request cannot become failed, got %v", err)
	}

	_, err = queries.MarkRequestCanceled(ctx, sqlc.MarkRequestCanceledParams{
		ErrorCode:       pgtype.Text{String: "should_not_overwrite", Valid: true},
		ErrorMessage:    pgtype.Text{String: "should not overwrite", Valid: true},
		CompletedAt:     pgtype.Timestamptz{Time: time.Now(), Valid: true},
		RequestRecordID: record.ID,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected succeeded request cannot become canceled, got %v", err)
	}

	if _, err = queries.MarkRequestRunning(ctx, record.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected succeeded request cannot become running, got %v", err)
	}
}

func TestRequestRecordResponseStartedCanBeRecordedBeforeTerminal(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("request-start-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("request-start-channel-%d", suffix), "enabled", 10, nil)

	record := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("request-start-%d", suffix))
	if _, err := queries.MarkRequestRunning(ctx, record.ID); err != nil {
		t.Fatalf("mark request running: %v", err)
	}

	firstStartedAt := time.Now()
	started, err := queries.MarkRequestResponseStarted(ctx, sqlc.MarkRequestResponseStartedParams{
		RequestRecordID:   record.ID,
		ResponseStartedAt: pgtype.Timestamptz{Time: firstStartedAt, Valid: true},
	})
	if err != nil {
		t.Fatalf("mark request response started: %v", err)
	}
	if !started.ResponseStartedAt.Valid {
		t.Fatal("expected response_started_at to be set")
	}

	repeatedStarted, err := queries.MarkRequestResponseStarted(ctx, sqlc.MarkRequestResponseStartedParams{
		RequestRecordID:   record.ID,
		ResponseStartedAt: pgtype.Timestamptz{Time: firstStartedAt.Add(time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("repeat mark request response started: %v", err)
	}
	if !repeatedStarted.ResponseStartedAt.Time.Equal(started.ResponseStartedAt.Time) {
		t.Fatalf("expected repeated start to keep %v, got %v", started.ResponseStartedAt.Time, repeatedStarted.ResponseStartedAt.Time)
	}

	completedAt := firstStartedAt.Add(250 * time.Millisecond)
	succeeded, err := queries.MarkRequestSucceeded(ctx, sqlc.MarkRequestSucceededParams{
		ResponseModelID:   pgtype.Text{String: "deepseek-v4-pro", Valid: true},
		ResponseProtocol:  pgtype.Text{String: "openai", Valid: true},
		ResponseID:        pgtype.Text{String: "chatcmpl-request-start", Valid: true},
		FinalProviderID:   pgtype.Int8{Int64: providerID, Valid: true},
		FinalChannelID:    pgtype.Int8{Int64: channelID, Valid: true},
		ResponseStartedAt: pgtype.Timestamptz{Time: firstStartedAt.Add(time.Hour), Valid: true},
		CompletedAt:       pgtype.Timestamptz{Time: completedAt, Valid: true},
		RequestRecordID:   record.ID,
	})
	if err != nil {
		t.Fatalf("mark request succeeded: %v", err)
	}
	if !succeeded.ResponseStartedAt.Time.Equal(started.ResponseStartedAt.Time) {
		t.Fatalf("expected succeeded to keep early response_started_at %v, got %v", started.ResponseStartedAt.Time, succeeded.ResponseStartedAt.Time)
	}
}

func TestRequestRecordRejectsDuplicateRequestID(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	requestID := fmt.Sprintf("duplicate-request-%d", time.Now().UnixNano())

	createRequestRecordForTest(t, ctx, queries, identity, requestID)

	_, err := queries.CreateRequestRecord(ctx, sqlc.CreateRequestRecordParams{
		RequestID:           requestID,
		UserID:              identity.user.ID,
		ProjectID:           identity.project.ID,
		ApiKeyID:            identity.apiKey.ID,
		RequestedModelID:    "deepseek-v4-pro",
		IngressProtocol:     "openai",
		Operation:           "chat_completions",
		ResponseModelID:     pgtype.Text{Valid: false},
		ResponseProtocol:    pgtype.Text{Valid: false},
		ResponseID:          pgtype.Text{Valid: false},
		Stream:              false,
		Status:              "pending",
		FinalProviderID:     pgtype.Int8{Valid: false},
		FinalChannelID:      pgtype.Int8{Valid: false},
		ErrorCode:           pgtype.Text{Valid: false},
		ErrorMessage:        pgtype.Text{Valid: false},
		DeliveryStatus:      "not_started",
		ResponseStartedAt:   pgtype.Timestamptz{Valid: false},
		ResponseCompletedAt: pgtype.Timestamptz{Valid: false},
		StartedAt:           pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CompletedAt:         pgtype.Timestamptz{Valid: false},
	})
	if err == nil {
		t.Fatal("expected duplicate request_id error")
	}
	if !isUniqueViolation(err) {
		t.Fatalf("expected unique violation, got %v", err)
	}
}

func TestRequestAttemptsOrderAndUniqueness(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("request-attempt-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("request-attempt-channel-%d", suffix), "enabled", 10, nil)
	record := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("request-attempt-%d", suffix))

	secondAttempt, err := queries.CreateRequestAttempt(ctx, sqlc.CreateRequestAttemptParams{
		RequestRecordID:       record.ID,
		AttemptIndex:          1,
		ProviderID:            providerID,
		ChannelID:             channelID,
		AdapterKey:            "openai",
		UpstreamModel:         "deepseek-v4-pro",
		UpstreamProtocol:      "openai",
		UpstreamResponseModel: pgtype.Text{Valid: false},
		Status:                "running",
		UpstreamStatusCode:    pgtype.Int4{Valid: false},
		UpstreamRequestID:     pgtype.Text{Valid: false},
		ErrorCode:             pgtype.Text{Valid: false},
		ErrorMessage:          pgtype.Text{Valid: false},
		StartedAt:             pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CompletedAt:           pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create second attempt: %v", err)
	}

	firstAttempt, err := queries.CreateRequestAttempt(ctx, sqlc.CreateRequestAttemptParams{
		RequestRecordID:       record.ID,
		AttemptIndex:          0,
		ProviderID:            providerID,
		ChannelID:             channelID,
		AdapterKey:            "openai",
		UpstreamModel:         "deepseek-v4-pro",
		UpstreamProtocol:      "openai",
		UpstreamResponseModel: pgtype.Text{Valid: false},
		Status:                "running",
		UpstreamStatusCode:    pgtype.Int4{Valid: false},
		UpstreamRequestID:     pgtype.Text{Valid: false},
		ErrorCode:             pgtype.Text{Valid: false},
		ErrorMessage:          pgtype.Text{Valid: false},
		StartedAt:             pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CompletedAt:           pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create first attempt: %v", err)
	}

	completedAt := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	succeeded, err := queries.MarkRequestAttemptSucceeded(ctx, sqlc.MarkRequestAttemptSucceededParams{
		UpstreamResponseID:    pgtype.Text{String: "chatcmpl-request-attempt", Valid: true},
		UpstreamResponseModel: pgtype.Text{String: "deepseek-v4-pro-actual", Valid: true},
		UpstreamFinishReason:  pgtype.Text{String: "stop", Valid: true},
		FinishClass:           pgtype.Text{String: "stop", Valid: true},
		UpstreamStatusCode:    pgtype.Int4{Int32: 200, Valid: true},
		UpstreamRequestID:     pgtype.Text{String: "upstream-request-id", Valid: true},
		UsageMappingVersion:   pgtype.Text{String: "openai_chat_usage_v1", Valid: true},
		CompletedAt:           completedAt,
		AttemptID:             firstAttempt.ID,
	})
	if err != nil {
		t.Fatalf("mark first attempt succeeded: %v", err)
	}
	if succeeded.Status != "succeeded" || !succeeded.CompletedAt.Valid {
		t.Fatalf("expected succeeded attempt with completed_at, got status=%q completed_at=%v", succeeded.Status, succeeded.CompletedAt.Valid)
	}
	if succeeded.UpstreamResponseModel.String != "deepseek-v4-pro-actual" {
		t.Fatalf("expected upstream response model to be saved, got %q", succeeded.UpstreamResponseModel.String)
	}

	failed, err := queries.MarkRequestAttemptFailed(ctx, sqlc.MarkRequestAttemptFailedParams{
		UpstreamStatusCode:  pgtype.Int4{Int32: 502, Valid: true},
		UpstreamRequestID:   pgtype.Text{String: "failed-upstream-request-id", Valid: true},
		ErrorCode:           pgtype.Text{String: "upstream_bad_gateway", Valid: true},
		ErrorMessage:        pgtype.Text{String: "upstream bad gateway", Valid: true},
		InternalErrorDetail: pgtype.Text{String: "provider returned 502 bad gateway", Valid: true},
		CompletedAt:         completedAt,
		AttemptID:           secondAttempt.ID,
	})
	if err != nil {
		t.Fatalf("mark second attempt failed: %v", err)
	}
	if failed.Status != "failed" || failed.UpstreamStatusCode.Int32 != 502 {
		t.Fatalf("expected failed attempt with upstream status 502, got status=%q status_code=%d", failed.Status, failed.UpstreamStatusCode.Int32)
	}
	if failed.InternalErrorDetail.String != "provider returned 502 bad gateway" {
		t.Fatalf("expected internal error detail, got %q", failed.InternalErrorDetail.String)
	}

	attempts, err := queries.ListRequestAttemptsByRequest(ctx, record.ID)
	if err != nil {
		t.Fatalf("list request attempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(attempts))
	}
	if attempts[0].ID != firstAttempt.ID || attempts[1].ID != secondAttempt.ID {
		t.Fatalf("expected attempts ordered by index, got ids %d then %d", attempts[0].ID, attempts[1].ID)
	}

	_, err = queries.CreateRequestAttempt(ctx, sqlc.CreateRequestAttemptParams{
		RequestRecordID:       record.ID,
		AttemptIndex:          0,
		ProviderID:            providerID,
		ChannelID:             channelID,
		AdapterKey:            "openai",
		UpstreamModel:         "deepseek-v4-pro",
		UpstreamProtocol:      "openai",
		UpstreamResponseModel: pgtype.Text{Valid: false},
		Status:                "running",
		UpstreamStatusCode:    pgtype.Int4{Valid: false},
		UpstreamRequestID:     pgtype.Text{Valid: false},
		ErrorCode:             pgtype.Text{Valid: false},
		ErrorMessage:          pgtype.Text{Valid: false},
		StartedAt:             pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CompletedAt:           pgtype.Timestamptz{Valid: false},
	})
	if err == nil {
		t.Fatal("expected duplicate attempt_index error")
	}
	if !isUniqueViolation(err) {
		t.Fatalf("expected unique violation, got %v", err)
	}
}

func TestRequestAttemptStateMachineKeepsTerminalFacts(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("attempt-state-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("attempt-state-channel-%d", suffix), "enabled", 10, nil)
	record := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("attempt-state-%d", suffix))

	attempt, err := queries.CreateRequestAttempt(ctx, sqlc.CreateRequestAttemptParams{
		RequestRecordID:       record.ID,
		AttemptIndex:          0,
		ProviderID:            providerID,
		ChannelID:             channelID,
		AdapterKey:            "openai",
		UpstreamModel:         "deepseek-v4-pro",
		UpstreamProtocol:      "openai",
		UpstreamResponseModel: pgtype.Text{Valid: false},
		Status:                "running",
		UpstreamStatusCode:    pgtype.Int4{Valid: false},
		UpstreamRequestID:     pgtype.Text{Valid: false},
		ErrorCode:             pgtype.Text{Valid: false},
		ErrorMessage:          pgtype.Text{Valid: false},
		StartedAt:             pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CompletedAt:           pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}

	attemptStartedAt := time.Now()
	firstSucceeded, err := queries.MarkRequestAttemptSucceeded(ctx, sqlc.MarkRequestAttemptSucceededParams{
		UpstreamResponseID:    pgtype.Text{String: "chatcmpl-attempt-state", Valid: true},
		UpstreamResponseModel: pgtype.Text{String: "deepseek-v4-pro-actual", Valid: true},
		UpstreamFinishReason:  pgtype.Text{String: "stop", Valid: true},
		FinishClass:           pgtype.Text{String: "stop", Valid: true},
		UpstreamStatusCode:    pgtype.Int4{Int32: 200, Valid: true},
		UpstreamRequestID:     pgtype.Text{String: "upstream-request-id", Valid: true},
		ResponseStartedAt:     pgtype.Timestamptz{Time: attemptStartedAt, Valid: true},
		UsageMappingVersion:   pgtype.Text{String: "openai_chat_usage_v1", Valid: true},
		CompletedAt:           pgtype.Timestamptz{Time: time.Now(), Valid: true},
		AttemptID:             attempt.ID,
	})
	if err != nil {
		t.Fatalf("mark attempt succeeded: %v", err)
	}
	if !firstSucceeded.ResponseStartedAt.Valid {
		t.Fatal("expected first succeeded attempt response_started_at to be set")
	}

	repeatedSucceeded, err := queries.MarkRequestAttemptSucceeded(ctx, sqlc.MarkRequestAttemptSucceededParams{
		UpstreamResponseID:    pgtype.Text{String: "should-not-overwrite", Valid: true},
		UpstreamResponseModel: pgtype.Text{String: "should-not-overwrite", Valid: true},
		UpstreamFinishReason:  pgtype.Text{String: "should-not-overwrite", Valid: true},
		FinishClass:           pgtype.Text{String: "other", Valid: true},
		UpstreamStatusCode:    pgtype.Int4{Int32: 201, Valid: true},
		UpstreamRequestID:     pgtype.Text{String: "should-not-overwrite", Valid: true},
		ResponseStartedAt:     pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		UsageMappingVersion:   pgtype.Text{String: "should-not-overwrite", Valid: true},
		CompletedAt:           pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		AttemptID:             attempt.ID,
	})
	if err != nil {
		t.Fatalf("repeat mark attempt succeeded: %v", err)
	}
	if repeatedSucceeded.UpstreamResponseModel.String != firstSucceeded.UpstreamResponseModel.String {
		t.Fatalf("expected repeated succeeded to keep response model %q, got %q", firstSucceeded.UpstreamResponseModel.String, repeatedSucceeded.UpstreamResponseModel.String)
	}
	if repeatedSucceeded.UpstreamStatusCode.Int32 != 200 {
		t.Fatalf("expected repeated succeeded to keep upstream status 200, got %d", repeatedSucceeded.UpstreamStatusCode.Int32)
	}
	if repeatedSucceeded.UpstreamRequestID.String != "upstream-request-id" {
		t.Fatalf("expected repeated succeeded to keep upstream request id, got %q", repeatedSucceeded.UpstreamRequestID.String)
	}
	if !repeatedSucceeded.ResponseStartedAt.Valid || !repeatedSucceeded.ResponseStartedAt.Time.Equal(firstSucceeded.ResponseStartedAt.Time) {
		t.Fatalf("expected repeated succeeded to keep response_started_at %v, got %v", firstSucceeded.ResponseStartedAt.Time, repeatedSucceeded.ResponseStartedAt.Time)
	}

	_, err = queries.MarkRequestAttemptFailed(ctx, sqlc.MarkRequestAttemptFailedParams{
		UpstreamStatusCode: pgtype.Int4{Int32: 502, Valid: true},
		UpstreamRequestID:  pgtype.Text{String: "should-not-overwrite", Valid: true},
		ErrorCode:          pgtype.Text{String: "should_not_overwrite", Valid: true},
		ErrorMessage:       pgtype.Text{String: "should not overwrite", Valid: true},
		CompletedAt:        pgtype.Timestamptz{Time: time.Now(), Valid: true},
		AttemptID:          attempt.ID,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected succeeded attempt cannot become failed, got %v", err)
	}

	_, err = queries.MarkRequestAttemptCanceled(ctx, sqlc.MarkRequestAttemptCanceledParams{
		ErrorCode:    pgtype.Text{String: "should_not_overwrite", Valid: true},
		ErrorMessage: pgtype.Text{String: "should not overwrite", Valid: true},
		CompletedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
		AttemptID:    attempt.ID,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected succeeded attempt cannot become canceled, got %v", err)
	}
}

func TestRequestAttemptResponseStartedCanBeRecordedBeforeTerminal(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("attempt-start-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("attempt-start-channel-%d", suffix), "enabled", 10, nil)
	record := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("attempt-start-%d", suffix))

	attempt, err := queries.CreateRequestAttempt(ctx, sqlc.CreateRequestAttemptParams{
		RequestRecordID:       record.ID,
		AttemptIndex:          0,
		ProviderID:            providerID,
		ChannelID:             channelID,
		AdapterKey:            "openai",
		UpstreamModel:         "deepseek-v4-pro",
		UpstreamProtocol:      "openai",
		UpstreamResponseModel: pgtype.Text{Valid: false},
		Status:                "running",
		UpstreamStatusCode:    pgtype.Int4{Valid: false},
		UpstreamRequestID:     pgtype.Text{Valid: false},
		ErrorCode:             pgtype.Text{Valid: false},
		ErrorMessage:          pgtype.Text{Valid: false},
		StartedAt:             pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CompletedAt:           pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}

	firstStartedAt := time.Now()
	started, err := queries.MarkRequestAttemptResponseStarted(ctx, sqlc.MarkRequestAttemptResponseStartedParams{
		AttemptID:         attempt.ID,
		ResponseStartedAt: pgtype.Timestamptz{Time: firstStartedAt, Valid: true},
	})
	if err != nil {
		t.Fatalf("mark attempt response started: %v", err)
	}
	if !started.ResponseStartedAt.Valid {
		t.Fatal("expected attempt response_started_at to be set")
	}

	repeatedStarted, err := queries.MarkRequestAttemptResponseStarted(ctx, sqlc.MarkRequestAttemptResponseStartedParams{
		AttemptID:         attempt.ID,
		ResponseStartedAt: pgtype.Timestamptz{Time: firstStartedAt.Add(time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("repeat mark attempt response started: %v", err)
	}
	if !repeatedStarted.ResponseStartedAt.Time.Equal(started.ResponseStartedAt.Time) {
		t.Fatalf("expected repeated start to keep %v, got %v", started.ResponseStartedAt.Time, repeatedStarted.ResponseStartedAt.Time)
	}

	succeeded, err := queries.MarkRequestAttemptSucceeded(ctx, sqlc.MarkRequestAttemptSucceededParams{
		UpstreamResponseID:    pgtype.Text{String: "chatcmpl-attempt-start", Valid: true},
		UpstreamResponseModel: pgtype.Text{String: "deepseek-v4-pro-actual", Valid: true},
		UpstreamFinishReason:  pgtype.Text{String: "stop", Valid: true},
		FinishClass:           pgtype.Text{String: "stop", Valid: true},
		UpstreamStatusCode:    pgtype.Int4{Int32: 200, Valid: true},
		UpstreamRequestID:     pgtype.Text{String: "upstream-request-id", Valid: true},
		ResponseStartedAt:     pgtype.Timestamptz{Time: firstStartedAt.Add(time.Hour), Valid: true},
		UsageMappingVersion:   pgtype.Text{String: "openai_chat_usage_v1", Valid: true},
		CompletedAt:           pgtype.Timestamptz{Time: time.Now(), Valid: true},
		AttemptID:             attempt.ID,
	})
	if err != nil {
		t.Fatalf("mark attempt succeeded: %v", err)
	}
	if !succeeded.ResponseStartedAt.Time.Equal(started.ResponseStartedAt.Time) {
		t.Fatalf("expected succeeded to keep early response_started_at %v, got %v", started.ResponseStartedAt.Time, succeeded.ResponseStartedAt.Time)
	}
}
