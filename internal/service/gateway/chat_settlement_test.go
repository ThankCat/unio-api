package gateway

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/apikey"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/core/ledger"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeChatBillingCalculator 是 chat settlement 测试使用的 billing calculator 替身。
type fakeChatBillingCalculator struct {
	usages     []billing.Usage
	prices     []billing.CustomerPriceSnapshot
	costUsages []billing.Usage
	costs      []billing.ProviderCostSnapshot
	charge     billing.CustomerCharge
	cost       billing.ProviderCost
	err        error
}

// fakeChatLedgerCapturer 是 chat settlement 测试使用的 ledger reservation 替身。
type fakeChatLedgerCapturer struct {
	captureParams []ledger.CaptureParams
	releaseParams []ledger.ReleaseParams
	queries       []*sqlc.Queries
	err           error
}

// CalculateCustomerCharge 记录 billing 入参，并返回测试预设客户扣费结果。
func (c *fakeChatBillingCalculator) CalculateCustomerCharge(usage billing.Usage, price billing.CustomerPriceSnapshot) (billing.CustomerCharge, error) {
	c.usages = append(c.usages, usage)
	c.prices = append(c.prices, price)
	if c.err != nil {
		return billing.CustomerCharge{}, c.err
	}

	return c.charge, nil
}

// CalculateProviderCost 记录 billing 入参，并返回测试预设平台成本结果。
func (c *fakeChatBillingCalculator) CalculateProviderCost(usage billing.Usage, cost billing.ProviderCostSnapshot) (billing.ProviderCost, error) {
	c.costUsages = append(c.costUsages, usage)
	c.costs = append(c.costs, cost)
	if c.err != nil {
		return billing.ProviderCost{}, c.err
	}

	return c.cost, nil
}

// CaptureWithQueries 记录事务内 ledger capture 参数，并返回测试预设错误。
func (c *fakeChatLedgerCapturer) CaptureWithQueries(ctx context.Context, queries *sqlc.Queries, params ledger.CaptureParams) (ledger.Reservation, error) {
	c.queries = append(c.queries, queries)
	c.captureParams = append(c.captureParams, params)
	if c.err != nil {
		return ledger.Reservation{}, c.err
	}

	return ledger.Reservation{ID: derefInt64(params.ReservationID), RequestRecordID: params.RequestRecordID, AuthorizedAmount: params.ActualAmount, CapturedAmount: params.ActualAmount}, nil
}

// ReleaseWithQueries 记录事务内 ledger release 参数，并返回测试预设错误。
func (c *fakeChatLedgerCapturer) ReleaseWithQueries(ctx context.Context, queries *sqlc.Queries, params ledger.ReleaseParams) (ledger.Reservation, error) {
	c.queries = append(c.queries, queries)
	c.releaseParams = append(c.releaseParams, params)
	if c.err != nil {
		return ledger.Reservation{}, c.err
	}

	return ledger.Reservation{ID: derefInt64(params.ReservationID), RequestRecordID: params.RequestRecordID}, nil
}

// derefInt64 返回可选 int64 指针的值，nil 时返回 0。
func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}

	return *value
}

// chatSettlementDBDeps 保存 chat settlement 集成测试依赖。
type chatSettlementDBDeps struct {
	ctx           context.Context
	cancel        context.CancelFunc
	pool          *pgxpool.Pool
	queries       *sqlc.Queries
	userID        int64
	projectID     int64
	apiKeyID      int64
	providerID    int64
	channelID     int64
	modelID       int64
	priceID       int64
	costPriceID   int64
	reservationID int64
	requestRecord sqlc.RequestRecord
	attemptRecord sqlc.RequestAttempt
}

// newChatSettlementDBDeps 创建带真实数据库记录的 chat settlement 测试依赖。
func newChatSettlementDBDeps(t *testing.T) *chatSettlementDBDeps {
	t.Helper()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

	deps := &chatSettlementDBDeps{
		ctx:     ctx,
		cancel:  cancel,
		pool:    pool,
		queries: sqlc.New(pool),
	}
	t.Cleanup(func() {
		deps.cleanup()
	})

	deps.seed(t)

	return deps
}

// cleanup 删除测试提交的数据，避免污染本地开发库。
func (d *chatSettlementDBDeps) cleanup() {
	ctx := context.Background()

	if d.requestRecord.ID != 0 {
		_, _ = d.pool.Exec(ctx, `DELETE FROM ledger_billing_exceptions WHERE request_record_id = $1`, d.requestRecord.ID)
		_, _ = d.pool.Exec(ctx, `DELETE FROM ledger_reservations WHERE request_record_id = $1`, d.requestRecord.ID)
		_, _ = d.pool.Exec(ctx, `DELETE FROM ledger_entries WHERE request_record_id = $1`, d.requestRecord.ID)
		_, _ = d.pool.Exec(ctx, `DELETE FROM cost_snapshots WHERE request_record_id = $1`, d.requestRecord.ID)
		_, _ = d.pool.Exec(ctx, `DELETE FROM price_snapshots WHERE request_record_id = $1`, d.requestRecord.ID)
		_, _ = d.pool.Exec(ctx, `DELETE FROM usage_records WHERE request_record_id = $1`, d.requestRecord.ID)
		_, _ = d.pool.Exec(ctx, `DELETE FROM request_attempts WHERE request_record_id = $1`, d.requestRecord.ID)
		_, _ = d.pool.Exec(ctx, `DELETE FROM request_records WHERE id = $1`, d.requestRecord.ID)
	}
	if d.costPriceID != 0 {
		_, _ = d.pool.Exec(ctx, `DELETE FROM channel_cost_prices WHERE id = $1`, d.costPriceID)
	}
	if d.priceID != 0 {
		_, _ = d.pool.Exec(ctx, `DELETE FROM prices WHERE id = $1`, d.priceID)
	}
	if d.channelID != 0 && d.modelID != 0 {
		_, _ = d.pool.Exec(ctx, `DELETE FROM channel_models WHERE channel_id = $1 AND model_id = $2`, d.channelID, d.modelID)
	}
	if d.channelID != 0 {
		_, _ = d.pool.Exec(ctx, `DELETE FROM channels WHERE id = $1`, d.channelID)
	}
	if d.providerID != 0 {
		_, _ = d.pool.Exec(ctx, `DELETE FROM providers WHERE id = $1`, d.providerID)
	}
	if d.modelID != 0 {
		_, _ = d.pool.Exec(ctx, `DELETE FROM models WHERE id = $1`, d.modelID)
	}
	if d.apiKeyID != 0 {
		_, _ = d.pool.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, d.apiKeyID)
	}
	if d.projectID != 0 {
		_, _ = d.pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, d.projectID)
	}
	if d.userID != 0 {
		_, _ = d.pool.Exec(ctx, `DELETE FROM ledger_billing_exceptions WHERE user_id = $1`, d.userID)
		_, _ = d.pool.Exec(ctx, `DELETE FROM ledger_entries WHERE user_id = $1`, d.userID)
		_, _ = d.pool.Exec(ctx, `DELETE FROM user_balances WHERE user_id = $1`, d.userID)
		_, _ = d.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, d.userID)
	}

	d.pool.Close()
	d.cancel()
}

