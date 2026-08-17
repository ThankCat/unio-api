package lifecycle

import (
	"context"
	"math/big"
	"reflect"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/ledger"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/jackc/pgx/v5/pgtype"
)

// chatAuthorizationBilling 用 price.OutputPrice 作为估算金额替身，便于断言「冻结取候选池最贵」。
type chatAuthorizationBilling struct {
	estimate billing.AuthorizationEstimate
	calls    int
}

func (b *chatAuthorizationBilling) EstimateAuthorizationAmount(estimate billing.AuthorizationEstimate, price billing.CustomerPriceSnapshot) (billing.CustomerCharge, error) {
	b.estimate = estimate
	b.calls++
	return billing.CustomerCharge{Amount: price.OutputPrice, Currency: "USD", FormulaVersion: billing.FormulaVersionV1}, nil
}

type chatAuthorizationLedger struct {
	preAuthorizeParams                 ledger.PreAuthorizeParams
	createRequestAndPreAuthorizeParams ledger.CreateRequestAndPreAuthorizeParams
	reservation                        ledger.Reservation
	authorizedRequest                  ledger.AuthorizedRequest
	err                                error
}

func (l *chatAuthorizationLedger) PreAuthorize(ctx context.Context, params ledger.PreAuthorizeParams) (ledger.Reservation, error) {
	l.preAuthorizeParams = params
	return l.reservation, l.err
}

func (l *chatAuthorizationLedger) CreateRequestAndPreAuthorize(_ context.Context, params ledger.CreateRequestAndPreAuthorizeParams) (ledger.AuthorizedRequest, error) {
	l.createRequestAndPreAuthorizeParams = params
	return l.authorizedRequest, l.err
}

func (l *chatAuthorizationLedger) Release(ctx context.Context, params ledger.ReleaseParams) (ledger.Reservation, error) {
	return ledger.Reservation{}, nil
}

func (l *chatAuthorizationLedger) ReleaseWithBillingException(ctx context.Context, params ledger.ReleaseWithBillingExceptionParams) (ledger.Reservation, error) {
	return ledger.Reservation{}, nil
}

func TestChatAuthorizationAuthorizesNewRequest(t *testing.T) {
	price := billing.CustomerPriceSnapshot{
		Currency:           "USD",
		PricingUnit:        billing.PricingUnitPer1MTokens,
		UncachedInputPrice: gatewayTestNumeric(1, 0),
		OutputPrice:        gatewayTestNumeric(12, 0),
		FormulaVersion:     billing.FormulaVersionV1,
	}
	requestParams := requestlog.CreateRequestParams{
		RequestID:        "req_atomic_authorization",
		UserID:           12,
		APIKeyID:         34,
		RequestedModelID: "gpt-test",
		IngressProtocol:  requestlog.ProtocolOpenAI,
		Endpoint:         requestlog.EndpointChatCompletions,
		Stream:           true,
	}
	requestRecord := requestlog.RequestRecord{
		ID:               44,
		RequestID:        requestParams.RequestID,
		UserID:           requestParams.UserID,
		APIKeyID:         requestParams.APIKeyID,
		RequestedModelID: requestParams.RequestedModelID,
		IngressProtocol:  requestParams.IngressProtocol,
		Endpoint:         requestParams.Endpoint,
		Stream:           requestParams.Stream,
		Status:           requestlog.RequestStatusRunning,
	}
	reservation := ledger.Reservation{
		ID:               7001,
		UserID:           requestParams.UserID,
		RequestRecordID:  requestRecord.ID,
		Currency:         "USD",
		EstimatedAmount:  gatewayTestNumeric(12, 0),
		AuthorizedAmount: gatewayTestNumeric(7, 0),
	}
	ledgerService := &chatAuthorizationLedger{
		authorizedRequest: ledger.AuthorizedRequest{
			Request:     requestRecord,
			Reservation: reservation,
		},
	}
	service := NewChatAuthorizationService(&chatAuthorizationBilling{}, ledgerService, 0)

	result, err := service.AuthorizeNewChat(context.Background(), ChatAuthorizeNewRequestParams{
		Request:             requestParams,
		CandidatePrices:     []billing.CustomerPriceSnapshot{price},
		InputTokens:         321,
		MaxCompletionTokens: 128,
	})
	if err != nil {
		t.Fatalf("AuthorizeNewChat returned error: %v", err)
	}

	gotParams := ledgerService.createRequestAndPreAuthorizeParams
	if !reflect.DeepEqual(gotParams.Request, requestParams) {
		t.Fatalf("request params mismatch: got %#v, want %#v", gotParams.Request, requestParams)
	}
	if !chatSettlementSameNumeric(gotParams.EstimatedAmount, gatewayTestNumeric(12, 0)) || gotParams.Currency != "USD" {
		t.Fatalf("unexpected authorization amount: %#v", gotParams)
	}
	if gotParams.IdempotencyKeyPrefix != "chat:authorize:" || gotParams.Reason != "chat completion authorization" {
		t.Fatalf("unexpected ledger metadata: %#v", gotParams)
	}
	if !reflect.DeepEqual(result.RequestRecord, requestRecord) {
		t.Fatalf("request record mismatch: got %#v, want %#v", result.RequestRecord, requestRecord)
	}
	if result.Authorization.ReservationID != reservation.ID ||
		result.Authorization.RequestRecordID != reservation.RequestRecordID ||
		result.Authorization.Currency != reservation.Currency ||
		!chatSettlementSameNumeric(result.Authorization.EstimatedAmount, reservation.EstimatedAmount) ||
		!chatSettlementSameNumeric(result.Authorization.AuthorizedAmount, reservation.AuthorizedAmount) {
		t.Fatalf("authorization mismatch: got %#v, reservation %#v", result.Authorization, reservation)
	}
}

