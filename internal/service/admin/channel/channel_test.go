package channel_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/channel"
)

type fakeChannelStore struct {
	provider      sqlc.Provider
	providerErr   error
	createRow     sqlc.Channel
	createErr     error
	createParam   sqlc.CreateChannelParams
	createCalls   int
	credentialAff int64
	credentialErr error
	deleteAff     int64
	deleteErr     error
	deleteID      int64
	deleteCalls   int

	rateLimitsParam sqlc.SetChannelRateLimitsParams
	rateLimitsCalls int
}

func (s *fakeChannelStore) GetProvider(_ context.Context, _ int64) (sqlc.Provider, error) {
	return s.provider, s.providerErr
}
func (s *fakeChannelStore) ListChannelsPage(context.Context, sqlc.ListChannelsPageParams) ([]sqlc.ListChannelsPageRow, error) {
	return nil, nil
}
func (s *fakeChannelStore) CountChannels(context.Context, sqlc.CountChannelsParams) (int64, error) {
	return 0, nil
}
func (s *fakeChannelStore) GetChannel(_ context.Context, _ int64) (sqlc.Channel, error) {
	return sqlc.Channel{}, pgx.ErrNoRows
}
func (s *fakeChannelStore) CreateChannel(_ context.Context, arg sqlc.CreateChannelParams) (sqlc.Channel, error) {
	s.createParam = arg
	s.createCalls++
	return s.createRow, s.createErr
}
func (s *fakeChannelStore) UpdateChannel(_ context.Context, _ sqlc.UpdateChannelParams) (sqlc.Channel, error) {
	return sqlc.Channel{}, pgx.ErrNoRows
}
func (s *fakeChannelStore) SetChannelRateLimits(_ context.Context, arg sqlc.SetChannelRateLimitsParams) (sqlc.Channel, error) {
	s.rateLimitsParam = arg
	s.rateLimitsCalls++
	return sqlc.Channel{
		ID:       arg.ID,
		RpmLimit: arg.RpmLimit,
		TpmLimit: arg.TpmLimit,
		RpdLimit: arg.RpdLimit,
	}, nil
}
func (s *fakeChannelStore) UpdateChannelCredential(_ context.Context, _ sqlc.UpdateChannelCredentialParams) (int64, error) {
	return s.credentialAff, s.credentialErr
}
func (s *fakeChannelStore) DeleteChannelCascade(_ context.Context, id int64) (int64, error) {
	s.deleteID = id
	s.deleteCalls++
	return s.deleteAff, s.deleteErr
}

type fakeRegistry struct {
	has  bool
	keys map[string][]string
}

func (r fakeRegistry) HasAny(string, string) bool { return r.has }

func (r fakeRegistry) AdapterKeys(protocol string) []string { return r.keys[protocol] }

func validCreateInput() channel.CreateInput {
	return channel.CreateInput{
		ProviderID: 1,
		Name:       "primary",
		Protocol:   channel.ProtocolOpenAI,
		AdapterKey: "deepseek",
		BaseURL:    "https://api.example.test/v1",
		Credential: "sk-secret",
		Status:     channel.StatusEnabled,
		Priority:   10,
	}
}

func TestCreateRejectsUnsupportedAdapterBinding(t *testing.T) {
	store := &fakeChannelStore{}
	svc := channel.NewService(store, fakeRegistry{has: false})

	_, err := svc.Create(context.Background(), validCreateInput())
	if got := failure.CodeOf(err); got != failure.CodeAdminAdapterBindingUnsupported {
		t.Fatalf("expected %q, got %q", failure.CodeAdminAdapterBindingUnsupported, got)
	}
	if store.createCalls != 0 {
		t.Fatalf("store must not be called on unsupported binding")
	}
}

func TestCreateRejectsInvalidArguments(t *testing.T) {
	cases := map[string]func(in *channel.CreateInput){
		"bad protocol": func(in *channel.CreateInput) { in.Protocol = "grpc" },
		"bad base_url": func(in *channel.CreateInput) { in.BaseURL = "notaurl" },
		"empty name":   func(in *channel.CreateInput) { in.Name = " " },
		"empty cred":   func(in *channel.CreateInput) { in.Credential = "" },
		"neg priority": func(in *channel.CreateInput) { in.Priority = -1 },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := validCreateInput()
			mutate(&in)
			svc := channel.NewService(&fakeChannelStore{}, fakeRegistry{has: true})
			_, err := svc.Create(context.Background(), in)
			if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
				t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
			}
		})
	}
}

func TestCreateProviderNotFound(t *testing.T) {
	store := &fakeChannelStore{providerErr: pgx.ErrNoRows}
	svc := channel.NewService(store, fakeRegistry{has: true})

	_, err := svc.Create(context.Background(), validCreateInput())
	if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
	}
}

func TestCreatePersistsPlaintextCredential(t *testing.T) {
	now := time.Now()
	store := &fakeChannelStore{
		provider: sqlc.Provider{ID: 1, Slug: "openai", Status: "enabled"},
		createRow: sqlc.Channel{
			ID: 9, ProviderID: 1, Name: "primary", Protocol: "openai", AdapterKey: "deepseek",
			BaseUrl: "https://api.example.test/v1", Status: "enabled", Priority: 10,
			CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
			UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		},
	}
	svc := channel.NewService(store, fakeRegistry{has: true})

	got, err := svc.Create(context.Background(), validCreateInput())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 渠道凭据明文存储（产品决策）：原样落库，不加密。
	if store.createParam.Credential != "sk-secret" {
		t.Fatalf("expected plaintext credential persisted, got %q", store.createParam.Credential)
	}
	if got.ID != 9 || got.TimeoutMs != nil {
		t.Fatalf("unexpected mapped channel: %+v", got)
	}
}

