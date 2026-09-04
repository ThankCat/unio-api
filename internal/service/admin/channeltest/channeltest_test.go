package channeltest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	corechannel "github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	adminchannel "github.com/ThankCat/unio-gateway/internal/service/admin/channel"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	"github.com/ThankCat/unio-gateway/internal/service/subscription"
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

func (p *fakeProber) ProbeChannel(_ context.Context, _, _ string, _ corechannel.Runtime, model string) (adapter.ProbeResult, error) {
	p.calls++
	p.model = model
	return adapter.ProbeResult{StatusCode: p.status}, p.err
}

func rotationFixture() sqlc.PrepareChannelCredentialRotationRow {
	return sqlc.PrepareChannelCredentialRotationRow{
		ChannelID: 7, ProviderID: 2,
		Protocols: []string{"openai"}, AdapterKey: "openai", Credential: "sk-new",
		CredentialValid: false, ConfigRevision: 8, CredentialChanged: true,
		ProviderSlug: "openai", Origin: "https://api.example.test",
		OriginRevision: 3, StatusRevision: 4,
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
	if store.applyParam.ExpectedConfigRevision != 8 || store.applyParam.ExpectedOriginRevision != 3 || store.applyParam.ExpectedStatusRevision != 4 {
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

// fakeStreamProber 同时实现 Prober 与 StreamProber：流式路径回放固定文本增量。
type fakeStreamProber struct {
	fakeProber
	deltas      []string
	streamCalls int
}

func (p *fakeStreamProber) ProbeChannelStream(_ context.Context, _, _ string, _ corechannel.Runtime, model string, onDelta func(string)) (adapter.ProbeResult, error) {
	p.streamCalls++
	p.model = model
	for _, d := range p.deltas {
		onDelta(d)
	}
	return adapter.ProbeResult{StatusCode: p.status}, p.err
}

// TestStream：prober 支持流式时，事件时序冻结为 probe_start →（content×N），
// 检测结果照常落库（与非流式 Test 同一条编排）。
func TestTestStreamEmitsStartAndContentEvents(t *testing.T) {
	store := &fakeStore{
		probeSnapshot: permissionSnapshot(8), bindings: enabledBinding(),
		applied: sqlc.ApplyChannelProbeResultRow{ResultApplied: true, CurrentConfigRevision: 8},
	}
	prober := &fakeStreamProber{fakeProber: fakeProber{status: 200}, deltas: []string{"po", "ng"}}

	var events []ProbeEvent
	result, err := NewService(store, prober, nil).TestStream(context.Background(), TestInput{ChannelID: 7}, func(ev ProbeEvent) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("test stream: %v", err)
	}
	if !result.Success || result.TestedModel != "gpt-test" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if prober.streamCalls != 1 || prober.calls != 0 {
		t.Fatalf("stream-capable prober must use streaming path: stream=%d plain=%d", prober.streamCalls, prober.calls)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3 (start + 2 content): %+v", len(events), events)
	}
	if events[0].Type != ProbeEventStart || events[0].Model != "gpt-test" {
		t.Fatalf("first event must be probe_start with model: %+v", events[0])
	}
	if events[1].Type != ProbeEventContent || events[1].Text != "po" || events[2].Text != "ng" {
		t.Fatalf("content deltas must be forwarded in order: %+v", events[1:])
	}
	if store.applyCalls != 1 {
		t.Fatalf("stream test must persist result exactly once, got %d", store.applyCalls)
	}
}

// TestStream：prober 不支持 StreamProber 时回退非流式 ProbeChannel，仅有 probe_start 事件。
func TestTestStreamFallsBackToPlainProber(t *testing.T) {
	store := &fakeStore{
		probeSnapshot: permissionSnapshot(8), bindings: enabledBinding(),
		applied: sqlc.ApplyChannelProbeResultRow{ResultApplied: true, CurrentConfigRevision: 8},
	}
	prober := &fakeProber{status: 200}

	var events []ProbeEvent
	result, err := NewService(store, prober, nil).TestStream(context.Background(), TestInput{ChannelID: 7}, func(ev ProbeEvent) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("test stream: %v", err)
	}
	if !result.Success || prober.calls != 1 {
		t.Fatalf("fallback must probe once: result=%+v calls=%d", result, prober.calls)
	}
	if len(events) != 1 || events[0].Type != ProbeEventStart {
		t.Fatalf("plain prober must emit only probe_start: %+v", events)
	}
}

// Test（非流式入口）不受 StreamProber 影响：即使 prober 支持流式也走非流式路径，行为逐字节不变。
func TestPlainTestIgnoresStreamCapability(t *testing.T) {
	store := &fakeStore{
		probeSnapshot: permissionSnapshot(8), bindings: enabledBinding(),
		applied: sqlc.ApplyChannelProbeResultRow{ResultApplied: true, CurrentConfigRevision: 8},
	}
	prober := &fakeStreamProber{fakeProber: fakeProber{status: 200}}

	result, err := NewService(store, prober, nil).Test(context.Background(), TestInput{ChannelID: 7})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !result.Success || prober.streamCalls != 0 || prober.calls != 1 {
		t.Fatalf("plain Test must not use streaming path: stream=%d plain=%d", prober.streamCalls, prober.calls)
	}
}

func permissionSnapshot(configRevision int64) sqlc.GetChannelProbeSnapshotRow {
	return sqlc.GetChannelProbeSnapshotRow{
		ChannelID: 7, ProviderID: 2,
		Protocols: []string{"openai"}, AdapterKey: "openai", Credential: "test-secret", CredentialValid: true,
		ConfigRevision: configRevision, ProviderSlug: "openai", Origin: "https://api.example.test",
		OriginRevision: 3, StatusRevision: 4,
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
		OriginRevision: 3, ProviderStatusRevision: 4,
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
		log.TestedConfigRevision.Int64 != 8 || log.TestedOriginRevision.Int64 != 3 ||
		log.TestedStatusRevision.Int64 != 4 {
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

type fakeAccountResolver struct {
	identity subscription.ProbeIdentity
}

func (r fakeAccountResolver) ResolveProbeIdentity(context.Context, int64, int64) (subscription.ProbeIdentity, error) {
	return r.identity, nil
}

type fakeAccountHealth struct {
	successCalls      int
	observationCalls  int
	observedAccountID int64
	observedUsage     *adapter.AccountUsageFacts
}

func (h *fakeAccountHealth) RecordAccountSuccess(context.Context, int64, *adapter.AccountUsageFacts) {
	h.successCalls++
}

func (h *fakeAccountHealth) RecordAccountUsageObservation(_ context.Context, accountID int64, usage *adapter.AccountUsageFacts) {
	h.observationCalls++
	h.observedAccountID = accountID
	h.observedUsage = usage
}

type fakeAccountRuntime struct {
	calls      int
	accountID  int64
	durationMs int64
	// runtime 是 AccountRuntimeMany 回读返回的运行态（模拟处置落地后的 Redis 实况）。
	runtime    breakerstore.AccountRuntime
	runtimeErr error
}

func (r *fakeAccountRuntime) SetAccountCooldown(_ context.Context, accountID, durationMs int64, _ breakerstore.AccountUsageWindow) (int64, error) {
	r.calls++
	r.accountID = accountID
	r.durationMs = durationMs
	return durationMs, nil
}

func (r *fakeAccountRuntime) AccountRuntimeMany(_ context.Context, accountIDs []int64) ([]breakerstore.AccountRuntime, error) {
	if r.runtimeErr != nil {
		return nil, r.runtimeErr
	}
	out := make([]breakerstore.AccountRuntime, 0, len(accountIDs))
	for _, id := range accountIDs {
		runtime := r.runtime
		runtime.AccountID = id
		out = append(out, runtime)
	}
	return out, nil
}

func poolProbeSnapshot() sqlc.GetChannelProbeSnapshotRow {
	row := permissionSnapshot(8)
	row.SupplyForm = "pool"
	row.Credential = ""
	row.AdapterKey = "codex"
	return row
}

func poolProbeIdentity() subscription.ProbeIdentity {
	return subscription.ProbeIdentity{
		AccountID:         3,
		DisplayName:       "rakes-hurried-Oj@icloud.com",
		AccessToken:       "tok-probe",
		UpstreamAccountID: "b396c2bc-d4e2-4ab8-99f4-3e34aff25a41",
	}
}

// 池型检测拿到真实 429 时，必须把账号冷却与用量观测写进运行态——
// 只展示「被限流」却不改 Redis，管理页会一直显示「启用 · 正常」，下一笔客户请求也会再撞一次墙。
func TestPoolProbe429WritesAccountCooldownAndUsage(t *testing.T) {
	store := &fakeStore{
		probeSnapshot: poolProbeSnapshot(),
		bindings:      enabledBinding(),
		applied:       sqlc.ApplyChannelProbeResultRow{ResultApplied: true, CurrentConfigRevision: 8},
	}
	usage := &adapter.AccountUsageFacts{
		PlanType: "plus",
		Primary:  adapter.AccountUsageWindowFacts{Present: true, UsedPercent: 100, WindowMinutes: 180, ResetAtUnix: 1788499015},
	}
	prober := &fakeProber{
		status: 429,
		err: adapter.NewUpstreamError(
			adapter.UpstreamErrorRateLimit,
			adapter.UpstreamMetadata{
				StatusCode:      429,
				RetryAfter:      8800 * time.Second,
				AccountUsage:    usage,
				ResponseSnippet: `{"error":{"type":"usage_limit_reached","resets_in_seconds":8800}}`,
			},
			errors.New("upstream rate limited"),
		),
	}
	health := &fakeAccountHealth{}
	runtime := &fakeAccountRuntime{
		runtime: breakerstore.AccountRuntime{CooldownRemainingMs: 8800_000, CooldownWindow: breakerstore.AccountUsageWindowPrimary},
	}
	service := NewService(store, prober, nil).
		WithAccountResolver(fakeAccountResolver{identity: poolProbeIdentity()}).
		WithAccountHealth(health).
		WithAccountRuntime(runtime)

	result, err := service.Test(context.Background(), TestInput{ChannelID: 7, AccountID: 3})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if result.Success || result.ErrorCode != ErrCodeRateLimited || result.TestedAccountID != 3 {
		t.Fatalf("unexpected 429 result: %+v", result)
	}
	if runtime.calls != 1 || runtime.accountID != 3 || runtime.durationMs != 8800_000 {
		t.Fatalf("pool 429 must write account cooldown: %+v", runtime)
	}
	if health.successCalls != 0 {
		t.Fatalf("429 must not touch last_success: successCalls=%d", health.successCalls)
	}
	if health.observationCalls != 1 || health.observedAccountID != 3 || health.observedUsage != usage {
		t.Fatalf("pool 429 must write usage observation: %+v", health)
	}
	// 失败响应携带的水位与处置后的运行态必须随结果回给弹窗（否则「被限流」与列表状态对不上）。
	if result.AccountUsage != usage {
		t.Fatalf("429 result must carry account usage observation: %+v", result.AccountUsage)
	}
	if result.AccountRuntime == nil || result.AccountRuntime.CooldownRemainingMs != 8800_000 {
		t.Fatalf("429 result must carry post-feedback account runtime: %+v", result.AccountRuntime)
	}
}

func TestCredentialProbe429DoesNotWriteAccountRuntime(t *testing.T) {
	store := &fakeStore{
		probeSnapshot: permissionSnapshot(8),
		bindings:      enabledBinding(),
		applied:       sqlc.ApplyChannelProbeResultRow{ResultApplied: true, CurrentConfigRevision: 8},
	}
	runtime := &fakeAccountRuntime{}
	service := NewService(store, &fakeProber{
		status: 429,
		err: adapter.NewUpstreamError(
			adapter.UpstreamErrorRateLimit,
			adapter.UpstreamMetadata{StatusCode: 429, RetryAfter: 60 * time.Second},
			errors.New("upstream rate limited"),
		),
	}, nil).WithAccountRuntime(runtime)

	result, err := service.Test(context.Background(), TestInput{ChannelID: 7})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if result.Success || result.ErrorCode != ErrCodeRateLimited {
		t.Fatalf("unexpected credential 429 result: %+v", result)
	}
	if runtime.calls != 0 {
		t.Fatalf("credential 429 must not write account cooldown, calls=%d", runtime.calls)
	}
}

// 429 既无重置头也无可解析错误体（RetryAfter=0）时，必须落秒级兜底冷却而不是什么都不写——
// 否则又回到「检测到被限流、管理页仍显示正常」的老问题（与请求路径的渠道 429 策略同口径）。
func TestPoolProbe429WithoutResetSignalFallsBackToDefaultCooldown(t *testing.T) {
	store := &fakeStore{
		probeSnapshot: poolProbeSnapshot(),
		bindings:      enabledBinding(),
		applied:       sqlc.ApplyChannelProbeResultRow{ResultApplied: true, CurrentConfigRevision: 8},
	}
	runtime := &fakeAccountRuntime{}
	service := NewService(store, &fakeProber{
		status: 429,
		err: adapter.NewUpstreamError(
			adapter.UpstreamErrorRateLimit,
			adapter.UpstreamMetadata{StatusCode: 429},
			errors.New("upstream rate limited"),
		),
	}, nil).
		WithAccountResolver(fakeAccountResolver{identity: poolProbeIdentity()}).
		WithAccountRuntime(runtime)

	result, err := service.Test(context.Background(), TestInput{ChannelID: 7, AccountID: 3})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if result.Success || result.ErrorCode != ErrCodeRateLimited {
		t.Fatalf("unexpected 429 result: %+v", result)
	}
	wantMs := appsettings.DefaultChannelCooldownSettings().Cooldown.Milliseconds()
	if runtime.calls != 1 || runtime.accountID != 3 || runtime.durationMs != wantMs {
		t.Fatalf("429 without reset signal must fall back to default cooldown %dms: %+v", wantMs, runtime)
	}
}

// 探测成功必须清除残留 429 冷却（durationMs=0 覆盖清除）：付费即时重置后管理员重测通过，
// 页面与调度要立即回「正常」，不能等旧冷却自然到期。
func TestPoolProbeSuccessClearsAccountCooldown(t *testing.T) {
	store := &fakeStore{
		probeSnapshot: poolProbeSnapshot(),
		bindings:      enabledBinding(),
		applied:       sqlc.ApplyChannelProbeResultRow{ResultApplied: true, CurrentConfigRevision: 8},
	}
	// 模拟「水位满但上游仍放行最小请求」后的实况：成功 + 用量暂停并存。
	runtime := &fakeAccountRuntime{
		runtime: breakerstore.AccountRuntime{UsagePauseRemainingMs: 4_486_000, UsagePauseWindow: breakerstore.AccountUsageWindowPrimary},
	}
	service := NewService(store, &fakeProber{status: 200}, nil).
		WithAccountResolver(fakeAccountResolver{identity: poolProbeIdentity()}).
		WithAccountRuntime(runtime)

	result, err := service.Test(context.Background(), TestInput{ChannelID: 7, AccountID: 3})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !result.Success || result.TestedAccountID != 3 {
		t.Fatalf("unexpected success result: %+v", result)
	}
	if runtime.calls != 1 || runtime.accountID != 3 || runtime.durationMs != 0 {
		t.Fatalf("pool probe success must clear account cooldown: %+v", runtime)
	}
	// 「检测通过」也必须回读运行态：水位满触发的用量暂停要能在弹窗里解释清楚。
	if result.AccountRuntime == nil || result.AccountRuntime.UsagePauseRemainingMs != 4_486_000 {
		t.Fatalf("success result must carry post-probe account runtime: %+v", result.AccountRuntime)
	}
}
