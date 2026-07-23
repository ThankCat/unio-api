package channeltest

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	corechannel "github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	adminchannel "github.com/ThankCat/unio-gateway/internal/service/admin/channel"
)

type fakeStore struct {
	prepared           sqlc.PrepareChannelCredentialRotationRow
	prepareErr         error
	bindings           []sqlc.ListChannelModelsByChannelRow
	bindingsErr        error
	applied            sqlc.ApplyChannelProbeResultRow
	applyErr           error
	applyParam         sqlc.ApplyChannelProbeResultParams
	applyCalls         int
	current            sqlc.Channel
	probeSnapshot      sqlc.GetChannelProbeSnapshotRow
	probeSnapshots     []sqlc.GetChannelProbeSnapshotRow
	probeSnapshotCalls int
	permissionLogParam sqlc.InsertPermissionRecheckLogParams
	permissionLogRows  int64
	permissionLogErr   error
}

func (s *fakeStore) GetChannel(context.Context, int64) (sqlc.Channel, error) {
	return s.current, nil
}
func (s *fakeStore) GetChannelProbeSnapshot(context.Context, int64) (sqlc.GetChannelProbeSnapshotRow, error) {
	if len(s.probeSnapshots) > 0 {
		index := s.probeSnapshotCalls
		if index >= len(s.probeSnapshots) {
			index = len(s.probeSnapshots) - 1
		}
		s.probeSnapshotCalls++
		return s.probeSnapshots[index], nil
	}
	return s.probeSnapshot, nil
}
func (s *fakeStore) PrepareChannelCredentialRotation(context.Context, sqlc.PrepareChannelCredentialRotationParams) (sqlc.PrepareChannelCredentialRotationRow, error) {
	return s.prepared, s.prepareErr
}
func (s *fakeStore) ApplyChannelProbeResult(_ context.Context, arg sqlc.ApplyChannelProbeResultParams) (sqlc.ApplyChannelProbeResultRow, error) {
	s.applyCalls++
	s.applyParam = arg
	return s.applied, s.applyErr
}
func (s *fakeStore) InsertPermissionRecheckLog(_ context.Context, arg sqlc.InsertPermissionRecheckLogParams) (int64, error) {
	s.permissionLogParam = arg
	return s.permissionLogRows, s.permissionLogErr
}
func (s *fakeStore) ListChannelModelsByChannel(context.Context, int64) ([]sqlc.ListChannelModelsByChannelRow, error) {
	return s.bindings, s.bindingsErr
}
func (s *fakeStore) ListChannelTestLogsByChannel(context.Context, sqlc.ListChannelTestLogsByChannelParams) ([]sqlc.ChannelTestLog, error) {
	return nil, nil
}
func (s *fakeStore) CountChannelTestLogsByChannel(context.Context, int64) (int64, error) {
	return 0, nil
}

type fakeProber struct {
	status int
	err    error
	calls  int
	model  string
}

type credentialMetricsStub struct {
	states []string
}

func (m *credentialMetricsStub) IncChannelCredentialRotationVerification(state string) {
	m.states = append(m.states, state)
}

func (p *fakeProber) ProbeChannel(_ context.Context, _, _ string, _ corechannel.Runtime, model string) (int, error) {
	p.calls++
	p.model = model
	return p.status, p.err
}

func rotationFixture() sqlc.PrepareChannelCredentialRotationRow {
	return sqlc.PrepareChannelCredentialRotationRow{
		ChannelID: 7, ProviderID: 2, ProviderEndpointID: 3,
		Protocol: "openai", AdapterKey: "openai", Credential: "sk-new",
		CredentialValid: false, ConfigRevision: 8, CredentialChanged: true,
		ProviderSlug: "openai", EndpointBaseUrl: "https://api.example.test",
		EndpointBaseUrlRevision: 3, EndpointStatusRevision: 4,
	}
}

func enabledBinding() []sqlc.ListChannelModelsByChannelRow {
	return []sqlc.ListChannelModelsByChannelRow{{
		ChannelID: 7, UpstreamModel: "gpt-test", Status: "enabled", ModelExternalID: "openai/gpt-test",
	}}
}

func TestRotateCredentialNotRequiredSkipsProbe(t *testing.T) {
	prepared := rotationFixture()
	prepared.CredentialChanged = false
	prepared.CredentialValid = true
	store := &fakeStore{prepared: prepared}
	prober := &fakeProber{}
	metrics := &credentialMetricsStub{}
	service := NewService(store, prober, nil)
	service.SetMetrics(metrics)

	result, err := service.RotateCredentialAndTest(context.Background(), adminchannel.RotateCredentialInput{ID: 7, Credential: "sk-new"})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if result.Verification.State != adminchannel.CredentialVerificationNotRequired || !result.Verification.CredentialValidAfter {
		t.Fatalf("unexpected not-required result: %+v", result)
	}
	if prober.calls != 0 || store.applyCalls != 0 {
		t.Fatalf("not-required must not probe/apply: probe=%d apply=%d", prober.calls, store.applyCalls)
	}
	if len(metrics.states) != 1 || metrics.states[0] != string(adminchannel.CredentialVerificationNotRequired) {
		t.Fatalf("verification metrics=%v", metrics.states)
	}
}