// TestChatAuthorizationFreezesOnMostExpensiveCandidate 验证阶段 15：渠道未定时按候选池里
// 「按本次 token 估算最贵」的一条售价冻结，确保命中任一候选都不超扣。
func TestChatAuthorizationFreezesOnMostExpensiveCandidate(t *testing.T) {
	cheap := billing.CustomerPriceSnapshot{
		Currency:           "USD",
		PricingUnit:        billing.PricingUnitPer1MTokens,
		UncachedInputPrice: gatewayTestNumeric(1, 0),
		OutputPrice:        gatewayTestNumeric(5, 0),
		FormulaVersion:     billing.FormulaVersionV1,
	}
	pricey := billing.CustomerPriceSnapshot{
		Currency:           "USD",
		PricingUnit:        billing.PricingUnitPer1MTokens,
		UncachedInputPrice: gatewayTestNumeric(2, 0),
		OutputPrice:        gatewayTestNumeric(12, 0),
		FormulaVersion:     billing.FormulaVersionV1,
	}

	billingService := &chatAuthorizationBilling{}
	ledgerService := &chatAuthorizationLedger{
		reservation: ledger.Reservation{
			ID:               7001,
			RequestRecordID:  44,
			Currency:         "USD",
			EstimatedAmount:  gatewayTestNumeric(12, 0),
			AuthorizedAmount: gatewayTestNumeric(12, 0),
		},
	}
	service := NewChatAuthorizationService(billingService, ledgerService, 0)

	authorization, err := service.AuthorizeChat(context.Background(), ChatAuthorizeParams{
		RequestRecord:       requestlog.RequestRecord{ID: 44},
		Principal:           &auth.APIKeyPrincipal{UserID: 12},
		CandidatePrices:     []billing.CustomerPriceSnapshot{cheap, pricey},
		InputTokens:         321,
		MaxCompletionTokens: 128,
	})
	if err != nil {
		t.Fatalf("AuthorizeChat returned error: %v", err)
	}

	if billingService.calls != 2 {
		t.Fatalf("expected estimate over each candidate (2 calls), got %d", billingService.calls)
	}
	if billingService.estimate.InputTokens != 321 || billingService.estimate.MaxCompletionTokens != 128 {
		t.Fatalf("unexpected estimate: %#v", billingService.estimate)
	}
	// 冻结额取候选池最贵 = 12。
	if !chatSettlementSameNumeric(ledgerService.preAuthorizeParams.EstimatedAmount, gatewayTestNumeric(12, 0)) {
		t.Fatalf("expected freeze on most expensive candidate (12), got %#v", ledgerService.preAuthorizeParams.EstimatedAmount)
	}
	if ledgerService.preAuthorizeParams.UserID != 12 || ledgerService.preAuthorizeParams.RequestRecordID != 44 {
		t.Fatalf("unexpected ledger preauthorize params: %#v", ledgerService.preAuthorizeParams)
	}
	if authorization.ReservationID != 7001 {
		t.Fatalf("unexpected authorization: %#v", authorization)
	}
}

