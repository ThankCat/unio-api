package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/apikey"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/ledger"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	coreusage "github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeChatBillingCalculator 是 chat settlement 测试使用的 billing calculator 替身。
type fakeChatBillingCalculator struct {
	usages     []coreusage.Facts
	prices     []billing.CustomerPriceSnapshot
	costUsages []coreusage.Facts
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
func (c *fakeChatBillingCalculator) CalculateCustomerCharge(facts coreusage.Facts, price billing.CustomerPriceSnapshot) (billing.CustomerCharge, error) {
	c.usages = append(c.usages, facts)
	c.prices = append(c.prices, price)
	if c.err != nil {
		return billing.CustomerCharge{}, c.err
	}

	return c.charge, nil
}

// CalculateProviderCost 记录 billing 入参，并返回测试预设平台成本结果。
func (c *fakeChatBillingCalculator) CalculateProviderCost(facts coreusage.Facts, cost billing.ProviderCostSnapshot) (billing.ProviderCost, error) {
	c.costUsages = append(c.costUsages, facts)
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
	ctx            context.Context
	cancel         context.CancelFunc
	pool           *pgxpool.Pool
	queries        *sqlc.Queries
	userID         int64
	apiKeyID       int64
	routeID        int64
	providerID     int64
	channelID      int64
	modelID        int64
	channelPriceID int64
	reservationID  int64
	requestRecord  sqlc.RequestRecord
	attemptRecord  sqlc.RequestAttempt
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
	if d.channelPriceID != 0 {
		_, _ = d.pool.Exec(ctx, `DELETE FROM channel_prices WHERE channel_id = $1 AND model_id = $2`, d.channelID, d.modelID)
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
		// model_prices 以 model_id 外键引用 models（无级联），倍率路径测试会为该模型建基准价行，删模型前先清。
		_, _ = d.pool.Exec(ctx, `DELETE FROM model_prices WHERE model_id = $1`, d.modelID)
		_, _ = d.pool.Exec(ctx, `DELETE FROM models WHERE id = $1`, d.modelID)
	}
	if d.apiKeyID != 0 {
		_, _ = d.pool.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, d.apiKeyID)
	}
	if d.routeID != 0 {
		// 线路被 api_keys.route_id 外键引用，必须在 api_keys 删除后再删。
		_, _ = d.pool.Exec(ctx, `DELETE FROM routes WHERE id = $1`, d.routeID)
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

	generatedKey, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}

	// 线路必填：先建一条线路供 API Key 绑定（route_id 现为 NOT NULL）。
	var priceRatio pgtype.Numeric
	if err := priceRatio.Scan("1"); err != nil {
		t.Fatalf("scan price ratio: %v", err)
	}
	route, err := d.queries.CreateRoute(d.ctx, sqlc.CreateRouteParams{
		Name:       fmt.Sprintf("chat-settlement-route-%d", suffix),
		Mode:       "cheapest",
		PoolKind:   "all",
		Status:     "enabled",
		PriceRatio: priceRatio,
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	d.routeID = route.ID

	apiKey, err := d.queries.CreateAPIKey(d.ctx, sqlc.CreateAPIKeyParams{
		UserID:    user.ID,
		Name:      "chat settlement key",
		KeyPrefix: generatedKey.Prefix,
		KeyHash:   generatedKey.Hash,
		ExpiresAt: pgtype.Timestamptz{Valid: false},
		RouteID:   route.ID,
	})
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}
	d.apiKeyID = apiKey.ID

	d.providerID = insertChatSettlementProvider(t, d.ctx, d.pool, suffix)
	d.channelID = insertChatSettlementChannel(t, d.ctx, d.pool, d.providerID, suffix)
	d.modelID = insertChatSettlementModel(t, d.ctx, d.pool, suffix)
	insertChatSettlementChannelModel(t, d.ctx, d.pool, d.channelID, d.modelID)

	// DEC-026：渠道只录成本（客户售价取 model_prices × 线路倍率，由 params.SalePrice 透传）。settlement 按命中渠道重查这一行取成本。
	channelPrice, err := d.queries.CreateChannelPrice(d.ctx, sqlc.CreateChannelPriceParams{
		ChannelID:           d.channelID,
		ModelID:             d.modelID,
		Currency:            "USD",
		PricingUnit:         billing.PricingUnitPer1MTokens,
		UncachedInputCost:   testNumeric(1_0000000000, -10),
		OutputCost:          testNumeric(4_0000000000, -10),
		CacheReadInputCost:  testNumeric(2500000000, -10),
		ReasoningOutputCost: testNumeric(6_0000000000, -10),
		Status:              "enabled",
		EffectiveFrom:       pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
		EffectiveTo:         pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create channel price: %v", err)
	}
	d.channelPriceID = channelPrice.ID

	requestRecord, err := d.queries.CreateRequestRecord(d.ctx, sqlc.CreateRequestRecordParams{
		RequestID:        fmt.Sprintf("chat-settlement-request-%d", suffix),
		UserID:           user.ID,
		ApiKeyID:         apiKey.ID,
		RequestedModelID: "openai/gpt-4.1",
		IngressProtocol:  string(requestlog.ProtocolOpenAI),
		Operation:        string(requestlog.OperationChatCompletions),
		ResponseModelID:  pgtype.Text{Valid: false},
		ResponseProtocol: pgtype.Text{Valid: false},
		ResponseID:       pgtype.Text{Valid: false},
		Stream:           false,
		Status:           string(requestlog.RequestStatusRunning),
		FinalProviderID:  pgtype.Int8{Valid: false},
		FinalChannelID:   pgtype.Int8{Valid: false},
		ErrorCode:        pgtype.Text{Valid: false},
		ErrorMessage:     pgtype.Text{Valid: false},
		DeliveryStatus:   string(requestlog.DeliveryStatusNotStarted),
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
		UpstreamProtocol:      string(requestlog.ProtocolOpenAI),
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
		Principal: &auth.APIKeyPrincipal{UserID: d.userID, APIKeyID: d.apiKeyID},
		Authorization: ChatAuthorization{
			ReservationID:    d.reservationID,
			RequestRecordID:  d.requestRecord.ID,
			EstimatedAmount:  testNumeric(1_0000000000, -10),
			AuthorizedAmount: testNumeric(1_0000000000, -10),
			Currency:         "USD",
		},
		ResponseProtocol: requestlog.ProtocolOpenAI,
		ResponseID:       "chatcmpl-settlement-1",
		ResponseModelID:  "openai/gpt-4.1",
		ModelDBID:        d.modelID,
		FinalProviderID:  d.providerID,
		FinalChannelID:   d.channelID,
		// SalePrice 是客户售价快照 = 模型基准价 × 线路倍率（DEC-026），由路由透传到结算写收入快照。
		SalePrice: billing.CustomerPriceSnapshot{
			Currency:             "USD",
			PricingUnit:          billing.PricingUnitPer1MTokens,
			UncachedInputPrice:   testNumeric(3_0000000000, -10),
			CacheReadInputPrice:  testNumeric(0_7500000000, -10),
			OutputPrice:          testNumeric(12_0000000000, -10),
			ReasoningOutputPrice: testNumeric(18_0000000000, -10),
			FormulaVersion:       billing.FormulaVersionV1,
		},
		// PriceRatio 随 SalePrice 一起快照进 price_snapshots.price_ratio（0.5×），供请求展示恒显结算当时倍率。
		PriceRatio: testNumeric(0_5000000000, -10),
		Facts:      chatSettlementFacts(coreusage.SourceUpstreamResponse),
	}
}

// chatSettlementFacts 返回 settlement 测试使用的 OpenAI 不可变响应事实。
func chatSettlementFacts(source coreusage.Source) adapter.ResponseFacts {
	return adapter.ResponseFacts{
		UpstreamProtocol:   string(requestlog.ProtocolOpenAI),
		UpstreamResponseID: "chatcmpl-settlement-1",
		UpstreamModel:      "gpt-4.1",
		Finish: adapter.FinishFacts{
			Class:     adapter.FinishStop,
			RawReason: "stop",
		},
		Usage: adapter.ChatUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
			CachedTokens:     3,
			ReasoningTokens:  2,
		}.ToUsageFacts(),
		UsageSource:         source,
		UsageMappingVersion: "openai.v1",
		Metadata: adapter.UpstreamMetadata{
			StatusCode: 200,
			RequestID:  "req-settlement-1",
		},
	}
}

// insertChatSettlementProvider 插入测试 provider。
func insertChatSettlementProvider(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix int64) int64 {
	t.Helper()

	var id int64
	err := pool.QueryRow(ctx, `
		INSERT INTO providers (slug, name, status)
		VALUES ($1, $2, $3)
		RETURNING id
	`, fmt.Sprintf("chat-settlement-provider-%d", suffix), "Chat Settlement Provider", "enabled").Scan(&id)
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
		INSERT INTO channels (provider_id, name, protocol, adapter_key, base_url, credential, status, priority, timeout_ms)
		VALUES ($1, $2, 'openai', 'openai', $3, $4, $5, $6, $7)
		RETURNING id
	`, providerID, fmt.Sprintf("chat-settlement-channel-%d", suffix), "https://example.test/v1", "sk-chat-settlement-test", "enabled", 10, 30000).Scan(&id)
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

// chatSettlementProviderCost 返回当前测试 usage 和成本价对应的平台成本分项。
func chatSettlementProviderCost() billing.ProviderCost {
	return billing.ProviderCost{
		UncachedInputCostAmount:      testNumeric(70000, -10),
		OutputCostAmount:             testNumeric(120000, -10),
		CacheReadInputCostAmount:     testNumeric(7500, -10),
		CacheWrite5mInputCostAmount:  testNumeric(0, -10),
		CacheWrite1hInputCostAmount:  testNumeric(0, -10),
		CacheWrite30mInputCostAmount: testNumeric(0, -10),
		ReasoningOutputCostAmount:    testNumeric(120000, -10),
		TotalCostAmount:              testNumeric(317500, -10),
		Currency:                     "USD",
		FormulaVersion:               billing.FormulaVersionV1,
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
	// 协议无关 facts 列：PromptTokens(10) 拆为 Uncached(7)+CacheRead(3)，OutputTotal=5，Reasoning=2。
	if usageRecord.UncachedInputTokens != 7 || usageRecord.CacheReadInputTokens != 3 {
		t.Fatalf("expected input tokens uncached=7 cache_read=3, got %d/%d", usageRecord.UncachedInputTokens, usageRecord.CacheReadInputTokens)
	}
	if usageRecord.OutputTokensTotal != 5 || usageRecord.ReasoningOutputTokens != 2 {
		t.Fatalf("expected output tokens total=5 reasoning=2, got %d/%d", usageRecord.OutputTokensTotal, usageRecord.ReasoningOutputTokens)
	}
	if usageRecord.UncachedInputTokensState != string(coreusage.CountKnown) ||
		usageRecord.CacheReadInputTokensState != string(coreusage.CountKnown) ||
		usageRecord.OutputTokensTotalState != string(coreusage.CountKnown) ||
		usageRecord.ReasoningOutputTokensState != string(coreusage.CountKnown) {
		t.Fatalf("expected known usage states, got %+v", usageRecord)
	}
	if usageRecord.CacheWrite5mInputTokensState != string(coreusage.CountNotApplicable) ||
		usageRecord.CacheWrite1hInputTokensState != string(coreusage.CountNotApplicable) {
		t.Fatalf("expected cache write states not_applicable, got 5m=%q 1h=%q", usageRecord.CacheWrite5mInputTokensState, usageRecord.CacheWrite1hInputTokensState)
	}
	if usageRecord.UsageSource != "upstream_response" {
		t.Fatalf("expected usage source upstream_response, got %q", usageRecord.UsageSource)
	}

	snapshot, err := deps.queries.GetPriceSnapshotByRequest(deps.ctx, deps.requestRecord.ID)
	if err != nil {
		t.Fatalf("get price snapshot: %v", err)
	}
	if !snapshot.PriceID.Valid || snapshot.PriceID.Int64 != deps.channelPriceID {
		t.Fatalf("expected snapshot price id %d, got valid=%v value=%d", deps.channelPriceID, snapshot.PriceID.Valid, snapshot.PriceID.Int64)
	}
	if snapshot.FormulaVersion != billing.FormulaVersionV1 {
		t.Fatalf("expected formula version %q, got %q", billing.FormulaVersionV1, snapshot.FormulaVersion)
	}
	// 线路倍率随售价一起快照，供请求详情/列表恒显示结算当时倍率（不随后续改倍率漂移）。
	assertNumericEqual(t, snapshot.PriceRatio, testNumeric(0_5000000000, -10))

	costSnapshot, err := deps.queries.GetCostSnapshotByRequest(deps.ctx, deps.requestRecord.ID)
	if err != nil {
		t.Fatalf("get cost snapshot: %v", err)
	}
	if !costSnapshot.CostPriceID.Valid || costSnapshot.CostPriceID.Int64 != deps.channelPriceID {
		t.Fatalf("expected cost price id %d, got valid=%v value=%d", deps.channelPriceID, costSnapshot.CostPriceID.Valid, costSnapshot.CostPriceID.Int64)
	}
	if costSnapshot.ProviderID != deps.providerID || costSnapshot.ChannelID != deps.channelID || costSnapshot.ModelID != deps.modelID {
		t.Fatalf("unexpected cost snapshot route provider/channel/model %d/%d/%d", costSnapshot.ProviderID, costSnapshot.ChannelID, costSnapshot.ModelID)
	}
	if costSnapshot.UpstreamModel != "gpt-4.1" {
		t.Fatalf("expected upstream model gpt-4.1, got %q", costSnapshot.UpstreamModel)
	}
	assertNumericEqual(t, costSnapshot.UncachedInputCost, testNumeric(1_0000000000, -10))
	assertNumericEqual(t, costSnapshot.OutputCost, testNumeric(4_0000000000, -10))
	assertNumericEqual(t, costSnapshot.CacheReadInputCost, testNumeric(2500000000, -10))
	assertNumericEqual(t, costSnapshot.ReasoningOutputCost, testNumeric(6_0000000000, -10))
	assertNumericEqual(t, costSnapshot.UncachedInputCostAmount, chatSettlementProviderCost().UncachedInputCostAmount)
	assertNumericEqual(t, costSnapshot.OutputCostAmount, chatSettlementProviderCost().OutputCostAmount)
	assertNumericEqual(t, costSnapshot.CacheReadInputCostAmount, chatSettlementProviderCost().CacheReadInputCostAmount)
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
	if v, ok := billingUsage.CacheReadInputTokens.BillableValue(); !ok || v != 3 {
		t.Fatalf("expected billing cache-read usage 3, got %d (ok=%v)", v, ok)
	}
	if v, ok := billingUsage.ReasoningOutputTokens.BillableValue(); !ok || v != 2 {
		t.Fatalf("expected billing reasoning usage 2, got %d (ok=%v)", v, ok)
	}
	costUsage := billingCalculator.costUsages[0]
	if v, ok := costUsage.CacheReadInputTokens.BillableValue(); !ok || v != 3 {
		t.Fatalf("expected provider cost cache-read usage 3, got %d (ok=%v)", v, ok)
	}
	if v, ok := costUsage.ReasoningOutputTokens.BillableValue(); !ok || v != 2 {
		t.Fatalf("expected provider cost reasoning usage 2, got %d (ok=%v)", v, ok)
	}
}

func TestChatSettlementSettlesClientCanceledPartialAsCanceled(t *testing.T) {
	deps := newChatSettlementDBDeps(t)
	billingCalculator := chatSettlementBilling(testNumeric(61_000000, -10))
	ledgerService := ledger.NewService(deps.pool, deps.queries)
	service := NewChatSettlementService(deps.pool, deps.queries, billingCalculator, ledgerService)
	params := deps.params()
	params.RequestFinalStatus = requestlog.RequestStatusCanceled
	params.AttemptFinalStatus = requestlog.AttemptStatusCanceled
	params.ErrorCode = "client_canceled"
	params.ErrorMessage = "client canceled request"
	params.InternalErrorDetail = "context canceled"
	params.ResponseID = "partial-client-canceled"
	params.Facts = BuildPartialStreamFacts(PartialStreamFactsParams{
		Candidate: routing.ChatRouteCandidate{
			ProviderID:    deps.providerID,
			ModelDBID:     deps.modelID,
			Protocol:      string(requestlog.ProtocolOpenAI),
			AdapterKey:    "openai",
			Channel:       channel.Runtime{ID: deps.channelID},
			UpstreamModel: "gpt-4.1",
		},
		StreamResponseID: "partial-client-canceled",
		RequestRecordID:  deps.requestRecord.ID,
		InputTokens:      10,
		OutputTokens:     5,
		Reason:           PartialReasonClientCanceled,
	})

	if err := service.SettleSuccessfulChat(deps.ctx, params); err != nil {
		t.Fatalf("settle client-canceled partial chat: %v", err)
	}

	if status := requestStatus(t, deps.ctx, deps.pool, deps.requestRecord.ID); status != string(requestlog.RequestStatusCanceled) {
		t.Fatalf("expected request canceled, got %q", status)
	}
	if status := attemptStatus(t, deps.ctx, deps.pool, deps.attemptRecord.ID); status != string(requestlog.AttemptStatusCanceled) {
		t.Fatalf("expected attempt canceled, got %q", status)
	}
	if got := requestTableCount(t, deps.ctx, deps.pool, "usage_records", deps.requestRecord.ID); got != 1 {
		t.Fatalf("expected one usage record for canceled partial settlement, got %d", got)
	}
	if got := requestDebitLedgerCount(t, deps.ctx, deps.pool, deps.requestRecord.ID); got != 1 {
		t.Fatalf("expected one debit ledger entry for canceled partial settlement, got %d", got)
	}

	attempts, err := deps.queries.ListRequestAttemptsByRequest(deps.ctx, deps.requestRecord.ID)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("expected one attempt, got %d", len(attempts))
	}
	if !attempts[0].UpstreamFinishReason.Valid || attempts[0].UpstreamFinishReason.String != PartialReasonClientCanceled {
		t.Fatalf("expected attempt finish reason %q, got valid=%v value=%q", PartialReasonClientCanceled, attempts[0].UpstreamFinishReason.Valid, attempts[0].UpstreamFinishReason.String)
	}
	if attempts[0].FinalUsageReceived {
		t.Fatal("expected final_usage_received=false for partial stream estimate")
	}
	if !attempts[0].ErrorCode.Valid || attempts[0].ErrorCode.String != "client_canceled" {
		t.Fatalf("expected attempt error code client_canceled, got valid=%v value=%q", attempts[0].ErrorCode.Valid, attempts[0].ErrorCode.String)
	}

	request, err := deps.queries.GetRequestRecordByRequestID(deps.ctx, deps.requestRecord.RequestID)
	if err != nil {
		t.Fatalf("get request record: %v", err)
	}
	if !request.ErrorCode.Valid || request.ErrorCode.String != "client_canceled" {
		t.Fatalf("expected request error code client_canceled, got valid=%v value=%q", request.ErrorCode.Valid, request.ErrorCode.String)
	}
	if !request.ResponseID.Valid || request.ResponseID.String != "partial-client-canceled" {
		t.Fatalf("expected partial response id, got valid=%v value=%q", request.ResponseID.Valid, request.ResponseID.String)
	}
}

func TestChatSettlementSettlesInterruptedPartialAsFailed(t *testing.T) {
	deps := newChatSettlementDBDeps(t)
	billingCalculator := chatSettlementBilling(testNumeric(61_000000, -10))
	ledgerService := ledger.NewService(deps.pool, deps.queries)
	service := NewChatSettlementService(deps.pool, deps.queries, billingCalculator, ledgerService)
	params := deps.params()
	params.RequestFinalStatus = requestlog.RequestStatusFailed
	params.AttemptFinalStatus = requestlog.AttemptStatusFailed
	params.ErrorCode = "stream_adapter_error"
	params.ErrorMessage = "Upstream stream failed."
	params.InternalErrorDetail = "upstream stream interrupted"
	params.ResponseID = "partial-interrupted"
	params.Facts = BuildPartialStreamFacts(PartialStreamFactsParams{
		Candidate: routing.ChatRouteCandidate{
			ProviderID:    deps.providerID,
			ModelDBID:     deps.modelID,
			Protocol:      string(requestlog.ProtocolOpenAI),
			AdapterKey:    "openai",
			Channel:       channel.Runtime{ID: deps.channelID},
			UpstreamModel: "gpt-4.1",
		},
		StreamResponseID: "partial-interrupted",
		RequestRecordID:  deps.requestRecord.ID,
		InputTokens:      10,
		OutputTokens:     5,
		Reason:           PartialReasonInterrupted,
	})

	if err := service.SettleSuccessfulChat(deps.ctx, params); err != nil {
		t.Fatalf("settle interrupted partial chat: %v", err)
	}

	if status := requestStatus(t, deps.ctx, deps.pool, deps.requestRecord.ID); status != string(requestlog.RequestStatusFailed) {
		t.Fatalf("expected request failed, got %q", status)
	}
	if status := attemptStatus(t, deps.ctx, deps.pool, deps.attemptRecord.ID); status != string(requestlog.AttemptStatusFailed) {
		t.Fatalf("expected attempt failed, got %q", status)
	}
	if got := requestTableCount(t, deps.ctx, deps.pool, "usage_records", deps.requestRecord.ID); got != 1 {
		t.Fatalf("expected one usage record for failed partial settlement, got %d", got)
	}
	if got := requestDebitLedgerCount(t, deps.ctx, deps.pool, deps.requestRecord.ID); got != 1 {
		t.Fatalf("expected one debit ledger entry for failed partial settlement, got %d", got)
	}

	attempts, err := deps.queries.ListRequestAttemptsByRequest(deps.ctx, deps.requestRecord.ID)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("expected one attempt, got %d", len(attempts))
	}
	if !attempts[0].UpstreamFinishReason.Valid || attempts[0].UpstreamFinishReason.String != PartialReasonInterrupted {
		t.Fatalf("expected attempt finish reason %q, got valid=%v value=%q", PartialReasonInterrupted, attempts[0].UpstreamFinishReason.Valid, attempts[0].UpstreamFinishReason.String)
	}
	if attempts[0].FinalUsageReceived {
		t.Fatal("expected final_usage_received=false for interrupted partial estimate")
	}
	if !attempts[0].ErrorCode.Valid || attempts[0].ErrorCode.String != "stream_adapter_error" {
		t.Fatalf("expected attempt error code stream_adapter_error, got valid=%v value=%q", attempts[0].ErrorCode.Valid, attempts[0].ErrorCode.String)
	}
}

// TestChatSettlementUsesAttemptTimeChannelPrice 验证阶段 15：结算按命中渠道、attempt 起始时刻
// 重查 channel_prices（售价+成本同源）。即便之后新增更贵的价格窗口，结算仍用 attempt 时刻生效的那一行。
func TestChatSettlementUsesAttemptTimeChannelPrice(t *testing.T) {
	deps := newChatSettlementDBDeps(t)
	params := deps.params()

	// 收口 seed 价格窗口并新增一条相邻、更贵的 enabled 价格，模拟“当前生效价格已变更”。
	// effective_from 必须明显晚于 attempt 时间（timestamptz 微秒精度，纳秒偏移会被舍入）。
	priceChangeAt := params.AttemptRecord.StartedAt.Add(time.Minute)
	if _, err := deps.pool.Exec(deps.ctx, `UPDATE channel_prices SET effective_to = $2 WHERE id = $1`, deps.channelPriceID, priceChangeAt); err != nil {
		t.Fatalf("close seed channel price window: %v", err)
	}

	newPrice, err := deps.queries.CreateChannelPrice(deps.ctx, sqlc.CreateChannelPriceParams{
		ChannelID:           deps.channelID,
		ModelID:             deps.modelID,
		Currency:            "USD",
		PricingUnit:         billing.PricingUnitPer1MTokens,
		UncachedInputCost:   testNumeric(50_0000000000, -10),
		OutputCost:          testNumeric(100_0000000000, -10),
		CacheReadInputCost:  testNumeric(25_0000000000, -10),
		ReasoningOutputCost: testNumeric(150_0000000000, -10),
		Status:              "enabled",
		EffectiveFrom:       pgtype.Timestamptz{Time: priceChangeAt, Valid: true},
		EffectiveTo:         pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create replacement channel price: %v", err)
	}

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
	if !snapshot.PriceID.Valid || snapshot.PriceID.Int64 != deps.channelPriceID {
		t.Fatalf("expected attempt-time channel price id %d, got valid=%v value=%d", deps.channelPriceID, snapshot.PriceID.Valid, snapshot.PriceID.Int64)
	}
	if snapshot.PriceID.Int64 == newPrice.ID {
		t.Fatalf("expected settlement not to use replacement price id %d", newPrice.ID)
	}
	// 售价取路由透传的 SalePrice（DEC-026：与渠道价解耦，= 模型基准价 × 线路倍率）。
	assertNumericEqual(t, snapshot.UncachedInputPrice, testNumeric(3_0000000000, -10))
	assertNumericEqual(t, snapshot.OutputPrice, testNumeric(12_0000000000, -10))

	costSnapshot, err := deps.queries.GetCostSnapshotByRequest(deps.ctx, deps.requestRecord.ID)
	if err != nil {
		t.Fatalf("get cost snapshot: %v", err)
	}
	if !costSnapshot.CostPriceID.Valid || costSnapshot.CostPriceID.Int64 != deps.channelPriceID {
		t.Fatalf("expected attempt-time channel price id %d for cost, got valid=%v value=%d", deps.channelPriceID, costSnapshot.CostPriceID.Valid, costSnapshot.CostPriceID.Int64)
	}
	// 成本同样取 attempt 时刻生效的 seed 行。
	assertNumericEqual(t, costSnapshot.UncachedInputCost, testNumeric(1_0000000000, -10))
	assertNumericEqual(t, costSnapshot.OutputCost, testNumeric(4_0000000000, -10))

	if len(billingCalculator.prices) != 1 {
		t.Fatalf("expected one billing price, got %d", len(billingCalculator.prices))
	}
	assertNumericEqual(t, billingCalculator.prices[0].UncachedInputPrice, testNumeric(3_0000000000, -10))
	assertNumericEqual(t, billingCalculator.prices[0].OutputPrice, testNumeric(12_0000000000, -10))
}

// TestChatSettlementPinsCandidateChannelPrice 验证 P1-3：当中标候选透传了 ChannelPriceID 时，结算以该行计费，
// 即使其窗口已被收口、且当前另有一条覆盖 attempt 时刻的更贵 enabled 价（不带 pin 会被 FindActiveChannelPrice 选中）。
func TestChatSettlementPinsCandidateChannelPrice(t *testing.T) {
	deps := newChatSettlementDBDeps(t)
	params := deps.params()
	params.ChannelPriceID = deps.channelPriceID // 中标候选在路由/授权时锁定的价

	// 收口 seed 窗口到 attempt 之前，并新增一条覆盖 attempt 时刻的更贵 enabled 价。
	// 不带 pin 时 FindActiveChannelPrice(attemptStart) 会选这条新价；带 pin 必须仍用 seed 行。
	cutoff := params.AttemptRecord.StartedAt.Add(-time.Minute)
	if _, err := deps.pool.Exec(deps.ctx, `UPDATE channel_prices SET effective_to = $2 WHERE id = $1`, deps.channelPriceID, cutoff); err != nil {
		t.Fatalf("close seed channel price window: %v", err)
	}

	newPrice, err := deps.queries.CreateChannelPrice(deps.ctx, sqlc.CreateChannelPriceParams{
		ChannelID:           deps.channelID,
		ModelID:             deps.modelID,
		Currency:            "USD",
		PricingUnit:         billing.PricingUnitPer1MTokens,
		UncachedInputCost:   testNumeric(50_0000000000, -10),
		OutputCost:          testNumeric(100_0000000000, -10),
		CacheReadInputCost:  testNumeric(25_0000000000, -10),
		ReasoningOutputCost: testNumeric(150_0000000000, -10),
		Status:              "enabled",
		EffectiveFrom:       pgtype.Timestamptz{Time: cutoff, Valid: true},
		EffectiveTo:         pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create replacement channel price: %v", err)
	}

	billingCalculator := chatSettlementBilling(testNumeric(61_000000, -10))
	service := NewChatSettlementService(deps.pool, deps.queries, billingCalculator, &fakeChatLedgerCapturer{})

	if err := service.SettleSuccessfulChat(deps.ctx, params); err != nil {
		t.Fatalf("settle successful chat: %v", err)
	}

	snapshot, err := deps.queries.GetPriceSnapshotByRequest(deps.ctx, deps.requestRecord.ID)
	if err != nil {
		t.Fatalf("get price snapshot: %v", err)
	}
	if !snapshot.PriceID.Valid || snapshot.PriceID.Int64 != deps.channelPriceID {
		t.Fatalf("expected pinned channel price id %d, got valid=%v value=%d", deps.channelPriceID, snapshot.PriceID.Valid, snapshot.PriceID.Int64)
	}
	if snapshot.PriceID.Int64 == newPrice.ID {
		t.Fatalf("settlement must not drift to replacement price id %d when candidate price is pinned", newPrice.ID)
	}
	// 售价取路由透传的 SalePrice（DEC-026：与渠道价/pin 解耦）；PriceID 仍指向 pin 的成本行。
	assertNumericEqual(t, snapshot.UncachedInputPrice, testNumeric(3_0000000000, -10))
	assertNumericEqual(t, snapshot.OutputPrice, testNumeric(12_0000000000, -10))

	costSnapshot, err := deps.queries.GetCostSnapshotByRequest(deps.ctx, deps.requestRecord.ID)
	if err != nil {
		t.Fatalf("get cost snapshot: %v", err)
	}
	if !costSnapshot.CostPriceID.Valid || costSnapshot.CostPriceID.Int64 != deps.channelPriceID {
		t.Fatalf("expected pinned channel price id %d for cost, got valid=%v value=%d", deps.channelPriceID, costSnapshot.CostPriceID.Valid, costSnapshot.CostPriceID.Int64)
	}
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

// TestChatSettlementMultiplierPathComputesAndPinsCost 验证 DEC-027/DEC-031 倍率路径：无绝对覆盖时，结算按
// pin 的 基准价（model_prices）× 价格倍率 × 充值倍率 算真实成本并冻结进 cost_snapshots，cost_price_id / price_snapshots.price_id 为 NULL，
// 且成本来源 id + 倍率标量落库，供审计与「改倍率不漂移」。
func TestChatSettlementMultiplierPathComputesAndPinsCost(t *testing.T) {
	deps := newChatSettlementDBDeps(t)

	// DEC-031：成本基数复用 model_prices（退役独立参考成本表）。基准价：未缓存 2.0 / 输出 10.0 / 缓存读取 0.2 / reasoning 10.0（名义 USD）。
	basePrice, err := deps.queries.CreateModelPrice(deps.ctx, sqlc.CreateModelPriceParams{
		ModelID:              deps.modelID,
		Currency:             "USD",
		PricingUnit:          billing.PricingUnitPer1MTokens,
		UncachedInputPrice:   testNumeric(2_0000000000, -10),
		CacheReadInputPrice:  testNumeric(2000000000, -10), // 0.2
		OutputPrice:          testNumeric(10_0000000000, -10),
		ReasoningOutputPrice: testNumeric(10_0000000000, -10),
		Status:               "enabled",
		EffectiveFrom:        pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
		EffectiveTo:          pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create model price (cost base): %v", err)
	}
	// 渠道默认价格倍率 1.2。
	mult, err := deps.queries.CreateChannelCostMultiplier(deps.ctx, sqlc.CreateChannelCostMultiplierParams{
		ChannelID:     deps.channelID,
		ModelID:       pgtype.Int8{Valid: false},
		Multiplier:    testNumeric(12, -1), // 1.2
		Status:        "enabled",
		EffectiveFrom: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
		EffectiveTo:   pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create channel cost multiplier: %v", err)
	}
	// 渠道充值倍率 0.5。
	recharge, err := deps.queries.CreateChannelRechargeFactor(deps.ctx, sqlc.CreateChannelRechargeFactorParams{
		ChannelID:     deps.channelID,
		Factor:        testNumeric(5, -1), // 0.5
		Status:        "enabled",
		EffectiveFrom: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
		EffectiveTo:   pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create channel recharge factor: %v", err)
	}

	billingCalculator := chatSettlementBilling(testNumeric(61_000000, -10))
	ledgerService := ledger.NewService(deps.pool, deps.queries)
	service := NewChatSettlementService(deps.pool, deps.queries, billingCalculator, ledgerService)

	// 走倍率路径：无绝对覆盖 pin，带上三个来源 pin。
	params := deps.params()
	params.ChannelPriceID = 0
	params.CostBaseModelPriceID = basePrice.ID
	params.ChannelCostMultiplierID = mult.ID
	params.ChannelRechargeFactorID = recharge.ID

	if err := service.SettleSuccessfulChat(deps.ctx, params); err != nil {
		t.Fatalf("settle successful chat (multiplier path): %v", err)
	}

	costSnapshot, err := deps.queries.GetCostSnapshotByRequest(deps.ctx, deps.requestRecord.ID)
	if err != nil {
		t.Fatalf("get cost snapshot: %v", err)
	}
	// 倍率路径：cost_price_id 为 NULL，成本来源 id + 倍率标量置位。
	if costSnapshot.CostPriceID.Valid {
		t.Fatalf("expected NULL cost_price_id on multiplier path, got %d", costSnapshot.CostPriceID.Int64)
	}
	if !costSnapshot.CostBaseModelPriceID.Valid || costSnapshot.CostBaseModelPriceID.Int64 != basePrice.ID {
		t.Fatalf("expected cost_base_model_price_id %d, got valid=%v value=%d", basePrice.ID, costSnapshot.CostBaseModelPriceID.Valid, costSnapshot.CostBaseModelPriceID.Int64)
	}
	if !costSnapshot.ChannelCostMultiplierID.Valid || costSnapshot.ChannelCostMultiplierID.Int64 != mult.ID {
		t.Fatalf("expected channel_cost_multiplier_id %d, got valid=%v value=%d", mult.ID, costSnapshot.ChannelCostMultiplierID.Valid, costSnapshot.ChannelCostMultiplierID.Int64)
	}
	if !costSnapshot.ChannelRechargeFactorID.Valid || costSnapshot.ChannelRechargeFactorID.Int64 != recharge.ID {
		t.Fatalf("expected channel_recharge_factor_id %d, got valid=%v value=%d", recharge.ID, costSnapshot.ChannelRechargeFactorID.Valid, costSnapshot.ChannelRechargeFactorID.Int64)
	}
	assertNumericEqual(t, costSnapshot.CostMultiplier, testNumeric(12, -1)) // 1.2
	assertNumericEqual(t, costSnapshot.RechargeFactor, testNumeric(5, -1))  // 0.5

	// 真实成本单价 = 基准价（model_prices） × 1.2 × 0.5（= ×0.6）。
	assertNumericEqual(t, costSnapshot.UncachedInputCost, testNumeric(1_2000000000, -10)) // 2.0 × 0.6
	assertNumericEqual(t, costSnapshot.OutputCost, testNumeric(6_0000000000, -10))        // 10.0 × 0.6
	assertNumericEqual(t, costSnapshot.CacheReadInputCost, testNumeric(1200000000, -10))  // 0.2 × 0.6 = 0.12
	assertNumericEqual(t, costSnapshot.ReasoningOutputCost, testNumeric(6_0000000000, -10))

	// 售价快照 price_id 倍率路径为 NULL（无 channel_prices 行可指）。
	priceSnapshot, err := deps.queries.GetPriceSnapshotByRequest(deps.ctx, deps.requestRecord.ID)
	if err != nil {
		t.Fatalf("get price snapshot: %v", err)
	}
	if priceSnapshot.PriceID.Valid {
		t.Fatalf("expected NULL price_snapshots.price_id on multiplier path, got %d", priceSnapshot.PriceID.Int64)
	}

	// 传入 billing 的成本快照即为缩放后单价（路由/结算同一套派生）。
	if len(billingCalculator.costs) != 1 {
		t.Fatalf("expected one provider cost calculation, got %d", len(billingCalculator.costs))
	}
	assertNumericEqual(t, billingCalculator.costs[0].UncachedInputCost, testNumeric(1_2000000000, -10))
	assertNumericEqual(t, billingCalculator.costs[0].OutputCost, testNumeric(6_0000000000, -10))
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
	// 改动不可变 usage 事实以触发幂等冲突：输出总量由 5 改为 6。
	replayed.Facts.Usage.OutputTokensTotal = coreusage.KnownTokens(6)
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
		SET uncached_input_cost_amount = uncached_input_cost_amount + 0.000001,
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

// usageRecordRowFromFacts 把不可变响应事实映射为持久化 usage 行，镜像 settlement 写入列，
// 供 ensureSettlementUsageMatches 的纯函数单测构造匹配/不匹配场景。
func usageRecordRowFromFacts(facts adapter.ResponseFacts) sqlc.UsageRecord {
	u := facts.Usage
	return sqlc.UsageRecord{
		UncachedInputTokens:           u.UncachedInputTokens.Value,
		UncachedInputTokensState:      string(u.UncachedInputTokens.State),
		CacheReadInputTokens:          u.CacheReadInputTokens.Value,
		CacheReadInputTokensState:     string(u.CacheReadInputTokens.State),
		CacheWrite5mInputTokens:       u.CacheWrite5mInputTokens.Value,
		CacheWrite5mInputTokensState:  string(u.CacheWrite5mInputTokens.State),
		CacheWrite1hInputTokens:       u.CacheWrite1hInputTokens.Value,
		CacheWrite1hInputTokensState:  string(u.CacheWrite1hInputTokens.State),
		CacheWrite30mInputTokens:      u.CacheWrite30mInputTokens.Value,
		CacheWrite30mInputTokensState: string(u.CacheWrite30mInputTokens.State),
		OutputTokensTotal:             u.OutputTokensTotal.Value,
		OutputTokensTotalState:        string(u.OutputTokensTotal.State),
		ReasoningOutputTokens:         u.ReasoningOutputTokens.Value,
		ReasoningOutputTokensState:    string(u.ReasoningOutputTokens.State),
		UsageSource:                   string(facts.UsageSource),
		UsageMappingVersion:           facts.UsageMappingVersion,
	}
}

func TestEnsureSettlementUsageMatchesAcceptsStreamSource(t *testing.T) {
	facts := chatSettlementFacts(coreusage.SourceUpstreamStream)
	row := usageRecordRowFromFacts(facts)

	if err := ensureSettlementUsageMatches(row, facts); err != nil {
		t.Fatalf("expected stream usage source to match: %v", err)
	}
}

func TestEnsureSettlementUsageMatchesRejectsDifferentSource(t *testing.T) {
	// row 按 upstream_response 持久化，facts 重放声明 upstream_stream，必须判为幂等冲突。
	row := usageRecordRowFromFacts(chatSettlementFacts(coreusage.SourceUpstreamResponse))
	facts := chatSettlementFacts(coreusage.SourceUpstreamStream)

	err := ensureSettlementUsageMatches(row, facts)
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