func TestRotateCredentialPassedUsesPinnedRevisions(t *testing.T) {
	prepared := rotationFixture()
	store := &fakeStore{
		prepared: prepared, bindings: enabledBinding(),
		applied: sqlc.ApplyChannelProbeResultRow{
			ResultApplied: true, StateChangeApplied: true,
			CredentialValidAfter: true, CurrentConfigRevision: 9,
		},
	}
	prober := &fakeProber{status: 200}

	result, err := NewService(store, prober, nil).RotateCredentialAndTest(context.Background(), adminchannel.RotateCredentialInput{ID: 7, Credential: "sk-new"})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if result.Verification.State != adminchannel.CredentialVerificationPassed || result.CurrentConfigRevision != 9 || result.Verification.Result == nil {
		t.Fatalf("unexpected passed result: %+v", result)
	}
	if store.applyParam.ExpectedConfigRevision != 8 || store.applyParam.ExpectedEndpointBaseUrlRevision != 3 || store.applyParam.ExpectedEndpointStatusRevision != 4 {
		t.Fatalf("probe result did not use pinned revisions: %+v", store.applyParam)
	}
	if !store.applyParam.NextCredentialValid.Valid || !store.applyParam.NextCredentialValid.Bool {
		t.Fatalf("successful probe must request credential restoration: %+v", store.applyParam.NextCredentialValid)
	}
}

func TestRotateCredentialStaleDoesNotClaimStateChange(t *testing.T) {
	prepared := rotationFixture()
	store := &fakeStore{
		prepared: prepared, bindings: enabledBinding(),
		applied: sqlc.ApplyChannelProbeResultRow{
			ResultApplied: false, StateChangeApplied: false,
			CredentialValidAfter: false, CurrentConfigRevision: 10,
		},
	}

	result, err := NewService(store, &fakeProber{status: 200}, nil).RotateCredentialAndTest(context.Background(), adminchannel.RotateCredentialInput{ID: 7, Credential: "sk-new"})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if result.Verification.State != adminchannel.CredentialVerificationStale || result.Verification.StateChangeApplied || result.Verification.CredentialValidAfter {
		t.Fatalf("unexpected stale result: %+v", result)
	}
}

func TestRotateCredentialExecutionFailedKeepsSavedOutcome(t *testing.T) {
	prepared := rotationFixture()
	store := &fakeStore{
		prepared: prepared,
		current:  sqlc.Channel{ID: 7, ConfigRevision: 8, CredentialValid: false},
	}

	result, err := NewService(store, &fakeProber{}, nil).RotateCredentialAndTest(context.Background(), adminchannel.RotateCredentialInput{ID: 7, Credential: "sk-new"})
	if err != nil {
		t.Fatalf("post-save execution failure must stay HTTP-success shaped: %v", err)
	}
	if !result.CredentialSaved || result.Verification.State != adminchannel.CredentialVerificationExecutionFailed || result.Verification.CredentialValidAfter {
		t.Fatalf("unexpected execution-failed result: %+v", result)
	}
}

func TestRotateCredentialFailedProbeDoesNotRestoreCredential(t *testing.T) {
	prepared := rotationFixture()
	store := &fakeStore{
		prepared: prepared, bindings: enabledBinding(),
		applied: sqlc.ApplyChannelProbeResultRow{
			ResultApplied: true, CredentialValidAfter: false, CurrentConfigRevision: 8,
		},
	}

	result, err := NewService(store, &fakeProber{err: errors.New("malformed response")}, nil).RotateCredentialAndTest(context.Background(), adminchannel.RotateCredentialInput{ID: 7, Credential: "sk-new"})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if result.Verification.State != adminchannel.CredentialVerificationFailed || result.Verification.Result == nil || result.Verification.Result.Success {
		t.Fatalf("unexpected failed result: %+v", result)
	}
	if store.applyParam.NextCredentialValid != (pgtype.Bool{}) {
		t.Fatalf("non-auth failure must keep saved credential invalid without a second state transition: %+v", store.applyParam.NextCredentialValid)
	}
}

func permissionSnapshot(configRevision int64) sqlc.GetChannelProbeSnapshotRow {
	return sqlc.GetChannelProbeSnapshotRow{
		ChannelID: 7, ProviderID: 2, ProviderEndpointID: 3,
		Protocol: "openai", AdapterKey: "openai", Credential: "test-secret", CredentialValid: true,
		ConfigRevision: configRevision, ProviderSlug: "openai", EndpointBaseUrl: "https://api.example.test",
		EndpointBaseUrlRevision: 3, EndpointStatusRevision: 4,
	}
}

