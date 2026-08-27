package adminapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/admin/customer"
)

type fakeUserService struct {
	list       []customer.User
	detail     customer.UserDetail
	getErr     error
	rateLimits customer.RateLimitsInput
}

func (f *fakeUserService) List(context.Context, customer.UserListParams) ([]customer.User, int64, error) {
	return f.list, int64(len(f.list)), nil
}

func (f *fakeUserService) Get(context.Context, int64) (customer.UserDetail, error) {
	return f.detail, f.getErr
}

func (f *fakeUserService) SetRateLimits(_ context.Context, in customer.RateLimitsInput) (customer.User, error) {
	f.rateLimits = in
	return customer.User{
		ID:               in.UserID,
		RPMLimit:         in.RPMLimit,
		RPDLimit:         in.RPDLimit,
		ConcurrencyLimit: in.ConcurrencyLimit,
	}, nil
}

type fakeAPIKeyService struct {
	list    []customer.APIKey
	created customer.CreatedAPIKey
	updated customer.APIKey
	revoked customer.APIKey
}

func (f *fakeAPIKeyService) List(context.Context, customer.APIKeyListParams) ([]customer.APIKey, int64, error) {
	return f.list, int64(len(f.list)), nil
}
func (f *fakeAPIKeyService) Get(context.Context, int64) (customer.APIKey, error) {
	return customer.APIKey{ID: 1, KeyPrefix: "sk-unio-a3f9k2m1", Status: "active", SpentTotal: "0"}, nil
}
func (f *fakeAPIKeyService) Create(context.Context, customer.APIKeyCreateParams) (customer.CreatedAPIKey, error) {
	return f.created, nil
}
func (f *fakeAPIKeyService) Update(context.Context, int64, customer.APIKeyUpdateParams) (customer.APIKey, error) {
	return f.updated, nil
}
func (f *fakeAPIKeyService) Revoke(context.Context, int64) (customer.APIKey, error) {
	return f.revoked, nil
}
func (f *fakeAPIKeyService) Delete(context.Context, int64) error {
	return nil
}

type fakeAdjustmentService struct {
	out customer.Adjustment
	err error
}

func (f *fakeAdjustmentService) Adjust(context.Context, customer.AdjustParams) (customer.Adjustment, error) {
	if f.err != nil {
		return customer.Adjustment{}, f.err
	}
	return f.out, nil
}

func TestGetUserReturnsBalances(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{UserService: &fakeUserService{
		detail: customer.UserDetail{
			User:     customer.User{ID: 7, Email: "x@y.com"},
			Balances: []customer.Balance{{Currency: "USD", Balance: "12.5", ReservedBalance: "0"}},
		},
	}})

	rec := doAdmin(t, handler, http.MethodGet, "/v1/users/7", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "\"balances\"") || !strings.Contains(rec.Body.String(), "12.5") {
		t.Fatalf("expected balances in response: %s", rec.Body.String())
	}
}

