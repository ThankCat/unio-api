package adminapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ThankCat/unio-api/internal/app/adminapi"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/service/admin/customer"
)

type fakeUserService struct {
	list   []customer.User
	detail customer.UserDetail
	getErr error
}

func (f *fakeUserService) List(context.Context, customer.UserListParams) ([]customer.User, int64, error) {
	return f.list, int64(len(f.list)), nil
}

func (f *fakeUserService) Get(context.Context, int64) (customer.UserDetail, error) {
	return f.detail, f.getErr
}

type fakeProjectService struct {
	list []customer.Project
}

func (f *fakeProjectService) List(context.Context, customer.ProjectListParams) ([]customer.Project, int64, error) {
	return f.list, int64(len(f.list)), nil
}

func (f *fakeProjectService) Get(context.Context, int64) (customer.Project, error) {
	return customer.Project{ID: 1, UserID: 10, Name: "ws"}, nil
}

func (f *fakeProjectService) SetDefaultRoute(_ context.Context, id int64, routeID *int64) (customer.Project, error) {
	return customer.Project{ID: id, UserID: 10, Name: "ws", DefaultRouteID: routeID}, nil
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
	return customer.APIKey{ID: 1, KeyPrefix: "unio_sk_abc", Status: "active", SpentTotal: "0"}, nil
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

func TestListUsersReturns200(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{UserService: &fakeUserService{
		list: []customer.User{{ID: 1, Email: "a@b.com", DisplayName: "A"}},
	}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/users?q=a", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "password_hash") {
		t.Fatalf("user response must not contain password_hash: %s", rec.Body.String())
	}
}

func TestGetUserReturnsBalances(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{UserService: &fakeUserService{
		detail: customer.UserDetail{
			User:     customer.User{ID: 7, Email: "x@y.com"},
			Balances: []customer.Balance{{Currency: "USD", Balance: "12.5", ReservedBalance: "0"}},
		},
	}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/users/7", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "\"balances\"") || !strings.Contains(rec.Body.String(), "12.5") {
		t.Fatalf("expected balances in response: %s", rec.Body.String())
	}
}

func TestCreateAdjustmentReturns201(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{
		UserService:       &fakeUserService{},
		AdjustmentService: &fakeAdjustmentService{out: customer.Adjustment{EntryID: 3, UserID: 7, EntryType: "adjustment_credit", Amount: "10", Currency: "USD", BalanceAfter: "10"}},
	})

	body := `{"direction":"credit","amount":"10","currency":"USD","reason":"top up"}`
	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/users/7/balance-adjustments", body, true)
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
	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/users/7/balance-adjustments", body, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestListProjectsReturns200(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{ProjectService: &fakeProjectService{
		list: []customer.Project{{ID: 1, UserID: 10, Name: "ws"}},
	}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/projects?user_id=10", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateAPIKeyReturnsPlaintextOnce(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{APIKeyService: &fakeAPIKeyService{
		created: customer.CreatedAPIKey{
			APIKey:    customer.APIKey{ID: 5, ProjectID: 100, Name: "ci", KeyPrefix: "unio_sk_abc", Status: "active", SpentTotal: "0"},
			Plaintext: "unio_sk_secretsecret",
		},
	}})

	body := `{"name":"ci"}`
	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/projects/100/api-keys", body, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unio_sk_secretsecret") {
		t.Fatalf("create response must return one-time plaintext: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "key_hash") {
		t.Fatalf("api key response must not contain key_hash: %s", rec.Body.String())
	}
}

func TestUpdateAPIKeyReturns200(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{APIKeyService: &fakeAPIKeyService{
		updated: customer.APIKey{ID: 5, Status: "disabled", SpentTotal: "0"},
	}})

	body := `{"disabled":true}`
	rec := doAdmin(t, handler, http.MethodPatch, "/admin/v1/api-keys/5", body, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestRevokeAPIKeyReturns200(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{APIKeyService: &fakeAPIKeyService{
		revoked: customer.APIKey{ID: 5, Status: "revoked", SpentTotal: "0"},
	}})

	rec := doAdmin(t, handler, http.MethodDelete, "/admin/v1/api-keys/5", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
}