func permissionBinding() []sqlc.ListChannelModelsByChannelRow {
	return []sqlc.ListChannelModelsByChannelRow{
		{ChannelID: 7, ModelID: 76, UpstreamModel: "other-model", Status: "enabled", ModelExternalID: "openai/other"},
		{ChannelID: 7, ModelID: 77, UpstreamModel: "permission-model", Status: "enabled", ModelExternalID: "openai/permission"},
	}
}

func permissionInput() PermissionRecheckInput {
	return PermissionRecheckInput{
		ChannelID: 7, ModelID: 77, ChannelConfigRevision: 8,
		EndpointBaseURLRevision: 3, EndpointStatusRevision: 4,
	}
}

func TestPermissionRecheckUsesExactInternalModelAndOnlyWritesAudit(t *testing.T) {
	store := &fakeStore{
		probeSnapshot: permissionSnapshot(8), bindings: permissionBinding(), permissionLogRows: 1,
	}
	prober := &fakeProber{status: 200}
	result, err := NewService(store, prober, nil).RecheckPermission(context.Background(), permissionInput())
	if err != nil {
		t.Fatalf("permission recheck: %v", err)
	}
	if result.Stale || !result.Probe.Success || prober.model != "permission-model" {
		t.Fatalf("unexpected permission probe: result=%+v model=%q", result, prober.model)
	}
	if store.applyCalls != 0 {
		t.Fatalf("permission recheck must not call credential probe state writer, calls=%d", store.applyCalls)
	}
	log := store.permissionLogParam
	if !log.Success || log.ChannelID != 7 || log.TestedModel.String != "permission-model" ||
		log.TestedConfigRevision.Int64 != 8 || log.TestedEndpointBaseUrlRevision.Int64 != 3 ||
		log.TestedEndpointStatusRevision.Int64 != 4 {
		t.Fatalf("permission audit mismatch: %+v", log)
	}
}

func TestPermissionRecheck403DoesNotFlipChannelCredentialOrPersistBody(t *testing.T) {
	store := &fakeStore{
		probeSnapshot: permissionSnapshot(8), bindings: permissionBinding(), permissionLogRows: 1,
	}
	prober := &fakeProber{
		status: 403,
		err: adapter.NewUpstreamError(
			adapter.UpstreamErrorPermission,
			adapter.UpstreamMetadata{StatusCode: 403, ResponseSnippet: `{"error":"sensitive upstream body"}`},
			errors.New("upstream permission denied"),
		),
	}
	result, err := NewService(store, prober, nil).RecheckPermission(context.Background(), permissionInput())
	if err != nil {
		t.Fatalf("permission recheck: %v", err)
	}
	if result.Stale || result.Probe.Success || result.Probe.HTTPStatus != 403 || result.Probe.ErrorCode != ErrCodeCredentialInvalid {
		t.Fatalf("unexpected 403 result: %+v", result)
	}
	if result.Probe.UpstreamError != "" {
		t.Fatalf("permission recheck must scrub upstream response body")
	}
	if store.applyCalls != 0 {
		t.Fatalf("403 permission recheck must never flip channel credential_valid, apply calls=%d", store.applyCalls)
	}
	log := store.permissionLogParam
	if log.Success || log.HttpStatus.Int32 != 403 || log.ErrorCode.String != ErrCodeCredentialInvalid {
		t.Fatalf("403 permission audit mismatch: %+v", log)
	}
	// Dedicated audit params intentionally have no upstream_error field, so the response body cannot be persisted.
	if log.Message.String == "" || log.Message.String == `{"error":"sensitive upstream body"}` {
		t.Fatalf("permission audit must contain only classified message: %+v", log)
	}
}

func TestPermissionRecheckProbeBecomesStaleAndOnlyAudits(t *testing.T) {
	store := &fakeStore{
		probeSnapshots: []sqlc.GetChannelProbeSnapshotRow{permissionSnapshot(8), permissionSnapshot(9)},
		bindings:       permissionBinding(), permissionLogRows: 1,
	}
	result, err := NewService(store, &fakeProber{status: 200}, nil).RecheckPermission(context.Background(), permissionInput())
	if err != nil {
		t.Fatalf("permission recheck: %v", err)
	}
	if !result.Stale || !result.Probe.Success {
		t.Fatalf("successful but stale probe must stay audit-only: %+v", result)
	}
	if store.applyCalls != 0 || store.permissionLogParam.Message.String == "" {
		t.Fatalf("stale probe must only write explanatory audit: apply=%d log=%+v", store.applyCalls, store.permissionLogParam)
	}
}