// TestCreateDefaultsAdapterKeyToProtocol 验证：adapter_key 留空时默认取 protocol 同名的忠实
// 透传 adapter（openai→"openai"、anthropic→"anthropic"），并以该默认键落库。
func TestCreateDefaultsAdapterKeyToProtocol(t *testing.T) {
	cases := []struct {
		protocol string
		wantKey  string
	}{
		{protocol: channel.ProtocolOpenAI, wantKey: "openai"},
		{protocol: channel.ProtocolAnthropic, wantKey: "anthropic"},
	}

	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			store := &fakeChannelStore{
				provider:  sqlc.Provider{ID: 1, Slug: "p", Status: "enabled"},
				createRow: sqlc.Channel{ID: 1, ProviderID: 1, Name: "primary", Protocol: tc.protocol, AdapterKey: tc.wantKey},
			}
			svc := channel.NewService(store, fakeRegistry{has: true})

			in := validCreateInput()
			in.Protocol = tc.protocol
			in.AdapterKey = "" // 留空触发默认

			if _, err := svc.Create(context.Background(), in); err != nil {
				t.Fatalf("create: %v", err)
			}
			if store.createParam.AdapterKey != tc.wantKey {
				t.Fatalf("persisted adapter_key = %q, want %q (default to protocol)", store.createParam.AdapterKey, tc.wantKey)
			}
		})
	}
}

// TestAdapterKeyOptions 验证：服务把 registry 注册的 adapter_key 按协议枚举出来，
// 与协议同名的键标记 is_default（供前端下拉默认选中忠实透传）。
func TestAdapterKeyOptions(t *testing.T) {
	reg := fakeRegistry{
		has: true,
		keys: map[string][]string{
			channel.ProtocolOpenAI:    {"deepseek", "openai"},
			channel.ProtocolAnthropic: {"anthropic", "deepseek"},
		},
	}
	svc := channel.NewService(&fakeChannelStore{}, reg)

	got := svc.AdapterKeyOptions()
	want := []channel.AdapterKeyOption{
		{Protocol: channel.ProtocolOpenAI, AdapterKey: "deepseek", IsDefault: false},
		{Protocol: channel.ProtocolOpenAI, AdapterKey: "openai", IsDefault: true},
		{Protocol: channel.ProtocolAnthropic, AdapterKey: "anthropic", IsDefault: true},
		{Protocol: channel.ProtocolAnthropic, AdapterKey: "deepseek", IsDefault: false},
	}

	if len(got) != len(want) {
		t.Fatalf("got %d options, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("option[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestRotateCredentialNotFound(t *testing.T) {
	store := &fakeChannelStore{credentialAff: 0}
	svc := channel.NewService(store, fakeRegistry{has: true})

	err := svc.RotateCredential(context.Background(), channel.RotateCredentialInput{ID: 5, Credential: "sk-new"})
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

func TestRotateCredentialSuccess(t *testing.T) {
	store := &fakeChannelStore{credentialAff: 1}
	svc := channel.NewService(store, fakeRegistry{has: true})

	if err := svc.RotateCredential(context.Background(), channel.RotateCredentialInput{ID: 5, Credential: "sk-new"}); err != nil {
		t.Fatalf("rotate: %v", err)
	}
}

func newChannelService(store *fakeChannelStore) *channel.Service {
	return channel.NewService(store, fakeRegistry{has: true})
}

func TestDeleteRejectsInvalidID(t *testing.T) {
	store := &fakeChannelStore{}
	err := newChannelService(store).Delete(context.Background(), 0)
	if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
	}
	if store.deleteCalls != 0 {
		t.Fatalf("store should not be called on invalid id")
	}
}

// 录错且无引用的渠道可真删；级联清理由 DB CTE 完成，受影响行 0 仅当 channel 不存在。
func TestDeleteSuccess(t *testing.T) {
	store := &fakeChannelStore{deleteAff: 1}
	if err := newChannelService(store).Delete(context.Background(), 9); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if store.deleteID != 9 {
		t.Fatalf("expected delete id 9, got %d", store.deleteID)
	}
}

func TestDeleteNotFoundWhenNoRows(t *testing.T) {
	store := &fakeChannelStore{deleteAff: 0}
	err := newChannelService(store).Delete(context.Background(), 9)
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

// 已被请求/账务历史引用时，DB 报外键冲突（23503），降级为 conflict 提示改用停用。
func TestDeleteConflictOnForeignKeyViolation(t *testing.T) {
	store := &fakeChannelStore{deleteErr: &pgconn.PgError{Code: "23503"}}
	err := newChannelService(store).Delete(context.Background(), 9)
	if got := failure.CodeOf(err); got != failure.CodeAdminConflict {
		t.Fatalf("expected %q, got %q", failure.CodeAdminConflict, got)
	}
}