// 限流三个维度都能设成「不限」（0）与「继承全局默认」（null）：
// 这两种意图必须能分别表达，合并成一个值会让管理员失去「显式不限」这个选择。
func TestSetUserRateLimitsDistinguishesUnlimitedFromInherit(t *testing.T) {
	service := &fakeUserService{}
	handler := newQueryRouter(t, adminapi.RouterDeps{UserService: service})

	rec := doAdmin(t, handler, http.MethodPatch, "/v1/users/7/rate-limits",
		`{"rpm_limit":600,"rpd_limit":0,"concurrency_limit":null}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	got := service.rateLimits
	if got.UserID != 7 {
		t.Fatalf("user id = %d, want 7", got.UserID)
	}
	if got.RPMLimit == nil || *got.RPMLimit != 600 {
		t.Fatalf("rpm limit = %v, want 600", got.RPMLimit)
	}
	if got.RPDLimit == nil || *got.RPDLimit != 0 {
		t.Fatalf("rpd limit = %v, want 0（显式不限）", got.RPDLimit)
	}
	if got.ConcurrencyLimit != nil {
		t.Fatalf("concurrency limit = %v, want nil（继承全局默认）", got.ConcurrencyLimit)
	}
}

func TestCreateAdjustmentReturns201(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{
		UserService:       &fakeUserService{},
		AdjustmentService: &fakeAdjustmentService{out: customer.Adjustment{EntryID: 3, UserID: 7, EntryType: "adjustment_credit", Amount: "10", Currency: "USD", BalanceAfter: "10"}},
	})

	body := `{"direction":"credit","amount":"10","currency":"USD","reason":"top up"}`
	rec := doAdmin(t, handler, http.MethodPost, "/v1/users/7/balance-adjustments", body, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateAdjustmentInsufficientBalanceReturns422(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{
		UserService:       &fakeUserService{},
		AdjustmentService: &fakeAdjustmentService{err: failure.New(failure.CodeLedgerInsufficientBalance, failure.WithMessage("insufficient balance"))},
	})

	body := `{"direction":"debit","amount":"10","currency":"USD","reason":"deduct"}`
	rec := doAdmin(t, handler, http.MethodPost, "/v1/users/7/balance-adjustments", body, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateAPIKeyReturnsPlaintext(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{APIKeyService: &fakeAPIKeyService{
		created: customer.CreatedAPIKey{
			APIKey:    customer.APIKey{ID: 5, UserID: 100, Name: "ci", KeyPrefix: "sk-unio-a3f9k2m1", Status: "active", SpentTotal: "0"},
			Plaintext: "sk-unio-a3f9k2m1x7bq4vzn8dht",
		},
	}})

	// 线路必填：创建请求必须带 route_id（fake service 不校验，这里以真实契约填写）。
	body := `{"name":"ci","route_id":3}`
	rec := doAdmin(t, handler, http.MethodPost, "/v1/users/100/api-keys", body, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sk-unio-a3f9k2m1x7bq4vzn8dht") {
		t.Fatalf("create response must return one-time plaintext: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "key_hash") {
		t.Fatalf("api key response must not contain key_hash: %s", rec.Body.String())
	}
}

// 明文只在创建响应里出现一次。这条守住的是「创建之外的任何接口都不许带 plaintext」——
// 一旦谁把它加回 apiKeyDTO，前端就会重新长出「稍后再复制」的入口。
func TestAPIKeyReadEndpointsOmitPlaintext(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{APIKeyService: &fakeAPIKeyService{
		list: []customer.APIKey{
			{ID: 5, UserID: 100, Name: "ci", KeyPrefix: "sk-unio-a3f9k2m1", Status: "active", SpentTotal: "0"},
		},
		updated: customer.APIKey{ID: 5, UserID: 100, Name: "ci", KeyPrefix: "sk-unio-a3f9k2m1", Status: "disabled", SpentTotal: "0"},
		revoked: customer.APIKey{ID: 5, UserID: 100, Name: "ci", KeyPrefix: "sk-unio-a3f9k2m1", Status: "revoked", SpentTotal: "0"},
	}})

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPatch, "/v1/api-keys/5", `{"disabled":true}`},
		{http.MethodPost, "/v1/api-keys/5/revoke", ""},
	} {
		rec := doAdmin(t, handler, tc.method, tc.path, tc.body, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: expected 200, got %d (%s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "plaintext") {
			t.Fatalf("%s %s must not expose plaintext: %s", tc.method, tc.path, rec.Body.String())
		}
	}
}

func TestUpdateAPIKeyReturns200(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{APIKeyService: &fakeAPIKeyService{
		updated: customer.APIKey{ID: 5, Status: "disabled", SpentTotal: "0"},
	}})

	body := `{"disabled":true}`
	rec := doAdmin(t, handler, http.MethodPatch, "/v1/api-keys/5", body, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestRevokeAPIKeyReturns200(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{APIKeyService: &fakeAPIKeyService{
		revoked: customer.APIKey{ID: 5, Status: "revoked", SpentTotal: "0"},
	}})

	rec := doAdmin(t, handler, http.MethodPost, "/v1/api-keys/5/revoke", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteAPIKeyReturns204(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{APIKeyService: &fakeAPIKeyService{}})

	rec := doAdmin(t, handler, http.MethodDelete, "/v1/api-keys/5", "", true)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d (%s)", rec.Code, rec.Body.String())
	}
}
