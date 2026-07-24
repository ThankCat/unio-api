package channel_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channel"
)

type fakeChannelStore struct {
	provider                sqlc.Provider
	providerErr             error
	origin                sqlc.ProviderOrigin
	originErr             error
	createRow               sqlc.Channel
	createErr               error
	createParam             sqlc.CreateChannelParams
	createCalls             int
	credentialAff           int64
	credentialErr           error
	deleteAff               int64
	deleteErr               error
	deleteID                int64
	deleteCalls             int
	getRow                  sqlc.Channel
	getRows                 []sqlc.Channel
	getErr                  error
	getCalls                int
	updateRow               sqlc.Channel
	updateErr               error
	updateParam             sqlc.UpdateChannelParams
	updateCalls             int
	archiveAff              int64
	archiveErr              error
	archiveID               int64
	archiveReplacementAff   int64
	archiveReplacementParam sqlc.ArchiveChannelWithReplacementParams
	emptyRoutes             []sqlc.ListEnabledRoutesEmptiedByChannelRow
	emptyRouteErr           error
	restoreAff              int64
	restoreErr              error
	restoreID               int64
}

func (s *fakeChannelStore) GetProvider(_ context.Context, _ int64) (sqlc.Provider, error) {
	return s.provider, s.providerErr
}
func (s *fakeChannelStore) GetProviderOrigin(_ context.Context, _ int64) (sqlc.ProviderOrigin, error) {
	return s.origin, s.originErr
}
func (s *fakeChannelStore) ListChannelsPage(context.Context, sqlc.ListChannelsPageParams) ([]sqlc.ListChannelsPageRow, error) {
	return nil, nil
}
func (s *fakeChannelStore) CountChannels(context.Context, sqlc.CountChannelsParams) (int64, error) {
	return 0, nil
}
func (s *fakeChannelStore) GetChannel(_ context.Context, _ int64) (sqlc.Channel, error) {
	if s.getCalls < len(s.getRows) {
		row := s.getRows[s.getCalls]
		s.getCalls++
		return row, s.getErr
	}
	s.getCalls++
	return s.getRow, s.getErr
}
func (s *fakeChannelStore) CreateChannel(_ context.Context, arg sqlc.CreateChannelParams) (sqlc.Channel, error) {
	s.createParam = arg
	s.createCalls++
	return s.createRow, s.createErr
}
func (s *fakeChannelStore) UpdateChannel(_ context.Context, arg sqlc.UpdateChannelParams) (sqlc.Channel, error) {
	s.updateParam = arg
	s.updateCalls++
	if s.updateErr != nil {
		return sqlc.Channel{}, s.updateErr
	}
	return s.updateRow, nil
}
func (s *fakeChannelStore) SetChannelBillingBehavior(_ context.Context, arg sqlc.SetChannelBillingBehaviorParams) (sqlc.Channel, error) {
	return sqlc.Channel{
		ID:                        arg.ID,
		UpstreamBillsOnDisconnect: arg.UpstreamBillsOnDisconnect,
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
func (s *fakeChannelStore) ArchiveChannelCascade(_ context.Context, id int64) (int64, error) {
	s.archiveID = id
	return s.archiveAff, s.archiveErr
}
func (s *fakeChannelStore) ArchiveChannelWithReplacement(_ context.Context, arg sqlc.ArchiveChannelWithReplacementParams) (int64, error) {
	s.archiveReplacementParam = arg
	return s.archiveReplacementAff, s.archiveErr
}
func (s *fakeChannelStore) ListEnabledRoutesEmptiedByChannel(context.Context, int64) ([]sqlc.ListEnabledRoutesEmptiedByChannelRow, error) {
	return s.emptyRoutes, s.emptyRouteErr
}
func (s *fakeChannelStore) RestoreChannel(_ context.Context, id int64) (int64, error) {
	s.restoreID = id
	return s.restoreAff, s.restoreErr
}

type fakeRegistry struct {
	has  bool
	keys map[string][]string
}

func (r fakeRegistry) HasAny(string, string) bool { return r.has }

func (r fakeRegistry) AdapterKeys(protocol string) []string { return r.keys[protocol] }

type fakeRuntimePublisher struct {
	result runtimecontrol.PublishResult
	err    error
	req    runtimecontrol.PublishRequest
	calls  int
}

func (p *fakeRuntimePublisher) Publish(_ context.Context, req runtimecontrol.PublishRequest) (runtimecontrol.PublishResult, error) {
	p.req = req
	p.calls++
	return p.result, p.err
}

type fakeAdmissionControlStore struct {
	restoreRevision int64
	restorePayload  string
	restoreCalls    int
	restoreErr      error
	readSnapshot    breakerstore.ControlSnapshot
	readErr         error
}

func (s *fakeAdmissionControlStore) ChannelAdmissionControl(int64) breakerstore.ControlTarget {
	return breakerstore.ControlTarget{}
}

func (s *fakeAdmissionControlStore) RestoreMissingControl(_ context.Context, _ breakerstore.ControlTarget, revision int64, payload string) (bool, error) {
	s.restoreRevision = revision
	s.restorePayload = payload
	s.restoreCalls++
	return s.restoreErr == nil, s.restoreErr
}

func (s *fakeAdmissionControlStore) ReadControl(context.Context, breakerstore.ControlTarget, int64) (breakerstore.ControlSnapshot, error) {
	return s.readSnapshot, s.readErr
}

type fakeCredentialRotator struct {
	result channel.RotateCredentialResult
	err    error
	input  channel.RotateCredentialInput
}

func (r *fakeCredentialRotator) RotateCredentialAndTest(_ context.Context, in channel.RotateCredentialInput) (channel.RotateCredentialResult, error) {
	r.input = in
	return r.result, r.err
}

func validCreateInput() channel.CreateInput {
	return channel.CreateInput{
		ProviderID:         1,
		ProviderOriginID: 1,
		Name:               "primary",
		Protocol:           channel.ProtocolOpenAI,
		AdapterKey:         "deepseek",
		Credential:         "sk-secret",
		Status:             channel.StatusEnabled,
		Priority:           10,
	}
}

func int64Ptr(v int64) *int64 { return &v }

func validUpdateInput() channel.UpdateInput {
	return channel.UpdateInput{
		ID: 9, Name: "primary", ProviderOriginID: 1, Status: channel.StatusEnabled, Priority: 10,
	}
}

func runtimeChannelRow(revision int64) sqlc.Channel {
	return sqlc.Channel{
		ID: 9, ProviderID: 1, ProviderOriginID: 1, Name: "primary", Protocol: "openai", AdapterKey: "openai",
		Credential: "sk-live", Status: channel.StatusEnabled, Priority: 10, ConfigRevision: 1,
		AdmissionLimitsRevision: revision,
	}
}

func TestCanonicalAdmissionLimitsPayloadKeepsInheritanceDistinctFromUnlimited(t *testing.T) {
	payload, err := channel.CanonicalAdmissionLimitsPayload(channel.AdmissionLimits{
		RPM: int64Ptr(10), RPD: int64Ptr(0), TPM: nil, Concurrency: int64Ptr(2),
	})
	if err != nil {
		t.Fatalf("canonical payload: %v", err)
	}
	if want := `{"rpm":10,"rpd":0,"tpm":null,"concurrency":2}`; payload != want {
		t.Fatalf("payload = %s, want %s", payload, want)
	}

	inherited, err := channel.CanonicalAdmissionLimitsPayload(channel.AdmissionLimits{})
	if err != nil {
		t.Fatalf("canonical inherited payload: %v", err)
	}
	if want := `{"rpm":null,"rpd":null,"tpm":null,"concurrency":null}`; inherited != want {
		t.Fatalf("inherited payload = %s, want %s", inherited, want)
	}
}

func TestCreatePersistsLimitsAndInitializesRevisionOneControl(t *testing.T) {
	rpm, rpd, concurrency := int64(10), int64(0), int64(2)
	payload := `{"rpm":10,"rpd":0,"tpm":null,"concurrency":2}`
	row := runtimeChannelRow(1)
	row.RpmLimit = pgtype.Int4{Int32: 10, Valid: true}
	row.RpdLimit = pgtype.Int4{Int32: 0, Valid: true}
	row.ConcurrencyLimit = pgtype.Int4{Int32: 2, Valid: true}
	store := &fakeChannelStore{
		provider:  sqlc.Provider{ID: 1, Name: "Provider"},
		origin:  sqlc.ProviderOrigin{ID: 1, ProviderID: 1, Name: "Primary", Status: "enabled", BaseUrl: "https://api.example.test"},
		createRow: row,
	}
	control := &fakeAdmissionControlStore{readSnapshot: breakerstore.ControlSnapshot{
		ActiveRevision: 1, ActivePayload: payload, SyncState: "active",
	}}
	in := validCreateInput()
	in.RateLimitsProvided = true
	in.RPMLimit, in.RPDLimit, in.ConcurrencyLimit = &rpm, &rpd, &concurrency
	bills := true
	in.BillsOnDisconnect = &bills

	got, err := channel.NewService(store, fakeRegistry{has: true}).
		WithRuntimeControl(&fakeRuntimePublisher{}, control).
		Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if store.createCalls != 1 || !store.createParam.RpmLimit.Valid || store.createParam.RpmLimit.Int32 != 10 ||
		!store.createParam.RpdLimit.Valid || store.createParam.RpdLimit.Int32 != 0 || store.createParam.TpmLimit.Valid ||
		!store.createParam.ConcurrencyLimit.Valid || store.createParam.ConcurrencyLimit.Int32 != 2 ||
		!store.createParam.UpstreamBillsOnDisconnect {
		t.Fatalf("unexpected create params: %+v", store.createParam)
	}
	if control.restoreCalls != 1 || control.restoreRevision != 1 || control.restorePayload != payload {
		t.Fatalf("unexpected control initialization: calls=%d revision=%d payload=%s", control.restoreCalls, control.restoreRevision, control.restorePayload)
	}
	if got.AdmissionLimitsRevision != 1 || got.RuntimeSyncPending {
		t.Fatalf("unexpected create result: %+v", got)
	}
}

func TestUpdateNoopAdmissionLimitsDoesNotPublishOrIncrement(t *testing.T) {
	current := runtimeChannelRow(4)
	current.RpmLimit = pgtype.Int4{Int32: 10, Valid: true}
	store := &fakeChannelStore{
		provider: sqlc.Provider{ID: 1, Name: "Provider"},
		origin: sqlc.ProviderOrigin{ID: 1, ProviderID: 1, Status: "enabled", BaseUrl: "https://api.example.test"},
		getRow:   current, updateRow: current,
	}
	publisher := &fakeRuntimePublisher{}
	control := &fakeAdmissionControlStore{readSnapshot: breakerstore.ControlSnapshot{
		ActiveRevision: 4, ActivePayload: `{"rpm":10,"rpd":null,"tpm":null,"concurrency":null}`, SyncState: "active",
	}}
	in := validUpdateInput()
	in.RateLimitsProvided = true
	in.RPMLimit = int64Ptr(10)

	got, err := channel.NewService(store, fakeRegistry{has: true}).WithRuntimeControl(publisher, control).Update(context.Background(), in)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if publisher.calls != 0 || store.updateCalls != 1 {
		t.Fatalf("no-op limits must use ordinary update without publishing: publish=%d update=%d", publisher.calls, store.updateCalls)
	}
	if got.AdmissionLimitsRevision != 4 || got.RuntimeSyncPending {
		t.Fatalf("no-op limits changed revision or sync state: %+v", got)
	}
}

func TestUpdateChangedAdmissionLimitsPublishesExactlyNextRevision(t *testing.T) {
	current := runtimeChannelRow(4)
	updated := runtimeChannelRow(5)
	updated.RpmLimit = pgtype.Int4{Int32: 10, Valid: true}
	store := &fakeChannelStore{
		provider: sqlc.Provider{ID: 1, Name: "Provider"},
		origin: sqlc.ProviderOrigin{ID: 1, ProviderID: 1, Status: "enabled", BaseUrl: "https://api.example.test"},
		getRows:  []sqlc.Channel{current, updated},
	}
	publisher := &fakeRuntimePublisher{result: runtimecontrol.PublishResult{State: runtimecontrol.PublishCommitted, ActiveRevision: 5}}
	in := validUpdateInput()
	in.RateLimitsProvided = true
	in.RPMLimit = int64Ptr(10)

	got, err := channel.NewService(store, fakeRegistry{has: true}).
		WithRuntimeControl(publisher, &fakeAdmissionControlStore{}).
		Update(context.Background(), in)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if publisher.calls != 1 || store.updateCalls != 0 {
		t.Fatalf("changed limits must only enter through publisher: publish=%d direct_update=%d", publisher.calls, store.updateCalls)
	}
	if publisher.req.Kind != runtimecontrol.KindChannelAdmissionLimits || publisher.req.CurrentRevision != 4 || publisher.req.NextRevision != 5 ||
		publisher.req.ChannelID == nil || *publisher.req.ChannelID != 9 || publisher.req.BusinessCommit == nil {
		t.Fatalf("unexpected publish request: %+v", publisher.req)
	}
	if want := `{"rpm":10,"rpd":null,"tpm":null,"concurrency":null}`; publisher.req.Payload != want {
		t.Fatalf("publish payload = %s, want %s", publisher.req.Payload, want)
	}
	if got.AdmissionLimitsRevision != 5 || got.RuntimeSyncPending {
		t.Fatalf("unexpected committed result: %+v", got)
	}
}

func TestUpdateChangedAdmissionLimitsFailsClosedWithoutPublisher(t *testing.T) {
	current := runtimeChannelRow(2)
	store := &fakeChannelStore{
		origin: sqlc.ProviderOrigin{ID: 1, ProviderID: 1},
		getRow:   current,
	}
	in := validUpdateInput()
	in.RateLimitsProvided = true
	in.RPMLimit = int64Ptr(1)

	_, err := channel.NewService(store, fakeRegistry{has: true}).Update(context.Background(), in)
	if got := failure.CodeOf(err); got != failure.CodeGatewayBreakerStoreUnavailable {
		t.Fatalf("expected fail-closed store unavailable, got %q (%v)", got, err)
	}
	if store.updateCalls != 0 {
		t.Fatalf("ordinary update bypassed unavailable publisher: %d", store.updateCalls)
	}
}

func TestUpdateChangedAdmissionLimitsDoesNotMutateOnPublisherError(t *testing.T) {
	current := runtimeChannelRow(2)
	store := &fakeChannelStore{
		origin: sqlc.ProviderOrigin{ID: 1, ProviderID: 1},
		getRow:   current,
	}
	publisher := &fakeRuntimePublisher{err: context.DeadlineExceeded}
	in := validUpdateInput()
	in.RateLimitsProvided = true
	in.TPMLimit = int64Ptr(100)

	_, err := channel.NewService(store, fakeRegistry{has: true}).
		WithRuntimeControl(publisher, &fakeAdmissionControlStore{}).
		Update(context.Background(), in)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected publisher error, got %v", err)
	}
	if publisher.calls != 1 || store.updateCalls != 0 {
		t.Fatalf("publisher error must leave ordinary update untouched: publish=%d update=%d", publisher.calls, store.updateCalls)
	}
}

func TestUpdatePendingPublishReturnsCommittedDatabaseRevision(t *testing.T) {
	current := runtimeChannelRow(7)
	updated := runtimeChannelRow(8)
	updated.ConcurrencyLimit = pgtype.Int4{Int32: 3, Valid: true}
	store := &fakeChannelStore{
		provider: sqlc.Provider{ID: 1}, origin: sqlc.ProviderOrigin{ID: 1, ProviderID: 1},
		getRows: []sqlc.Channel{current, updated},
	}
	publisher := &fakeRuntimePublisher{result: runtimecontrol.PublishResult{State: runtimecontrol.PublishRuntimeSyncPending}}
	in := validUpdateInput()
	in.RateLimitsProvided = true
	in.ConcurrencyLimit = int64Ptr(3)

	got, err := channel.NewService(store, fakeRegistry{has: true}).
		WithRuntimeControl(publisher, &fakeAdmissionControlStore{}).
		Update(context.Background(), in)
	if err != nil {
		t.Fatalf("update pending: %v", err)
	}
	if got.AdmissionLimitsRevision != 8 || !got.RuntimeSyncPending {
		t.Fatalf("pending publish must expose saved revision 8: %+v", got)
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
		"bad protocol":  func(in *channel.CreateInput) { in.Protocol = "grpc" },
		"zero origin": func(in *channel.CreateInput) { in.ProviderOriginID = 0 },
		"empty name":    func(in *channel.CreateInput) { in.Name = " " },
		"empty cred":    func(in *channel.CreateInput) { in.Credential = "" },
		"neg priority":  func(in *channel.CreateInput) { in.Priority = -1 },
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
		origin: sqlc.ProviderOrigin{ID: 1, ProviderID: 1, Status: "enabled", BaseUrl: "https://api.example.test"},
		createRow: sqlc.Channel{
			ID: 9, ProviderID: 1, ProviderOriginID: 1, Name: "primary", Protocol: "openai", AdapterKey: "deepseek",
			Status: "enabled", Priority: 10,
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
				origin:  sqlc.ProviderOrigin{ID: 1, ProviderID: 1, Status: "enabled", BaseUrl: "https://api.example.test"},
				createRow: sqlc.Channel{ID: 1, ProviderID: 1, ProviderOriginID: 1, Name: "primary", Protocol: tc.protocol, AdapterKey: tc.wantKey},
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
	rotator := &fakeCredentialRotator{err: failure.New(failure.CodeAdminNotFound)}
	svc := channel.NewService(&fakeChannelStore{}, fakeRegistry{has: true}).WithCredentialRotator(rotator)

	_, err := svc.RotateCredential(context.Background(), channel.RotateCredentialInput{ID: 5, Credential: "sk-new"})
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

func TestRotateCredentialSuccess(t *testing.T) {
	want := channel.RotateCredentialResult{CredentialSaved: true, SavedConfigRevision: 8}
	rotator := &fakeCredentialRotator{result: want}
	svc := channel.NewService(&fakeChannelStore{}, fakeRegistry{has: true}).WithCredentialRotator(rotator)

	got, err := svc.RotateCredential(context.Background(), channel.RotateCredentialInput{ID: 5, Credential: "  sk-new  "})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if got != want || rotator.input.Credential != "sk-new" {
		t.Fatalf("unexpected result/input: got=%+v input=%+v", got, rotator.input)
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

// 已归档且无引用的渠道可真删；级联清理由 DB CTE 完成，受影响行 0 仅当 channel 不存在。
func TestDeleteSuccess(t *testing.T) {
	store := &fakeChannelStore{deleteAff: 1, getRow: sqlc.Channel{ID: 9, Status: "archived"}}
	if err := newChannelService(store).Delete(context.Background(), 9); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if store.deleteID != 9 {
		t.Fatalf("expected delete id 9, got %d", store.deleteID)
	}
}

// 未归档的渠道直接删除被拦截（先归档）。
func TestDeleteRejectsWhenNotArchived(t *testing.T) {
	store := &fakeChannelStore{deleteAff: 1, getRow: sqlc.Channel{ID: 9, Status: "enabled"}}
	err := newChannelService(store).Delete(context.Background(), 9)
	if got := failure.CodeOf(err); got != failure.CodeAdminConflict {
		t.Fatalf("expected %q, got %q", failure.CodeAdminConflict, got)
	}
	if store.deleteCalls != 0 {
		t.Fatalf("store delete must not be called before archive")
	}
}

func TestDeleteNotFoundWhenNoRows(t *testing.T) {
	store := &fakeChannelStore{deleteAff: 0, getRow: sqlc.Channel{ID: 9, Status: "archived"}}
	err := newChannelService(store).Delete(context.Background(), 9)
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

// 已归档但仍被请求/账务历史引用时，DB 报外键冲突（23503），降级为 conflict。
func TestDeleteConflictOnForeignKeyViolation(t *testing.T) {
	store := &fakeChannelStore{deleteErr: &pgconn.PgError{Code: "23503"}, getRow: sqlc.Channel{ID: 9, Status: "archived"}}
	err := newChannelService(store).Delete(context.Background(), 9)
	if got := failure.CodeOf(err); got != failure.CodeAdminConflict {
		t.Fatalf("expected %q, got %q", failure.CodeAdminConflict, got)
	}
}

// 渠道归档；恢复时父服务商非归档放行。
func TestArchiveAndRestore(t *testing.T) {
	store := &fakeChannelStore{archiveAff: 1, restoreAff: 1, getRow: sqlc.Channel{ID: 9, ProviderID: 3, Status: "archived"}, provider: sqlc.Provider{ID: 3, Status: "enabled"}}
	svc := newChannelService(store)
	if err := svc.Archive(context.Background(), 9, nil); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if store.archiveID != 9 {
		t.Fatalf("expected archive id 9, got %d", store.archiveID)
	}
	if err := svc.Restore(context.Background(), 9); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if store.restoreID != 9 {
		t.Fatalf("expected restore id 9, got %d", store.restoreID)
	}
}

func TestArchiveRejectsEmptyingEnabledRoute(t *testing.T) {
	store := &fakeChannelStore{
		archiveAff:  1,
		emptyRoutes: []sqlc.ListEnabledRoutesEmptiedByChannelRow{{ID: 3, Name: "production"}},
	}
	err := newChannelService(store).Archive(context.Background(), 9, nil)
	if failure.CodeOf(err) != failure.CodeAdminConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
	if store.archiveID != 0 {
		t.Fatal("archive mutation must not run when an enabled route would be emptied")
	}
}

func TestArchiveAtomicallyReplacesChannel(t *testing.T) {
	replacementID := int64(10)
	store := &fakeChannelStore{
		getRow: sqlc.Channel{
			ID: replacementID, ProviderID: 3, ProviderOriginID: 5, Status: "enabled", CredentialValid: true,
			Credential: "sk-live",
		},
		provider:              sqlc.Provider{ID: 3, Status: "enabled"},
		archiveReplacementAff: 1,
	}
	if err := newChannelService(store).Archive(context.Background(), 9, &replacementID); err != nil {
		t.Fatalf("replace and archive channel: %v", err)
	}
	if store.archiveReplacementParam.ID != 9 || store.archiveReplacementParam.ReplacementChannelID != replacementID {
		t.Fatalf("unexpected atomic archive params: %+v", store.archiveReplacementParam)
	}
	if store.archiveID != 0 {
		t.Fatal("legacy archive mutation must not run for replacement endpoint")
	}
}

// 父服务商归档时，恢复渠道被拦截（先恢复服务商）。
func TestRestoreBlockedWhenProviderArchived(t *testing.T) {
	store := &fakeChannelStore{restoreAff: 1, getRow: sqlc.Channel{ID: 9, ProviderID: 3, Status: "archived"}, provider: sqlc.Provider{ID: 3, Status: "archived"}}
	err := newChannelService(store).Restore(context.Background(), 9)
	if got := failure.CodeOf(err); got != failure.CodeAdminConflict {
		t.Fatalf("expected %q, got %q", failure.CodeAdminConflict, got)
	}
}
