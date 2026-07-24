package providerorigin_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/providerorigin"
)

func TestNormalizeBaseURL(t *testing.T) {
	ok := map[string]string{
		"https://Open.CODEX521.cc":         "https://open.codex521.cc",
		"https://open.codex521.cc/":        "https://open.codex521.cc",
		"https://open.codex521.cc:443/":    "https://open.codex521.cc",
		"http://Example.com:80":            "http://example.com",
		"https://example.com:8443/API/v1/": "https://example.com:8443/API/v1",
		"https://example.com/Path/Keep":    "https://example.com/Path/Keep",
		"  https://example.com  ":          "https://example.com",
	}
	for in, want := range ok {
		got, err := providerorigin.NormalizeBaseURL(in)
		if err != nil {
			t.Fatalf("NormalizeBaseURL(%q) unexpected err: %v", in, err)
		}
		if got != want {
			t.Fatalf("NormalizeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}

	bad := []string{
		"",
		"ftp://example.com",
		"example.com",                   // no scheme
		"https://user:pass@example.com", // userinfo
		"https://example.com?a=1",       // query
		"https://example.com#frag",      // fragment
		"https://",                      // no host
	}
	for _, in := range bad {
		if _, err := providerorigin.NormalizeBaseURL(in); err == nil {
			t.Fatalf("NormalizeBaseURL(%q) expected error, got nil", in)
		} else if failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
			t.Fatalf("NormalizeBaseURL(%q) code = %q, want invalid_argument", in, failure.CodeOf(err))
		}
	}
}

// ---- fakes ----

type fakeStore struct {
	provider    sqlc.Provider
	providerErr error
	createRow   sqlc.ProviderOrigin
	createErr   error
	createParam sqlc.CreateProviderOriginParams
	createCalls int
}

func (f *fakeStore) GetProvider(context.Context, int64) (sqlc.Provider, error) {
	return f.provider, f.providerErr
}
func (f *fakeStore) CreateProviderOrigin(_ context.Context, arg sqlc.CreateProviderOriginParams) (sqlc.ProviderOrigin, error) {
	f.createParam = arg
	f.createCalls++
	return f.createRow, f.createErr
}
func (f *fakeStore) GetProviderOrigin(context.Context, int64) (sqlc.ProviderOrigin, error) {
	return sqlc.ProviderOrigin{}, pgx.ErrNoRows
}
func (f *fakeStore) ListProviderOriginsPage(context.Context, sqlc.ListProviderOriginsPageParams) ([]sqlc.ListProviderOriginsPageRow, error) {
	return nil, nil
}
func (f *fakeStore) CountProviderOrigins(context.Context, sqlc.CountProviderOriginsParams) (int64, error) {
	return 0, nil
}
func (f *fakeStore) UpdateProviderOriginName(context.Context, sqlc.UpdateProviderOriginNameParams) (sqlc.ProviderOrigin, error) {
	return sqlc.ProviderOrigin{}, pgx.ErrNoRows
}
func (f *fakeStore) CountChannelsByProviderOrigin(context.Context, int64) (int64, error) {
	return 0, nil
}

type fakeControl struct {
	err    error
	called bool
}

func (c *fakeControl) InitOriginControl(context.Context, int64, int64, int64, string) (bool, error) {
	c.called = true
	return true, c.err
}

func TestCreateNormalizesAndInitializesControl(t *testing.T) {
	store := &fakeStore{
		provider: sqlc.Provider{ID: 1, Status: "enabled"},
		createRow: sqlc.ProviderOrigin{
			ID: 5, ProviderID: 1, Name: "StarAPI", BaseUrl: "https://open.codex521.cc",
			BaseUrlRevision: 1, Status: "enabled", StatusRevision: 1,
		},
	}
	ctrl := &fakeControl{}
	svc := providerorigin.NewService(store, ctrl)

	ep, err := svc.Create(context.Background(), providerorigin.CreateInput{
		ProviderID: 1, Name: "StarAPI", BaseURL: "https://Open.Codex521.cc/", Status: "enabled",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if store.createParam.BaseUrl != "https://open.codex521.cc" {
		t.Fatalf("persisted base_url = %q, want normalized", store.createParam.BaseUrl)
	}
	if !ctrl.called {
		t.Fatalf("InitOriginControl must be called on create")
	}
	if ep.RuntimeSyncPending {
		t.Fatalf("control init succeeded; RuntimeSyncPending should be false")
	}
}

func TestCreateProviderNotFound(t *testing.T) {
	store := &fakeStore{providerErr: pgx.ErrNoRows}
	svc := providerorigin.NewService(store, &fakeControl{})
	_, err := svc.Create(context.Background(), providerorigin.CreateInput{
		ProviderID: 9, Name: "x", BaseURL: "https://x.example", Status: "enabled",
	})
	if failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("want invalid_argument for missing provider, got %v", failure.CodeOf(err))
	}
	if store.createCalls != 0 {
		t.Fatalf("must not create origin when provider missing")
	}
}

func TestCreateControlFailureMarksRuntimeSyncPending(t *testing.T) {
	store := &fakeStore{
		provider:  sqlc.Provider{ID: 1, Status: "enabled"},
		createRow: sqlc.ProviderOrigin{ID: 5, ProviderID: 1, Name: "n", BaseUrl: "https://x.example", BaseUrlRevision: 1, Status: "enabled", StatusRevision: 1},
	}
	svc := providerorigin.NewService(store, &fakeControl{err: context.DeadlineExceeded})
	ep, err := svc.Create(context.Background(), providerorigin.CreateInput{
		ProviderID: 1, Name: "n", BaseURL: "https://x.example", Status: "enabled",
	})
	if err != nil {
		t.Fatalf("create should not fail on control error (fail-closed via pending): %v", err)
	}
	if !ep.RuntimeSyncPending {
		t.Fatalf("control init failed; RuntimeSyncPending should be true")
	}
}
