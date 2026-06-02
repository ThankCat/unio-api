package lifecycle

import (
	"context"
	"math/big"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/core/ledger"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

type chatAuthorizationPriceStore struct {
	price  sqlc.Price
	params []sqlc.FindActivePriceForModelParams
	err    error
}

func (s *chatAuthorizationPriceStore) FindActivePriceForModel(ctx context.Context, arg sqlc.FindActivePriceForModelParams) (sqlc.Price, error) {
	s.params = append(s.params, arg)
	return s.price, s.err
}

type chatAuthorizationBilling struct {
	estimate billing.AuthorizationEstimate
	price    billing.CustomerPriceSnapshot
	charge   billing.CustomerCharge
	err      error
}

func (b *chatAuthorizationBilling) EstimateAuthorizationAmount(estimate billing.AuthorizationEstimate, price billing.CustomerPriceSnapshot) (billing.CustomerCharge, error) {
	b.estimate = estimate
	b.price = price
	return b.charge, b.err
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

func TestChatAuthorizationUsesConservativeInputEstimate(t *testing.T) {
	amount := gatewayTestNumeric(12345, -4)
	priceStore := &chatAuthorizationPriceStore{price: sqlc.Price{
		ID:          99,
		Currency:    "USD",
		PricingUnit: billing.PricingUnitPer1MTokens,
	}}
	billingService := &chatAuthorizationBilling{
		charge: billing.CustomerCharge{Amount: amount, Currency: "USD", FormulaVersion: billing.FormulaVersionV1},
	}
	ledgerService := &chatAuthorizationLedger{
		reservation: ledger.Reservation{
			ID:               7001,
			RequestRecordID:  44,
			Currency:         "USD",
			EstimatedAmount:  amount,
			AuthorizedAmount: amount,
		},
	}
	service := NewChatAuthorizationService(priceStore, billingService, ledgerService)

	authorization, err := service.AuthorizeChat(context.Background(), ChatAuthorizeParams{
		RequestRecord:       requestlog.RequestRecord{ID: 44},
		Principal:           &auth.APIKeyPrincipal{UserID: 12},
		ModelDBID:           55,
		InputTokens:         321,
		MaxCompletionTokens: 128,
	})
	if err != nil {
		t.Fatalf("AuthorizeChat returned error: %v", err)
	}

	if billingService.estimate.InputTokens != 321 {
		t.Fatalf("expected input token estimate %d, got %d", 321, billingService.estimate.InputTokens)
	}
	if billingService.estimate.MaxCompletionTokens != 128 {
		t.Fatalf("expected max completion tokens %d, got %d", 128, billingService.estimate.MaxCompletionTokens)
	}
	if ledgerService.preAuthorizeParams.UserID != 12 || ledgerService.preAuthorizeParams.RequestRecordID != 44 {
		t.Fatalf("unexpected ledger preauthorize params: %#v", ledgerService.preAuthorizeParams)
	}
	if authorization.ReservationID != 7001 || authorization.PriceID != 99 {
		t.Fatalf("unexpected authorization: %#v", authorization)
	}
}

func gatewayTestNumeric(value int64, exp int32) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(value), Exp: exp, Valid: true}
}