// TestChatAuthorizationUsesCandidateMaxOutputTokensWhenClientOmits 验证 P0-2：客户未给出输出上限时，
// 冻结估算改用候选模型 max_output_tokens（取候选最大值），而非进程级偏小兜底。
func TestChatAuthorizationUsesCandidateMaxOutputTokensWhenClientOmits(t *testing.T) {
	price := billing.CustomerPriceSnapshot{
		Currency:           "USD",
		PricingUnit:        billing.PricingUnitPer1MTokens,
		UncachedInputPrice: gatewayTestNumeric(1, 0),
		OutputPrice:        gatewayTestNumeric(5, 0),
		FormulaVersion:     billing.FormulaVersionV1,
	}

	billingService := &chatAuthorizationBilling{}
	ledgerService := &chatAuthorizationLedger{reservation: ledger.Reservation{ID: 1, RequestRecordID: 9, Currency: "USD"}}
	// 进程级兜底设很小(100)，确认未被用到（候选上限 32000 优先）。
	service := NewChatAuthorizationService(billingService, ledgerService, 100)

	_, err := service.AuthorizeChat(context.Background(), ChatAuthorizeParams{
		RequestRecord:            requestlog.RequestRecord{ID: 9},
		Principal:                &auth.APIKeyPrincipal{UserID: 3},
		CandidatePrices:          []billing.CustomerPriceSnapshot{price},
		InputTokens:              10,
		MaxCompletionTokens:      0,
		CandidateMaxOutputTokens: 32000,
	})
	if err != nil {
		t.Fatalf("AuthorizeChat returned error: %v", err)
	}
	if billingService.estimate.MaxCompletionTokens != 32000 {
		t.Fatalf("expected estimate to use candidate max output tokens 32000, got %d", billingService.estimate.MaxCompletionTokens)
	}
}

// TestChatAuthorizationFallsBackToProcessDefaultWhenAllOmit 验证 P0-2：客户与候选均未给出输出上限时，
// 回退进程级 maxOutputTokensFallback。
func TestChatAuthorizationFallsBackToProcessDefaultWhenAllOmit(t *testing.T) {
	price := billing.CustomerPriceSnapshot{
		Currency:           "USD",
		PricingUnit:        billing.PricingUnitPer1MTokens,
		UncachedInputPrice: gatewayTestNumeric(1, 0),
		OutputPrice:        gatewayTestNumeric(5, 0),
		FormulaVersion:     billing.FormulaVersionV1,
	}

	billingService := &chatAuthorizationBilling{}
	ledgerService := &chatAuthorizationLedger{reservation: ledger.Reservation{ID: 1, RequestRecordID: 9, Currency: "USD"}}
	service := NewChatAuthorizationService(billingService, ledgerService, 8192)

	_, err := service.AuthorizeChat(context.Background(), ChatAuthorizeParams{
		RequestRecord:            requestlog.RequestRecord{ID: 9},
		Principal:                &auth.APIKeyPrincipal{UserID: 3},
		CandidatePrices:          []billing.CustomerPriceSnapshot{price},
		InputTokens:              10,
		MaxCompletionTokens:      0,
		CandidateMaxOutputTokens: 0,
	})
	if err != nil {
		t.Fatalf("AuthorizeChat returned error: %v", err)
	}
	if billingService.estimate.MaxCompletionTokens != 8192 {
		t.Fatalf("expected estimate to fall back to process default 8192, got %d", billingService.estimate.MaxCompletionTokens)
	}
}

// TestChatAuthorizationPrefersClientMaxOutputTokens 验证 P0-2：客户显式给出输出上限时优先生效。
func TestChatAuthorizationPrefersClientMaxOutputTokens(t *testing.T) {
	price := billing.CustomerPriceSnapshot{
		Currency:           "USD",
		PricingUnit:        billing.PricingUnitPer1MTokens,
		UncachedInputPrice: gatewayTestNumeric(1, 0),
		OutputPrice:        gatewayTestNumeric(5, 0),
		FormulaVersion:     billing.FormulaVersionV1,
	}

	billingService := &chatAuthorizationBilling{}
	ledgerService := &chatAuthorizationLedger{reservation: ledger.Reservation{ID: 1, RequestRecordID: 9, Currency: "USD"}}
	service := NewChatAuthorizationService(billingService, ledgerService, 8192)

	_, err := service.AuthorizeChat(context.Background(), ChatAuthorizeParams{
		RequestRecord:            requestlog.RequestRecord{ID: 9},
		Principal:                &auth.APIKeyPrincipal{UserID: 3},
		CandidatePrices:          []billing.CustomerPriceSnapshot{price},
		InputTokens:              10,
		MaxCompletionTokens:      256,
		CandidateMaxOutputTokens: 32000,
	})
	if err != nil {
		t.Fatalf("AuthorizeChat returned error: %v", err)
	}
	if billingService.estimate.MaxCompletionTokens != 256 {
		t.Fatalf("expected estimate to prefer client max output tokens 256, got %d", billingService.estimate.MaxCompletionTokens)
	}
}

