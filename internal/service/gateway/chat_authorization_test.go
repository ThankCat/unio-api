package gateway

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ThankCat/unio-api/internal/app/gatewayapi"
	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/core/ledger"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/platform/failure"
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

type chatAuthorizationRegistry struct {
	tokenizerKey string
	tokenizer    adapter.ChatInputTokenizer
	ok           bool
}

func (r *chatAuthorizationRegistry) Chat(adapterKey string) (adapter.ChatAdapter, bool) {
	return nil, false
}

func (r *chatAuthorizationRegistry) StreamChat(adapterKey string) (adapter.StreamChatAdapter, bool) {
	return nil, false
}

func (r *chatAuthorizationRegistry) ChatInputTokenizer(adapterKey string) (adapter.ChatInputTokenizer, bool) {
	r.tokenizerKey = adapterKey
	return r.tokenizer, r.ok
}

type chatAuthorizationTokenizer struct {
	req    adapter.ChatInputTokenizeRequest
	tokens int64
	err    error
}

func (t *chatAuthorizationTokenizer) CountChatInputTokens(req adapter.ChatInputTokenizeRequest) (int64, error) {
	t.req = req
	return t.tokens, t.err
}

func TestChatAuthorizationUsesAdapterInputTokenizer(t *testing.T) {
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
	tokenizer := &chatAuthorizationTokenizer{tokens: 321}
	registry := &chatAuthorizationRegistry{tokenizer: tokenizer, ok: true}
	service := NewChatAuthorizationService(priceStore, billingService, ledgerService, registry)

	maxTokens := 128
	authorization, err := service.AuthorizeChat(context.Background(), ChatAuthorizeParams{
		RequestRecord: requestlog.RequestRecord{ID: 44},
		Principal:     &auth.APIKeyPrincipal{UserID: 12},
		Request: gatewayapi.ChatCompletionRequest{
			Model: "openai/gpt-4.1",
			Messages: []gatewayapi.ChatMessage{
				{Role: "system", Content: jsonContent("Be concise.")},
				{Role: "user", Content: jsonContent("Hello")},
			},
			MaxTokens: &maxTokens,
		},
		ModelDBID:     55,
		AdapterKey:    "openai",
		UpstreamModel: "gpt-4.1",
	})
	if err != nil {
		t.Fatalf("AuthorizeChat returned error: %v", err)
	}

	if registry.tokenizerKey != "openai" {
		t.Fatalf("expected tokenizer key %q, got %q", "openai", registry.tokenizerKey)
	}
	if tokenizer.req.Model != "gpt-4.1" {
		t.Fatalf("expected tokenizer model %q, got %q", "gpt-4.1", tokenizer.req.Model)
	}
	if len(tokenizer.req.Messages) != 2 || tokenizer.req.Messages[1].ContentString() != "Hello" {
		t.Fatalf("unexpected tokenizer messages: %#v", tokenizer.req.Messages)
	}
	if billingService.estimate.PromptTokens != 321 {
		t.Fatalf("expected prompt token estimate %d, got %d", 321, billingService.estimate.PromptTokens)
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

func TestChatAuthorizationFailsWhenTokenizerIsMissing(t *testing.T) {
	priceStore := &chatAuthorizationPriceStore{price: sqlc.Price{ID: 99, Currency: "USD", PricingUnit: billing.PricingUnitPer1MTokens}}
	service := NewChatAuthorizationService(
		priceStore,
		&chatAuthorizationBilling{},
		&chatAuthorizationLedger{},
		&chatAuthorizationRegistry{ok: false},
	)

	_, err := service.AuthorizeChat(context.Background(), ChatAuthorizeParams{
		RequestRecord: requestlog.RequestRecord{ID: 44},
		Principal:     &auth.APIKeyPrincipal{UserID: 12},
		Request: gatewayapi.ChatCompletionRequest{
			Model:    "openai/gpt-4.1",
			Messages: []gatewayapi.ChatMessage{{Role: "user", Content: jsonContent("Hello")}},
		},
		ModelDBID:     55,
		AdapterKey:    "openai",
		UpstreamModel: "gpt-4.1",
	})
	if failure.CodeOf(err) != failure.CodeGatewayChatAuthorizationFailed {
		t.Fatalf("expected failure code %q, got %q", failure.CodeGatewayChatAuthorizationFailed, failure.CodeOf(err))
	}
}

func TestChatAuthorizationWrapsTokenizerFailure(t *testing.T) {
	tokenizeErr := errors.New("tokenizer failed")
	priceStore := &chatAuthorizationPriceStore{price: sqlc.Price{ID: 99, Currency: "USD", PricingUnit: billing.PricingUnitPer1MTokens}}
	service := NewChatAuthorizationService(
		priceStore,
		&chatAuthorizationBilling{},
		&chatAuthorizationLedger{},
		&chatAuthorizationRegistry{tokenizer: &chatAuthorizationTokenizer{err: tokenizeErr}, ok: true},
	)

	_, err := service.AuthorizeChat(context.Background(), ChatAuthorizeParams{
		RequestRecord: requestlog.RequestRecord{ID: 44},
		Principal:     &auth.APIKeyPrincipal{UserID: 12},
		Request: gatewayapi.ChatCompletionRequest{
			Model:    "openai/gpt-4.1",
			Messages: []gatewayapi.ChatMessage{{Role: "user", Content: jsonContent("Hello")}},
		},
		ModelDBID:     55,
		AdapterKey:    "openai",
		UpstreamModel: "gpt-4.1",
	})
	if failure.CodeOf(err) != failure.CodeGatewayChatAuthorizationFailed {
		t.Fatalf("expected failure code %q, got %q", failure.CodeGatewayChatAuthorizationFailed, failure.CodeOf(err))
	}
	if !errors.Is(err, tokenizeErr) {
		t.Fatalf("expected wrapped tokenizer error, got %v", err)
	}
}

func gatewayTestNumeric(value int64, exp int32) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(value), Exp: exp, Valid: true}
}
