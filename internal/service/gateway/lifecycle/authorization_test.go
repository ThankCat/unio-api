package lifecycle

import (
	"context"
	"math/big"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/core/ledger"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
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
	preAuthorizeParams ledger.PreAuthorizeParams
	reservation        ledger.Reservation
	err                error
}

func (l *chatAuthorizationLedger) PreAuthorize(ctx context.Context, params ledger.PreAuthorizeParams) (ledger.Reservation, error) {
	l.preAuthorizeParams = params
	return l.reservation, l.err
}

func (l *chatAuthorizationLedger) Release(ctx context.Context, params ledger.ReleaseParams) (ledger.Reservation, error) {
	return ledger.Reservation{}, nil
}

func (l *chatAuthorizationLedger) ReleaseWithBillingException(ctx context.Context, params ledger.ReleaseWithBillingExceptionParams) (ledger.Reservation, error) {
	return ledger.Reservation{}, nil
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
	service := NewChatAuthorizationService(billingService, ledgerService)

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

// TestChatAuthorizationRequiresCandidatePrices 验证无候选售价时拒绝冻结。
func TestChatAuthorizationRequiresCandidatePrices(t *testing.T) {
	service := NewChatAuthorizationService(&chatAuthorizationBilling{}, &chatAuthorizationLedger{})
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