// TestChatAuthorizationCoversLongContextPriceBelowThreshold 验证本地输入估算尚未过门槛时，
// 授权仍会覆盖可能由上游真实 usage 触发的长上下文价格。
func TestChatAuthorizationCoversLongContextPriceBelowThreshold(t *testing.T) {
	price := billing.CustomerPriceSnapshot{
		Currency:           "USD",
		PricingUnit:        billing.PricingUnitPer1MTokens,
		UncachedInputPrice: gatewayTestNumeric(1, 0),
		OutputPrice:        gatewayTestNumeric(5, 0),
		FormulaVersion:     billing.FormulaVersionV1,
	}
	billingService := &chatAuthorizationBilling{}
	ledgerService := &chatAuthorizationLedger{reservation: ledger.Reservation{ID: 1, RequestRecordID: 9, Currency: "USD"}}
	service := NewChatAuthorizationService(billingService, ledgerService, 0)

	_, err := service.AuthorizeChat(context.Background(), ChatAuthorizeParams{
		RequestRecord:       requestlog.RequestRecord{ID: 9},
		Principal:           &auth.APIKeyPrincipal{UserID: 3},
		CandidatePrices:     []billing.CustomerPriceSnapshot{price},
		InputTokens:         90,
		MaxCompletionTokens: 10,
		LongContextPolicy: billing.LongContextPolicy{
			Enabled:          true,
			Threshold:        100,
			InputMultiplier:  gatewayTestNumeric(2, 0),
			OutputMultiplier: gatewayTestNumeric(3, 0),
		},
	})
	if err != nil {
		t.Fatalf("AuthorizeChat returned error: %v", err)
	}

	if billingService.calls != 2 {
		t.Fatalf("expected standard and long-context estimates, got %d calls", billingService.calls)
	}
	if !chatSettlementSameNumeric(ledgerService.preAuthorizeParams.EstimatedAmount, gatewayTestNumeric(15, 0)) {
		t.Fatalf("expected long-context authorization amount 15, got %#v", ledgerService.preAuthorizeParams.EstimatedAmount)
	}
}

// TestChatAuthorizationKeepsHigherStandardPrice 验证配置中的长上下文倍率小于 1 时，
// 授权仍取普通价和阶梯价中较高的一项，不会因阶梯配置而减少冻结。
func TestChatAuthorizationKeepsHigherStandardPrice(t *testing.T) {
	price := billing.CustomerPriceSnapshot{
		Currency:           "USD",
		PricingUnit:        billing.PricingUnitPer1MTokens,
		UncachedInputPrice: gatewayTestNumeric(1, 0),
		OutputPrice:        gatewayTestNumeric(5, 0),
		FormulaVersion:     billing.FormulaVersionV1,
	}
	billingService := &chatAuthorizationBilling{}
	ledgerService := &chatAuthorizationLedger{reservation: ledger.Reservation{ID: 1, RequestRecordID: 9, Currency: "USD"}}
	service := NewChatAuthorizationService(billingService, ledgerService, 0)

	_, err := service.AuthorizeChat(context.Background(), ChatAuthorizeParams{
		RequestRecord:       requestlog.RequestRecord{ID: 9},
		Principal:           &auth.APIKeyPrincipal{UserID: 3},
		CandidatePrices:     []billing.CustomerPriceSnapshot{price},
		InputTokens:         90,
		MaxCompletionTokens: 10,
		LongContextPolicy: billing.LongContextPolicy{
			Enabled:          true,
			Threshold:        100,
			InputMultiplier:  gatewayTestNumeric(5, -1),
			OutputMultiplier: gatewayTestNumeric(5, -1),
		},
	})
	if err != nil {
		t.Fatalf("AuthorizeChat returned error: %v", err)
	}

	if !chatSettlementSameNumeric(ledgerService.preAuthorizeParams.EstimatedAmount, gatewayTestNumeric(5, 0)) {
		t.Fatalf("expected standard authorization amount 5, got %#v", ledgerService.preAuthorizeParams.EstimatedAmount)
	}
}

// TestChatAuthorizationRequiresCandidatePrices 验证无候选售价时拒绝冻结。
func TestChatAuthorizationRequiresCandidatePrices(t *testing.T) {
	service := NewChatAuthorizationService(&chatAuthorizationBilling{}, &chatAuthorizationLedger{}, 0)
	_, err := service.AuthorizeChat(context.Background(), ChatAuthorizeParams{
		RequestRecord: requestlog.RequestRecord{ID: 1},
		Principal:     &auth.APIKeyPrincipal{UserID: 1},
	})
	if err == nil {
		t.Fatal("expected error when no candidate prices are provided")
	}
}

func gatewayTestNumeric(value int64, exp int32) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(value), Exp: exp, Valid: true}
}