// seed 插入一次可结算 chat 请求所需的身份、模型、价格、请求和余额数据。
func (d *chatSettlementDBDeps) seed(t *testing.T) {
	t.Helper()

	suffix := time.Now().UnixNano()

	user, err := d.queries.CreateUser(d.ctx, sqlc.CreateUserParams{
		Email:        fmt.Sprintf("chat-settlement-%d@example.com", suffix),
		PasswordHash: "test-password-hash",
		DisplayName:  "Chat Settlement User",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	d.userID = user.ID

	project, err := d.queries.CreateProject(d.ctx, sqlc.CreateProjectParams{
		UserID: user.ID,
		Name:   fmt.Sprintf("chat-settlement-project-%d", suffix),
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	d.projectID = project.ID

	generatedKey, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}
	apiKey, err := d.queries.CreateAPIKey(d.ctx, sqlc.CreateAPIKeyParams{
		ProjectID: project.ID,
		Name:      "chat settlement key",
		KeyPrefix: generatedKey.Prefix,
		KeyHash:   generatedKey.Hash,
		ExpiresAt: pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}
	d.apiKeyID = apiKey.ID

	d.providerID = insertChatSettlementProvider(t, d.ctx, d.pool, suffix)
	d.channelID = insertChatSettlementChannel(t, d.ctx, d.pool, d.providerID, suffix)
	d.modelID = insertChatSettlementModel(t, d.ctx, d.pool, suffix)
	insertChatSettlementChannelModel(t, d.ctx, d.pool, d.channelID, d.modelID)

	price, err := d.queries.CreatePrice(d.ctx, sqlc.CreatePriceParams{
		ModelID:              d.modelID,
		Currency:             "USD",
		PricingUnit:          billing.PricingUnitPer1MTokens,
		InputPrice:           testNumeric(2_0000000000, -10),
		OutputPrice:          testNumeric(8_0000000000, -10),
		CachedInputPrice:     testNumeric(5000000000, -10),
		ReasoningOutputPrice: testNumeric(12_0000000000, -10),
		Status:               "enabled",
		EffectiveFrom:        pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
		EffectiveTo:          pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create price: %v", err)
	}
	d.priceID = price.ID

	costPrice, err := d.queries.CreateChannelCostPrice(d.ctx, sqlc.CreateChannelCostPriceParams{
		ChannelID:           d.channelID,
		ModelID:             d.modelID,
		Currency:            "USD",
		PricingUnit:         billing.PricingUnitPer1MTokens,
		InputCost:           testNumeric(1_0000000000, -10),
		OutputCost:          testNumeric(4_0000000000, -10),
		CachedInputCost:     testNumeric(2500000000, -10),
		ReasoningOutputCost: testNumeric(6_0000000000, -10),
		Status:              "enabled",
		EffectiveFrom:       pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
		EffectiveTo:         pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create channel cost price: %v", err)
	}
	d.costPriceID = costPrice.ID

	requestRecord, err := d.queries.CreateRequestRecord(d.ctx, sqlc.CreateRequestRecordParams{
		RequestID:        fmt.Sprintf("chat-settlement-request-%d", suffix),
		UserID:           user.ID,
		ProjectID:        project.ID,
		ApiKeyID:         apiKey.ID,
		RequestedModelID: "openai/gpt-4.1",
		ResponseModelID:  pgtype.Text{Valid: false},
		Stream:           false,
		Status:           string(requestlog.RequestStatusRunning),
		FinalProviderID:  pgtype.Int8{Valid: false},
		FinalChannelID:   pgtype.Int8{Valid: false},
		ErrorCode:        pgtype.Text{Valid: false},
		ErrorMessage:     pgtype.Text{Valid: false},
		StartedAt:        pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CompletedAt:      pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create request record: %v", err)
	}
	d.requestRecord = requestRecord

	attemptRecord, err := d.queries.CreateRequestAttempt(d.ctx, sqlc.CreateRequestAttemptParams{
		RequestRecordID:       requestRecord.ID,
		AttemptIndex:          0,
		ProviderID:            d.providerID,
		ChannelID:             d.channelID,
		AdapterKey:            "openai",
		UpstreamModel:         "gpt-4.1",
		UpstreamResponseModel: pgtype.Text{Valid: false},
		Status:                string(requestlog.AttemptStatusRunning),
		UpstreamStatusCode:    pgtype.Int4{Valid: false},
		UpstreamRequestID:     pgtype.Text{Valid: false},
		ErrorCode:             pgtype.Text{Valid: false},
		ErrorMessage:          pgtype.Text{Valid: false},
		StartedAt:             pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CompletedAt:           pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create request attempt: %v", err)
	}
	d.attemptRecord = attemptRecord

	if err := d.queries.EnsureUserBalance(d.ctx, sqlc.EnsureUserBalanceParams{
		UserID:   user.ID,
		Currency: "USD",
	}); err != nil {
		t.Fatalf("ensure user balance: %v", err)
	}
	if _, err := d.queries.AddUserBalance(d.ctx, sqlc.AddUserBalanceParams{
		Amount:   testNumeric(10_0000000000, -10),
		UserID:   user.ID,
		Currency: "USD",
	}); err != nil {
		t.Fatalf("add user balance: %v", err)
	}

	reservation, err := ledger.NewService(d.pool, d.queries).PreAuthorize(d.ctx, ledger.PreAuthorizeParams{
		UserID:          user.ID,
		RequestRecordID: requestRecord.ID,
		EstimatedAmount: testNumeric(1_0000000000, -10),
		Currency:        "USD",
		IdempotencyKey:  fmt.Sprintf("chat-settlement-reserve-%d", suffix),
		Reason:          "chat settlement test reservation",
	})
	if err != nil {
		t.Fatalf("create ledger reservation: %v", err)
	}
	d.reservationID = reservation.ID
}

// params 创建 chat settlement 测试参数。
func (d *chatSettlementDBDeps) params() ChatSettlementParams {
	return ChatSettlementParams{
		RequestRecord: requestlog.RequestRecord{
			ID:               d.requestRecord.ID,
			UserID:           d.userID,
			ProjectID:        d.projectID,
			APIKeyID:         d.apiKeyID,
			RequestedModelID: d.requestRecord.RequestedModelID,
			Status:           requestlog.RequestStatus(d.requestRecord.Status),
		},
		AttemptRecord: requestlog.AttemptRecord{
			ID:              d.attemptRecord.ID,
			RequestRecordID: d.requestRecord.ID,
			AttemptIndex:    int(d.attemptRecord.AttemptIndex),
			ProviderID:      d.providerID,
			ChannelID:       d.channelID,
			AdapterKey:      d.attemptRecord.AdapterKey,
			UpstreamModel:   d.attemptRecord.UpstreamModel,
			Status:          requestlog.AttemptStatus(d.attemptRecord.Status),
			StartedAt:       d.attemptRecord.StartedAt.Time,
		},
		Principal: &auth.APIKeyPrincipal{UserID: d.userID, ProjectID: d.projectID, APIKeyID: d.apiKeyID},
		Authorization: ChatAuthorization{
			ReservationID:    d.reservationID,
			RequestRecordID:  d.requestRecord.ID,
			EstimatedAmount:  testNumeric(1_0000000000, -10),
			AuthorizedAmount: testNumeric(1_0000000000, -10),
			Currency:         "USD",
			PriceID:          d.priceID,
			Price:            chatSettlementAuthorizationPrice(),
		},
		ResponseModelID:       "openai/gpt-4.1",
		ModelDBID:             d.modelID,
		FinalProviderID:       d.providerID,
		FinalChannelID:        d.channelID,
		UpstreamResponseModel: "gpt-4.1",
		UpstreamStatusCode:    200,
		UpstreamRequestID:     upstreamRequestIDPtr("req-settlement-1"),
		Usage: adapter.ChatUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
			CachedTokens:     3,
			ReasoningTokens:  2,
		},
		UsageSource: ChatSettlementUsageSourceUpstreamResponse,
	}
}

// insertChatSettlementProvider 插入测试 provider。
func insertChatSettlementProvider(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix int64) int64 {
	t.Helper()

	var id int64
	err := pool.QueryRow(ctx, `
		INSERT INTO providers (slug, name, adapter, status)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, fmt.Sprintf("chat-settlement-provider-%d", suffix), "Chat Settlement Provider", "openai", "enabled").Scan(&id)
	if err != nil {
		t.Fatalf("insert provider: %v", err)
	}

	return id
}

// insertChatSettlementChannel 插入测试 channel。
func insertChatSettlementChannel(t *testing.T, ctx context.Context, pool *pgxpool.Pool, providerID int64, suffix int64) int64 {
	t.Helper()

	var id int64
	err := pool.QueryRow(ctx, `
		INSERT INTO channels (provider_id, name, base_url, credential_ref, status, priority, timeout_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, providerID, fmt.Sprintf("chat-settlement-channel-%d", suffix), "https://example.test/v1", "secret://chat-settlement", "enabled", 10, 30000).Scan(&id)
	if err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	return id
}

// insertChatSettlementModel 插入测试 model。
func insertChatSettlementModel(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix int64) int64 {
	t.Helper()

	var id int64
	err := pool.QueryRow(ctx, `
		INSERT INTO models (model_id, display_name, owned_by, status)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, fmt.Sprintf("chat-settlement-model-%d", suffix), "Chat Settlement Model", "openai", "enabled").Scan(&id)
	if err != nil {
		t.Fatalf("insert model: %v", err)
	}

	return id
}

// insertChatSettlementChannelModel 插入测试 channel/model 服务映射。
func insertChatSettlementChannelModel(t *testing.T, ctx context.Context, pool *pgxpool.Pool, channelID int64, modelID int64) {
	t.Helper()

	if _, err := pool.Exec(ctx, `
		INSERT INTO channel_models (channel_id, model_id, upstream_model, status)
		VALUES ($1, $2, $3, $4)
	`, channelID, modelID, "gpt-4.1", "enabled"); err != nil {
		t.Fatalf("insert channel model: %v", err)
	}
}

// chatSettlementBilling 创建测试用 billing calculator。
func chatSettlementBilling(amount pgtype.Numeric) *fakeChatBillingCalculator {
	return &fakeChatBillingCalculator{
		charge: billing.CustomerCharge{
			Amount:         amount,
			Currency:       "USD",
			FormulaVersion: billing.FormulaVersionV1,
		},
		cost: chatSettlementProviderCost(),
	}
}

// chatSettlementAuthorizationPrice 返回 seed 中创建价格对应的 billing 快照。
func chatSettlementAuthorizationPrice() billing.CustomerPriceSnapshot {
	return billing.CustomerPriceSnapshot{
		Currency:             "USD",
		PricingUnit:          billing.PricingUnitPer1MTokens,
		InputPrice:           testNumeric(2_0000000000, -10),
		OutputPrice:          testNumeric(8_0000000000, -10),
		CachedInputPrice:     testNumeric(5000000000, -10),
		ReasoningOutputPrice: testNumeric(12_0000000000, -10),
		FormulaVersion:       billing.FormulaVersionV1,
	}
}

// chatSettlementProviderCost 返回当前测试 usage 和成本价对应的平台成本分项。
func chatSettlementProviderCost() billing.ProviderCost {
	return billing.ProviderCost{
		InputCostAmount:           testNumeric(70000, -10),
		OutputCostAmount:          testNumeric(120000, -10),
		CachedInputCostAmount:     testNumeric(7500, -10),
		ReasoningOutputCostAmount: testNumeric(120000, -10),
		TotalCostAmount:           testNumeric(317500, -10),
		Currency:                  "USD",
		FormulaVersion:            billing.FormulaVersionV1,
	}
}

// requestStatus 查询 request record 当前状态。
func requestStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, requestRecordID int64) string {
	t.Helper()

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM request_records WHERE id = $1`, requestRecordID).Scan(&status); err != nil {
		t.Fatalf("query request status: %v", err)
	}

	return status
}

// requestTableCount 查询指定请求在事实表中的记录数量。
func requestTableCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, requestRecordID int64) int {
	t.Helper()

	var count int
	query := fmt.Sprintf(`SELECT count(*) FROM %s WHERE request_record_id = $1`, table)
	if err := pool.QueryRow(ctx, query, requestRecordID).Scan(&count); err != nil {
		t.Fatalf("count %s by request: %v", table, err)
	}

	return count
}

// requestDebitLedgerCount 查询指定请求的 debit ledger entry 数量。
func requestDebitLedgerCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, requestRecordID int64) int {
	t.Helper()

	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM ledger_entries
		WHERE request_record_id = $1
		  AND entry_type = 'debit'
	`, requestRecordID).Scan(&count); err != nil {
		t.Fatalf("count debit ledger entries by request: %v", err)
	}

	return count
}

// attemptStatus 查询 request attempt 当前状态。
func attemptStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attemptID int64) string {
	t.Helper()

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM request_attempts WHERE id = $1`, attemptID).Scan(&status); err != nil {
		t.Fatalf("query attempt status: %v", err)
	}

	return status
}

// attemptUpstreamMetadata 查询 request attempt 写入的真实上游 status code 和 request id。
func attemptUpstreamMetadata(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attemptID int64) (int, string) {
	t.Helper()

	var statusCode pgtype.Int4
	var requestID pgtype.Text
	if err := pool.QueryRow(ctx, `
		SELECT upstream_status_code, upstream_request_id
		FROM request_attempts
		WHERE id = $1
	`, attemptID).Scan(&statusCode, &requestID); err != nil {
		t.Fatalf("query attempt upstream metadata: %v", err)
	}

	return int(statusCode.Int32), requestID.String
}

// testNumeric 创建测试用 pgtype.Numeric。
func testNumeric(value int64, exp int32) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(value), Exp: exp, Valid: true}
}

// assertNumericEqual 校验 NUMERIC 值相等，忽略 PostgreSQL 返回的 scale 差异。
func assertNumericEqual(t *testing.T, got pgtype.Numeric, want pgtype.Numeric) {
	t.Helper()

	if got.Valid != want.Valid {
		t.Fatalf("expected numeric valid=%v, got valid=%v", want.Valid, got.Valid)
	}
	if !want.Valid {
		return
	}
	if got.Int == nil || want.Int == nil {
		t.Fatalf("expected numeric ints to be set, got=%v want=%v", got.Int, want.Int)
	}

	if numericRat(got).Cmp(numericRat(want)) != 0 {
		t.Fatalf("expected numeric %s, got %s", numericRat(want).String(), numericRat(got).String())
	}
}

func numericRat(value pgtype.Numeric) *big.Rat {
	rat := new(big.Rat).SetInt(value.Int)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(absInt32(value.Exp))), nil)
	if value.Exp < 0 {
		return rat.Quo(rat, new(big.Rat).SetInt(scale))
	}
	return rat.Mul(rat, new(big.Rat).SetInt(scale))
}

func absInt32(value int32) int32 {
	if value < 0 {
		return -value
	}
	return value
}

func TestChatSettlementSettlesSuccessfulChat(t *testing.T) {
	deps := newChatSettlementDBDeps(t)
	billingCalculator := chatSettlementBilling(testNumeric(61_000000, -10))
	ledgerService := ledger.NewService(deps.pool, deps.queries)
	service := NewChatSettlementService(deps.pool, deps.queries, billingCalculator, ledgerService)

	if err := service.SettleSuccessfulChat(deps.ctx, deps.params()); err != nil {
		t.Fatalf("settle successful chat: %v", err)
	}

	usageRecord, err := deps.queries.GetUsageRecordByRequest(deps.ctx, deps.requestRecord.ID)
	if err != nil {
		t.Fatalf("get usage record: %v", err)
	}
	if usageRecord.PromptTokens != 10 || usageRecord.CompletionTokens != 5 || usageRecord.TotalTokens != 15 {
		t.Fatalf("expected usage 10/5/15, got %d/%d/%d", usageRecord.PromptTokens, usageRecord.CompletionTokens, usageRecord.TotalTokens)
	}
	if usageRecord.CachedTokens != 3 || usageRecord.ReasoningTokens != 2 {
		t.Fatalf("expected cached/reasoning usage 3/2, got %d/%d", usageRecord.CachedTokens, usageRecord.ReasoningTokens)
	}
	if usageRecord.Source != "upstream_response" {
		t.Fatalf("expected source upstream_response, got %q", usageRecord.Source)
	}

	snapshot, err := deps.queries.GetPriceSnapshotByRequest(deps.ctx, deps.requestRecord.ID)
	if err != nil {
		t.Fatalf("get price snapshot: %v", err)
	}
	if !snapshot.PriceID.Valid || snapshot.PriceID.Int64 != deps.priceID {
		t.Fatalf("expected snapshot price id %d, got valid=%v value=%d", deps.priceID, snapshot.PriceID.Valid, snapshot.PriceID.Int64)
	}
	if snapshot.FormulaVersion != billing.FormulaVersionV1 {
		t.Fatalf("expected formula version %q, got %q", billing.FormulaVersionV1, snapshot.FormulaVersion)
	}

	costSnapshot, err := deps.queries.GetCostSnapshotByRequest(deps.ctx, deps.requestRecord.ID)
	if err != nil {
		t.Fatalf("get cost snapshot: %v", err)
	}
	if costSnapshot.CostPriceID != deps.costPriceID {
		t.Fatalf("expected cost price id %d, got %d", deps.costPriceID, costSnapshot.CostPriceID)
	}
	if costSnapshot.ProviderID != deps.providerID || costSnapshot.ChannelID != deps.channelID || costSnapshot.ModelID != deps.modelID {
		t.Fatalf("unexpected cost snapshot route provider/channel/model %d/%d/%d", costSnapshot.ProviderID, costSnapshot.ChannelID, costSnapshot.ModelID)
	}
	if costSnapshot.UpstreamModel != "gpt-4.1" {
		t.Fatalf("expected upstream model gpt-4.1, got %q", costSnapshot.UpstreamModel)
	}
	assertNumericEqual(t, costSnapshot.InputCost, testNumeric(1_0000000000, -10))
	assertNumericEqual(t, costSnapshot.OutputCost, testNumeric(4_0000000000, -10))
	assertNumericEqual(t, costSnapshot.CachedInputCost, testNumeric(2500000000, -10))
	assertNumericEqual(t, costSnapshot.ReasoningOutputCost, testNumeric(6_0000000000, -10))
	assertNumericEqual(t, costSnapshot.InputCostAmount, chatSettlementProviderCost().InputCostAmount)
	assertNumericEqual(t, costSnapshot.OutputCostAmount, chatSettlementProviderCost().OutputCostAmount)
	assertNumericEqual(t, costSnapshot.CachedInputCostAmount, chatSettlementProviderCost().CachedInputCostAmount)
	assertNumericEqual(t, costSnapshot.ReasoningOutputCostAmount, chatSettlementProviderCost().ReasoningOutputCostAmount)
	assertNumericEqual(t, costSnapshot.TotalCostAmount, chatSettlementProviderCost().TotalCostAmount)

	entry, err := deps.queries.GetLedgerEntryByIdempotencyKey(deps.ctx, fmt.Sprintf("chat:settle:%d", deps.requestRecord.ID))
	if err != nil {
		t.Fatalf("get ledger entry: %v", err)
	}
	if entry.UserID != deps.userID || entry.EntryType != "debit" {
		t.Fatalf("unexpected ledger entry user/type %d/%q", entry.UserID, entry.EntryType)
	}

	if status := requestStatus(t, deps.ctx, deps.pool, deps.requestRecord.ID); status != string(requestlog.RequestStatusSucceeded) {
		t.Fatalf("expected request succeeded, got %q", status)
	}
	if status := attemptStatus(t, deps.ctx, deps.pool, deps.attemptRecord.ID); status != string(requestlog.AttemptStatusSucceeded) {
		t.Fatalf("expected attempt succeeded, got %q", status)
	}

	gotStatusCode, gotRequestID := attemptUpstreamMetadata(t, deps.ctx, deps.pool, deps.attemptRecord.ID)
	if gotStatusCode != 200 {
		t.Fatalf("expected attempt upstream status 200, got %d", gotStatusCode)
	}
	if gotRequestID != "req-settlement-1" {
		t.Fatalf("expected attempt upstream request id %q, got %q", "req-settlement-1", gotRequestID)
	}

	if len(billingCalculator.usages) != 1 || len(billingCalculator.prices) != 1 {
		t.Fatalf("expected one billing calculation, got usages=%d prices=%d", len(billingCalculator.usages), len(billingCalculator.prices))
	}
	if len(billingCalculator.costUsages) != 1 || len(billingCalculator.costs) != 1 {
		t.Fatalf("expected one provider cost calculation, got usages=%d costs=%d", len(billingCalculator.costUsages), len(billingCalculator.costs))
	}
	billingUsage := billingCalculator.usages[0]
	if billingUsage.CachedTokens != 3 || billingUsage.ReasoningTokens != 2 {
		t.Fatalf("expected billing cached/reasoning usage 3/2, got %d/%d", billingUsage.CachedTokens, billingUsage.ReasoningTokens)
	}
	costUsage := billingCalculator.costUsages[0]
	if costUsage.CachedTokens != 3 || costUsage.ReasoningTokens != 2 {
		t.Fatalf("expected provider cost cached/reasoning usage 3/2, got %d/%d", costUsage.CachedTokens, costUsage.ReasoningTokens)
	}
}

func TestChatSettlementUsesAuthorizationPriceWhenActivePriceChanges(t *testing.T) {
	deps := newChatSettlementDBDeps(t)
	params := deps.params()

	// prices 排他约束不允许同一模型存在重叠的 enabled 生效窗口。
	// 先收口 seed 价格窗口，再新增一个相邻的 enabled 价格，模拟“当前生效价格已变更”。
	priceChangeAt := time.Now()
	if _, err := deps.pool.Exec(deps.ctx, `UPDATE prices SET effective_to = $2 WHERE id = $1`, deps.priceID, priceChangeAt); err != nil {
		t.Fatalf("close seed price window: %v", err)
	}

	newPrice, err := deps.queries.CreatePrice(deps.ctx, sqlc.CreatePriceParams{
		ModelID:              deps.modelID,
		Currency:             "USD",
		PricingUnit:          billing.PricingUnitPer1MTokens,
		InputPrice:           testNumeric(99_0000000000, -10),
		OutputPrice:          testNumeric(199_0000000000, -10),
		CachedInputPrice:     testNumeric(49_0000000000, -10),
		ReasoningOutputPrice: testNumeric(299_0000000000, -10),
		Status:               "enabled",
		EffectiveFrom:        pgtype.Timestamptz{Time: priceChangeAt, Valid: true},
		EffectiveTo:          pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create replacement price: %v", err)
	}
	t.Cleanup(func() {
		_, _ = deps.pool.Exec(context.Background(), `DELETE FROM prices WHERE id = $1`, newPrice.ID)
	})

	billingCalculator := chatSettlementBilling(testNumeric(61_000000, -10))
	ledgerCapturer := &fakeChatLedgerCapturer{}
	service := NewChatSettlementService(deps.pool, deps.queries, billingCalculator, ledgerCapturer)

	if err := service.SettleSuccessfulChat(deps.ctx, params); err != nil {
		t.Fatalf("settle successful chat: %v", err)
	}

	snapshot, err := deps.queries.GetPriceSnapshotByRequest(deps.ctx, deps.requestRecord.ID)
	if err != nil {
		t.Fatalf("get price snapshot: %v", err)
	}
	if !snapshot.PriceID.Valid || snapshot.PriceID.Int64 != deps.priceID {
		t.Fatalf("expected authorization price id %d, got valid=%v value=%d", deps.priceID, snapshot.PriceID.Valid, snapshot.PriceID.Int64)
	}
	if snapshot.PriceID.Int64 == newPrice.ID {
		t.Fatalf("expected settlement not to use replacement price id %d", newPrice.ID)
	}

	assertNumericEqual(t, snapshot.InputPrice, params.Authorization.Price.InputPrice)
	assertNumericEqual(t, snapshot.OutputPrice, params.Authorization.Price.OutputPrice)
	assertNumericEqual(t, snapshot.CachedInputPrice, params.Authorization.Price.CachedInputPrice)
	assertNumericEqual(t, snapshot.ReasoningOutputPrice, params.Authorization.Price.ReasoningOutputPrice)

	if len(billingCalculator.prices) != 1 {
		t.Fatalf("expected one billing price, got %d", len(billingCalculator.prices))
	}
	assertNumericEqual(t, billingCalculator.prices[0].InputPrice, params.Authorization.Price.InputPrice)
	assertNumericEqual(t, billingCalculator.prices[0].OutputPrice, params.Authorization.Price.OutputPrice)
	assertNumericEqual(t, billingCalculator.prices[0].CachedInputPrice, params.Authorization.Price.CachedInputPrice)
	assertNumericEqual(t, billingCalculator.prices[0].ReasoningOutputPrice, params.Authorization.Price.ReasoningOutputPrice)
}

func TestChatSettlementUsesAttemptTimeCostPriceWhenActiveCostChanges(t *testing.T) {
	deps := newChatSettlementDBDeps(t)
	params := deps.params()

	newCostPrice, err := deps.queries.CreateChannelCostPrice(deps.ctx, sqlc.CreateChannelCostPriceParams{
		ChannelID:           deps.channelID,
		ModelID:             deps.modelID,
		Currency:            "USD",
		PricingUnit:         billing.PricingUnitPer1MTokens,
		InputCost:           testNumeric(99_0000000000, -10),
		OutputCost:          testNumeric(199_0000000000, -10),
		CachedInputCost:     testNumeric(49_0000000000, -10),
		ReasoningOutputCost: testNumeric(299_0000000000, -10),
		Status:              "enabled",
		// 必须明显晚于 attempt 时间：PostgreSQL timestamptz 精度是微秒，
		// 纳秒级偏移会被舍入到同一时刻，导致按 effective_from DESC 误选这条更新的成本价。
		EffectiveFrom: pgtype.Timestamptz{Time: params.AttemptRecord.StartedAt.Add(time.Minute), Valid: true},
		EffectiveTo:   pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create replacement cost price: %v", err)
	}
	t.Cleanup(func() {
		_, _ = deps.pool.Exec(context.Background(), `DELETE FROM channel_cost_prices WHERE id = $1`, newCostPrice.ID)
	})

	billingCalculator := chatSettlementBilling(testNumeric(61_000000, -10))
	ledgerCapturer := &fakeChatLedgerCapturer{}
	service := NewChatSettlementService(deps.pool, deps.queries, billingCalculator, ledgerCapturer)

	if err := service.SettleSuccessfulChat(deps.ctx, params); err != nil {
		t.Fatalf("settle successful chat: %v", err)
	}

	costSnapshot, err := deps.queries.GetCostSnapshotByRequest(deps.ctx, deps.requestRecord.ID)
	if err != nil {
		t.Fatalf("get cost snapshot: %v", err)
	}
	if costSnapshot.CostPriceID != deps.costPriceID {
		t.Fatalf("expected attempt-time cost price id %d, got %d", deps.costPriceID, costSnapshot.CostPriceID)
	}
	if costSnapshot.CostPriceID == newCostPrice.ID {
		t.Fatalf("expected settlement not to use replacement cost price id %d", newCostPrice.ID)
	}
	assertNumericEqual(t, costSnapshot.InputCost, testNumeric(1_0000000000, -10))
	assertNumericEqual(t, costSnapshot.OutputCost, testNumeric(4_0000000000, -10))
}

func TestChatSettlementReturnsIdempotentSuccessAfterRequestSucceeded(t *testing.T) {
	deps := newChatSettlementDBDeps(t)
	billingCalculator := chatSettlementBilling(testNumeric(61_000000, -10))
	ledgerService := ledger.NewService(deps.pool, deps.queries)
	service := NewChatSettlementService(deps.pool, deps.queries, billingCalculator, ledgerService)
	params := deps.params()

	if err := service.SettleSuccessfulChat(deps.ctx, params); err != nil {
		t.Fatalf("settle successful chat: %v", err)
	}
	if err := service.SettleSuccessfulChat(deps.ctx, params); err != nil {
		t.Fatalf("repeat successful settlement: %v", err)
	}

	if got := requestTableCount(t, deps.ctx, deps.pool, "usage_records", deps.requestRecord.ID); got != 1 {
		t.Fatalf("expected one usage record after replay, got %d", got)
	}
	if got := requestTableCount(t, deps.ctx, deps.pool, "price_snapshots", deps.requestRecord.ID); got != 1 {
		t.Fatalf("expected one price snapshot after replay, got %d", got)
	}
	if got := requestTableCount(t, deps.ctx, deps.pool, "cost_snapshots", deps.requestRecord.ID); got != 1 {
		t.Fatalf("expected one cost snapshot after replay, got %d", got)
	}
	if got := requestDebitLedgerCount(t, deps.ctx, deps.pool, deps.requestRecord.ID); got != 1 {
		t.Fatalf("expected one debit ledger entry after replay, got %d", got)
	}
	if status := requestStatus(t, deps.ctx, deps.pool, deps.requestRecord.ID); status != string(requestlog.RequestStatusSucceeded) {
		t.Fatalf("expected request succeeded after replay, got %q", status)
	}
}

func TestChatSettlementRejectsReplayWithDifferentUsage(t *testing.T) {
	deps := newChatSettlementDBDeps(t)
	billingCalculator := chatSettlementBilling(testNumeric(61_000000, -10))
	ledgerService := ledger.NewService(deps.pool, deps.queries)
	service := NewChatSettlementService(deps.pool, deps.queries, billingCalculator, ledgerService)
	params := deps.params()

	if err := service.SettleSuccessfulChat(deps.ctx, params); err != nil {
		t.Fatalf("settle successful chat: %v", err)
	}

	replayed := params
	replayed.Usage.TotalTokens++
	err := service.SettleSuccessfulChat(deps.ctx, replayed)
	if got := failure.CodeOf(err); got != failure.CodeGatewayChatSettlementIdempotencyConflict {
		t.Fatalf("expected failure code %q, got %q err=%v", failure.CodeGatewayChatSettlementIdempotencyConflict, got, err)
	}
	if got := requestTableCount(t, deps.ctx, deps.pool, "usage_records", deps.requestRecord.ID); got != 1 {
		t.Fatalf("expected one usage record after rejected replay, got %d", got)
	}
	if got := requestDebitLedgerCount(t, deps.ctx, deps.pool, deps.requestRecord.ID); got != 1 {
		t.Fatalf("expected one debit ledger entry after rejected replay, got %d", got)
	}
}

func TestChatSettlementRejectsReplayWithDifferentCostSnapshot(t *testing.T) {
	deps := newChatSettlementDBDeps(t)
	billingCalculator := chatSettlementBilling(testNumeric(61_000000, -10))
	ledgerService := ledger.NewService(deps.pool, deps.queries)
	service := NewChatSettlementService(deps.pool, deps.queries, billingCalculator, ledgerService)
	params := deps.params()

	if err := service.SettleSuccessfulChat(deps.ctx, params); err != nil {
		t.Fatalf("settle successful chat: %v", err)
	}

	if _, err := deps.pool.Exec(deps.ctx, `
		UPDATE cost_snapshots
		SET input_cost_amount = input_cost_amount + 0.000001,
		    total_cost_amount = total_cost_amount + 0.000001
		WHERE request_record_id = $1
	`, deps.requestRecord.ID); err != nil {
		t.Fatalf("tamper cost snapshot: %v", err)
	}

	err := service.SettleSuccessfulChat(deps.ctx, params)
	if got := failure.CodeOf(err); got != failure.CodeGatewayChatSettlementIdempotencyConflict {
		t.Fatalf("expected failure code %q, got %q err=%v", failure.CodeGatewayChatSettlementIdempotencyConflict, got, err)
	}
	if got := requestTableCount(t, deps.ctx, deps.pool, "cost_snapshots", deps.requestRecord.ID); got != 1 {
		t.Fatalf("expected one cost snapshot after rejected replay, got %d", got)
	}
}

func TestEnsureSettlementUsageMatchesAcceptsStreamSource(t *testing.T) {
	usage := adapter.ChatUsage{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		CachedTokens:     3,
		ReasoningTokens:  2,
	}
	row := sqlc.UsageRecord{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		CachedTokens:     3,
		ReasoningTokens:  2,
		Source:           string(ChatSettlementUsageSourceUpstreamStream),
	}

	if err := ensureSettlementUsageMatches(row, usage, ChatSettlementUsageSourceUpstreamStream); err != nil {
		t.Fatalf("expected stream usage source to match: %v", err)
	}
}

func TestEnsureSettlementUsageMatchesRejectsDifferentSource(t *testing.T) {
	usage := adapter.ChatUsage{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		CachedTokens:     3,
		ReasoningTokens:  2,
	}
	row := sqlc.UsageRecord{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		CachedTokens:     3,
		ReasoningTokens:  2,
		Source:           string(ChatSettlementUsageSourceUpstreamResponse),
	}

	err := ensureSettlementUsageMatches(row, usage, ChatSettlementUsageSourceUpstreamStream)
	if got := failure.CodeOf(err); got != failure.CodeGatewayChatSettlementIdempotencyConflict {
		t.Fatalf("expected failure code %q, got %q err=%v", failure.CodeGatewayChatSettlementIdempotencyConflict, got, err)
	}
}

func TestChatSettlementReleasesReservationForZeroAmount(t *testing.T) {
	deps := newChatSettlementDBDeps(t)
	billingCalculator := chatSettlementBilling(testNumeric(0, -10))
	ledgerCapturer := &fakeChatLedgerCapturer{}
	service := NewChatSettlementService(deps.pool, deps.queries, billingCalculator, ledgerCapturer)

	if err := service.SettleSuccessfulChat(deps.ctx, deps.params()); err != nil {
		t.Fatalf("settle successful chat: %v", err)
	}

	if len(ledgerCapturer.captureParams) != 0 {
		t.Fatalf("expected no ledger capture for zero amount, got %d", len(ledgerCapturer.captureParams))
	}
	if len(ledgerCapturer.releaseParams) != 1 {
		t.Fatalf("expected one ledger release for zero amount, got %d", len(ledgerCapturer.releaseParams))
	}
	if status := requestStatus(t, deps.ctx, deps.pool, deps.requestRecord.ID); status != string(requestlog.RequestStatusSucceeded) {
		t.Fatalf("expected request succeeded, got %q", status)
	}
	if _, err := deps.queries.GetUsageRecordByRequest(deps.ctx, deps.requestRecord.ID); err != nil {
		t.Fatalf("expected committed usage record: %v", err)
	}
	if _, err := deps.queries.GetPriceSnapshotByRequest(deps.ctx, deps.requestRecord.ID); err != nil {
		t.Fatalf("expected committed price snapshot: %v", err)
	}
	if _, err := deps.queries.GetCostSnapshotByRequest(deps.ctx, deps.requestRecord.ID); err != nil {
		t.Fatalf("expected committed cost snapshot: %v", err)
	}
}

func TestChatSettlementRollsBackFactsWhenLedgerCaptureFails(t *testing.T) {
	deps := newChatSettlementDBDeps(t)
	ledgerErr := errors.New("ledger capture failed")
	ledgerCapturer := &fakeChatLedgerCapturer{err: ledgerErr}
	service := NewChatSettlementService(
		deps.pool,
		deps.queries,
		chatSettlementBilling(testNumeric(61_000000, -10)),
		ledgerCapturer,
	)

	err := service.SettleSuccessfulChat(deps.ctx, deps.params())
	if !errors.Is(err, ledgerErr) {
		t.Fatalf("expected ledger error, got %v", err)
	}
	if len(ledgerCapturer.captureParams) != 1 {
		t.Fatalf("expected one ledger capture attempt, got %d", len(ledgerCapturer.captureParams))
	}

	if _, err := deps.queries.GetUsageRecordByRequest(deps.ctx, deps.requestRecord.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected usage record rollback, got %v", err)
	}
	if _, err := deps.queries.GetPriceSnapshotByRequest(deps.ctx, deps.requestRecord.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected price snapshot rollback, got %v", err)
	}
	if _, err := deps.queries.GetCostSnapshotByRequest(deps.ctx, deps.requestRecord.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected cost snapshot rollback, got %v", err)
	}
	if status := requestStatus(t, deps.ctx, deps.pool, deps.requestRecord.ID); status != string(requestlog.RequestStatusRunning) {
		t.Fatalf("expected request to remain running after rollback, got %q", status)
	}
	if status := attemptStatus(t, deps.ctx, deps.pool, deps.attemptRecord.ID); status != string(requestlog.AttemptStatusRunning) {
		t.Fatalf("expected attempt to remain running after rollback, got %q", status)
	}
}

func TestChatSettlementRollsBackFactsWhenBillingFails(t *testing.T) {
	deps := newChatSettlementDBDeps(t)
	billingErr := errors.New("billing calculation failed")
	billingCalculator := chatSettlementBilling(testNumeric(61_000000, -10))
	billingCalculator.err = billingErr
	ledgerCapturer := &fakeChatLedgerCapturer{}
	service := NewChatSettlementService(deps.pool, deps.queries, billingCalculator, ledgerCapturer)

	err := service.SettleSuccessfulChat(deps.ctx, deps.params())
	if !errors.Is(err, billingErr) {
		t.Fatalf("expected billing error, got %v", err)
	}
	if len(ledgerCapturer.captureParams) != 0 {
		t.Fatalf("expected no ledger capture after billing error, got %d", len(ledgerCapturer.captureParams))
	}
	if len(ledgerCapturer.releaseParams) != 0 {
		t.Fatalf("expected no ledger release after billing error, got %d", len(ledgerCapturer.releaseParams))
	}
	if _, err := deps.queries.GetUsageRecordByRequest(deps.ctx, deps.requestRecord.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected usage record rollback, got %v", err)
	}
	if _, err := deps.queries.GetPriceSnapshotByRequest(deps.ctx, deps.requestRecord.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected price snapshot rollback, got %v", err)
	}
	if _, err := deps.queries.GetCostSnapshotByRequest(deps.ctx, deps.requestRecord.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected cost snapshot rollback, got %v", err)
	}
}
