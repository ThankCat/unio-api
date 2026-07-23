package breakerstore

import (
	"context"
	"strconv"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func limitOverride(value int64) *int64 { return &value }

func assertNoRequestResources(t *testing.T, s *Store, requestID string, routeID, userID int64) {
	t.Helper()
	patterns := []string{
		s.keys.admissionRequest(requestID),
		s.keys.base + "admission:v1:ru-rpm:" + i(routeID) + ":" + i(userID) + ":*",
		s.keys.base + "admission:v1:ru-rpd:" + i(routeID) + ":" + i(userID) + ":*",
		s.keys.base + "admission:v1:ru-tpm:" + i(routeID) + ":" + i(userID) + ":*",
		s.keys.requestConcurrency(routeID, userID),
	}
	for _, pattern := range patterns {
		keys, err := s.client.Keys(context.Background(), pattern).Result()
		if err != nil {
			t.Fatalf("list request resources %q: %v", pattern, err)
		}
		if len(keys) != 0 {
			t.Fatalf("denied request admission left resources for %q: %v", pattern, keys)
		}
	}
}

func assertNoAttemptResources(t *testing.T, s *Store, permitID string, endpointID, channelID int64) {
	t.Helper()
	patterns := []string{
		s.keys.permit(permitID),
		s.keys.endpoint(endpointID),
		s.keys.channel(channelID) + "*",
		s.keys.base + "admission:v1:ch-rpm:" + i(channelID) + ":*",
		s.keys.base + "admission:v1:ch-rpd:" + i(channelID) + ":*",
		s.keys.base + "admission:v1:ch-tpm:" + i(channelID) + ":*",
	}
	for _, pattern := range patterns {
		keys, err := s.client.Keys(context.Background(), pattern).Result()
		if err != nil {
			t.Fatalf("list attempt resources %q: %v", pattern, err)
		}
		if len(keys) != 0 {
			t.Fatalf("denied attempt left resources for %q: %v", pattern, keys)
		}
	}
}

func authoritativeAttemptInput(id string, endpointID, channelID int64) AcquireAttemptInput {
	return withAttemptControlRevisions(AcquireAttemptInput{
		PermitID:                id,
		AdmissionFingerprint:    id + "-fp",
		RequestAdmissionID:      "request-active",
		EndpointID:              endpointID,
		ChannelID:               channelID,
		EndpointBaseURLRevision: 1,
		EndpointStatusRevision:  1,
		ChannelConfigRevision:   1,
		ModelID:                 100,
		UpstreamOperation:       OpChatCompletions,
		RequestMode:             ModeNonStream,
		EstimatedInputTokens:    10,
	})
}

func TestRequestAdmissionMergesTrustedOverridesWithRedisDefaults(t *testing.T) {
	s, _, _ := newTestStore(t)
	epoch, revision := seedAdmissionEnvWithControls(
		t,
		s,
		`{"rpm":1,"tpm":100,"rpd":33}`,
		`{"key_limit":1,"channel_limit":0}`,
		testConfig(),
	)

	in := raInput("override-merge", 81, 82, epoch, revision)
	in.RPMLimitOverride = limitOverride(0)
	in.TPMLimitOverride = limitOverride(55)
	in.ConcurrencyLimitOverride = limitOverride(2)
	result, err := s.AcquireRequestAdmission(context.Background(), in)
	if err != nil || result.Outcome != RequestAllowed {
		t.Fatalf("override acquire want allowed, got %+v err=%v", result, err)
	}
	values, err := s.client.HMGet(context.Background(), s.keys.admissionRequest(in.RequestAdmissionID),
		"eff_rpm", "eff_rpd", "eff_tpm", "eff_concurrency").Result()
	if err != nil {
		t.Fatalf("read frozen effective limits: %v", err)
	}
	want := []interface{}{"0", "33", "55", "2"}
	for index := range want {
		if values[index] != want[index] {
			t.Fatalf("frozen effective limits want %v, got %v", want, values)
		}
	}
	if reserve, err := s.ReserveRequestTokens(context.Background(), in.RequestAdmissionID, 81, 82, 56,
		in.IntegrityEpoch, in.IntegrityRevision); err != nil || reserve != ReserveLimited {
		t.Fatalf("trusted TPM override must be enforced, got %s err=%v", reserve, err)
	}

	second := raInput("override-concurrency-2", 81, 82, epoch, revision)
	second.RPMLimitOverride = limitOverride(0)
	second.ConcurrencyLimitOverride = limitOverride(2)
	if got, err := s.AcquireRequestAdmission(context.Background(), second); err != nil || got.Outcome != RequestAllowed {
		t.Fatalf("second request under concurrency override want allowed, got %+v err=%v", got, err)
	}
	third := raInput("override-concurrency-3", 81, 82, epoch, revision)
	third.RPMLimitOverride = limitOverride(0)
	third.ConcurrencyLimitOverride = limitOverride(2)
	if got, err := s.AcquireRequestAdmission(context.Background(), third); err != nil ||
		got.Outcome != RequestLimited || got.LimitedDimension != "concurrency" {
		t.Fatalf("third request must enforce concurrency override, got %+v err=%v", got, err)
	}
}

func TestRequestAdmissionFreezesCommittedLifecycleWhileBreakerUpdateIsPending(t *testing.T) {
	s, _, _ := newTestStore(t)
	active := testConfig()
	epoch, revision := seedAdmissionEnvWithControls(
		t,
		s,
		`{"rpm":0,"tpm":0,"rpd":0}`,
		`{"key_limit":0,"channel_limit":0}`,
		active,
	)

	next := active
	next.AttemptPermitTTLMs = 60000
	next.AttemptRenewMs = 20000
	next.AttemptTerminalTTLMs = 600000
	nextPayload := testCircuitBreakerPayload(next)
	code, _, err := s.PrepareControl(
		context.Background(),
		s.SettingControl("gateway.circuit_breaker"),
		"request-lifecycle-update",
		1,
		2,
		nextPayload,
	)
	if err != nil || code != ControlPrepared {
		t.Fatalf("prepare breaker lifecycle update: code=%s err=%v", code, err)
	}

	assertLifecycle := func(requestID string, want Config) {
		t.Helper()
		result, acquireErr := s.AcquireRequestAdmission(
			context.Background(),
			raInput(requestID, 86, 87, epoch, revision),
		)
		if acquireErr != nil || result.Outcome != RequestAllowed {
			t.Fatalf("acquire %s want allowed, got %+v err=%v", requestID, result, acquireErr)
		}
		if result.RenewIntervalMs != want.AttemptRenewMs {
			t.Fatalf("returned renew interval want %d, got %d", want.AttemptRenewMs, result.RenewIntervalMs)
		}
		key := s.keys.admissionRequest(requestID)
		values, readErr := s.client.HMGet(context.Background(), key,
			"lease_ttl_ms", "renew_ms", "terminal_ttl_ms", "circuit_breaker_revision").Result()
		if readErr != nil {
			t.Fatalf("read frozen lifecycle: %v", readErr)
		}
		wantValues := []interface{}{
			strconv.FormatInt(want.AttemptPermitTTLMs, 10),
			strconv.FormatInt(want.AttemptRenewMs, 10),
			strconv.FormatInt(want.AttemptTerminalTTLMs, 10),
			nil,
		}
		for index := range wantValues {
			if values[index] != wantValues[index] {
				t.Fatalf("frozen lifecycle want %v, got %v", wantValues, values)
			}
		}
	}

	// Pending does not block request admission and still freezes the last committed lifecycle.
	assertLifecycle("request-lifecycle-pending", active)
	if committed, commitErr := s.CommitControl(
		context.Background(),
		s.SettingControl("gateway.circuit_breaker"),
		"request-lifecycle-update",
		nextPayload,
	); commitErr != nil || committed != 2 {
		t.Fatalf("commit breaker lifecycle update: revision=%d err=%v", committed, commitErr)
	}
	// New tokens after Commit freeze the newly active lifecycle without a caller breaker revision.
	assertLifecycle("request-lifecycle-committed", next)
}

func TestRequestAdmissionControlFailuresLeaveNoResources(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*testing.T, *Store, *RequestAdmissionInput)
		want       RequestAdmissionOutcome
		syncTarget string
	}{
		{
			name: "missing route rate",
			mutate: func(t *testing.T, s *Store, _ *RequestAdmissionInput) {
				t.Helper()
				if err := s.client.Del(context.Background(), s.keys.admissionRouteRate()).Err(); err != nil {
					t.Fatal(err)
				}
			},
			want: RequestRuntimeSyncReq, syncTarget: "route_rate",
		},
		{
			name: "pending route rate",
			mutate: func(t *testing.T, s *Store, _ *RequestAdmissionInput) {
				t.Helper()
				code, _, err := s.PrepareControl(context.Background(), s.RouteRateLimitControl(), "pending-route-rate",
					testRouteRateRevision, testRouteRateRevision+1, `{"rpm":2,"tpm":0,"rpd":0}`)
				if err != nil || code != ControlPrepared {
					t.Fatalf("prepare pending route rate: %s %v", code, err)
				}
			},
			want: RequestRuntimeSyncPending, syncTarget: "route_rate",
		},
		{
			name: "pending global concurrency",
			mutate: func(t *testing.T, s *Store, _ *RequestAdmissionInput) {
				t.Helper()
				code, _, err := s.PrepareControl(context.Background(), s.GlobalConcurrencyControl(), "pending-concurrency", 1, 2,
					`{"key_limit":2,"channel_limit":0}`)
				if err != nil || code != ControlPrepared {
					t.Fatalf("prepare pending control: %s %v", code, err)
				}
			},
			want: RequestRuntimeSyncPending, syncTarget: "global_concurrency",
		},
	}
	malformed := []struct {
		name   string
		target func(*Store) string
	}{
		{name: "malformed route rate", target: func(s *Store) string { return s.keys.admissionRouteRate() }},
		{name: "malformed global concurrency", target: func(s *Store) string { return s.keys.admissionGlobalConcurrency() }},
		{name: "malformed breaker", target: func(s *Store) string { return s.keys.runtimeControlSetting("gateway.circuit_breaker") }},
	}
	for _, tc := range malformed {
		target := tc.target
		tests = append(tests, struct {
			name       string
			mutate     func(*testing.T, *Store, *RequestAdmissionInput)
			want       RequestAdmissionOutcome
			syncTarget string
		}{
			name: tc.name,
			mutate: func(t *testing.T, s *Store, _ *RequestAdmissionInput) {
				t.Helper()
				if err := s.client.HSet(context.Background(), target(s), "active_payload", `{"unknown":1}`).Err(); err != nil {
					t.Fatal(err)
				}
			},
			want: RequestRuntimeSyncReq,
		})
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newTestStore(t)
			epoch, revision := seedAdmissionEnv(t, s)
			in := raInput("fail-closed", 91, 92, epoch, revision)
			tc.mutate(t, s, &in)
			result, err := s.AcquireRequestAdmission(context.Background(), in)
			if err != nil || result.Outcome != tc.want {
				t.Fatalf("want %s, got %+v err=%v", tc.want, result, err)
			}
			if tc.syncTarget != "" && result.SyncTarget != tc.syncTarget {
				t.Fatalf("want sync target %s, got %s", tc.syncTarget, result.SyncTarget)
			}
			assertNoRequestResources(t, s, in.RequestAdmissionID, in.RouteID, in.UserID)
		})
	}
}

func TestAttemptAndSnapshotUseChannelRateIndependentlyFromRouteRate(t *testing.T) {
	s, _, _ := newTestStore(t)
	const channelID int64 = 181
	const endpointID int64 = 1810
	seedAttemptControls(t, s, testConfig(), channelID, `{"rpm":null,"rpd":null,"tpm":null,"concurrency":null}`)

	code, _, err := s.PrepareControl(
		context.Background(), s.RouteRateLimitControl(), "route-rate-pending-during-attempt",
		testRouteRateRevision, testRouteRateRevision+1, `{"rpm":1,"tpm":10,"rpd":10}`,
	)
	if err != nil || code != ControlPrepared {
		t.Fatalf("prepare route rate: code=%s err=%v", code, err)
	}
	first, err := acquireAttempt(t, s, authoritativeAttemptInput("route-pending-attempt", endpointID, channelID))
	if err != nil || first.Mode != AdmissionPermit {
		t.Fatalf("route pending must not block attempt: result=%+v err=%v", first, err)
	}
	if err := s.Abort(context.Background(), *first.Permit); err != nil {
		t.Fatalf("abort route-pending attempt: %v", err)
	}

	code, _, err = s.PrepareControl(
		context.Background(), s.ChannelRateLimitControl(), "channel-rate-pending-during-attempt",
		testChannelRateRevision, testChannelRateRevision+1, `{"rpm":3,"tpm":30,"rpd":30}`,
	)
	if err != nil || code != ControlPrepared {
		t.Fatalf("prepare channel rate: code=%s err=%v", code, err)
	}
	secondInput := authoritativeAttemptInput("channel-pending-attempt", endpointID, channelID)
	second, err := acquireAttempt(t, s, secondInput)
	if err != nil || second.Mode != AdmissionDenied || second.Reason != ReasonRuntimeSyncPending {
		t.Fatalf("channel pending must block attempt: result=%+v err=%v", second, err)
	}

	_, err = s.SnapshotMany(context.Background(), SnapshotManyInput{
		IntegrityEpoch: testAttemptIntegrityEpoch, IntegrityRevision: testAttemptIntegrityRevision,
		ChannelRateRevision: testChannelRateRevision, GlobalConcurrencyRevision: 1,
		CircuitBreakerRevision: 1, RoutingBalanceRevision: 1, ModelID: 100,
		Candidates: []SnapshotCandidateInput{{
			EndpointID: endpointID, ChannelID: channelID,
			EndpointBaseURLRevision: 1, EndpointStatusRevision: 1,
			ChannelConfigRevision: 1, ChannelAdmissionRevision: 1,
		}},
	})
	if failure.CodeOf(err) != failure.CodeGatewayRuntimeSyncRequired {
		t.Fatalf("channel pending must block snapshot: code=%q err=%v", failure.CodeOf(err), err)
	}
}

func TestAttemptReadsAuthoritativeLimitsAndFailsClosedWithoutPartialWrites(t *testing.T) {
	t.Run("channel default RPM cannot be bypassed by route defaults", func(t *testing.T) {
		s, _, _ := newTestStore(t)
		cfg := testConfig()
		seedAttemptIntegrity(t, s)
		ensureTestControlAtRevision(t, s, s.RouteRateLimitControl(), testRouteRateRevision, `{"rpm":99,"tpm":9900,"rpd":990}`)
		ensureTestControlAtRevision(t, s, s.ChannelRateLimitControl(), testChannelRateRevision, `{"rpm":1,"tpm":0,"rpd":0}`)
		ensureTestControl(t, s, s.GlobalConcurrencyControl(), `{"key_limit":0,"channel_limit":0}`)
		ensureTestControl(t, s, s.SettingControl("gateway.circuit_breaker"), testCircuitBreakerPayload(cfg))
		ensureTestControl(t, s, s.ChannelAdmissionControl(101), `{"rpm":null,"rpd":null,"tpm":null,"concurrency":null}`)
		first, err := acquireAttempt(t, s, authoritativeAttemptInput("channel-default-rpm-1", 1010, 101))
		if err != nil || first.Mode != AdmissionPermit {
			t.Fatalf("first acquire want permit, got %+v err=%v", first, err)
		}
		second, err := acquireAttempt(t, s, authoritativeAttemptInput("channel-default-rpm-2", 1010, 101))
		if err != nil || second.Mode != AdmissionDenied || second.Reason != ReasonRateLimited {
			t.Fatalf("second acquire must enforce Redis channel-default RPM, got %+v err=%v", second, err)
		}
	})

	t.Run("explicit channel RPM zero overrides nonzero channel default", func(t *testing.T) {
		s, _, _ := newTestStore(t)
		cfg := testConfig()
		const channelID int64 = 102
		const endpointID int64 = 1020

		seedAttemptIntegrity(t, s)
		ensureTestControlAtRevision(t, s, s.RouteRateLimitControl(), testRouteRateRevision, `{"rpm":99,"tpm":9900,"rpd":990}`)
		ensureTestControlAtRevision(t, s, s.ChannelRateLimitControl(), testChannelRateRevision, `{"rpm":1,"tpm":0,"rpd":0}`)
		ensureTestControl(t, s, s.GlobalConcurrencyControl(), `{"key_limit":0,"channel_limit":0}`)
		ensureTestControl(t, s, s.SettingControl("gateway.circuit_breaker"), testCircuitBreakerPayload(cfg))
		ensureTestControl(t, s, s.SettingControl("gateway.routing_balance"), testRoutingBalancePayload)
		ensureTestControl(t, s, s.ChannelAdmissionControl(channelID), `{"rpm":0,"rpd":null,"tpm":null,"concurrency":null}`)
		if created, err := s.InitEndpointControl(context.Background(), endpointID, 1, 1, "enabled"); err != nil || !created {
			t.Fatalf("init endpoint: created=%v err=%v", created, err)
		}

		acquireUnlimited := func(id string) AttemptAdmission {
			t.Helper()
			in := authoritativeAttemptInput(id, endpointID, channelID)
			in.EnforceEndpointControl = true
			admission, err := acquireAttempt(t, s, in)
			if err != nil || admission.Mode != AdmissionPermit {
				t.Fatalf("acquire %s must honor explicit unlimited RPM, got %+v err=%v", id, admission, err)
			}
			return admission
		}

		first := acquireUnlimited("channel-explicit-unlimited-rpm-1")
		second := acquireUnlimited("channel-explicit-unlimited-rpm-2")
		snapshot, err := s.SnapshotMany(context.Background(), SnapshotManyInput{
			IntegrityEpoch: testAttemptIntegrityEpoch, IntegrityRevision: testAttemptIntegrityRevision,
			ChannelRateRevision: testChannelRateRevision, GlobalConcurrencyRevision: 1,
			CircuitBreakerRevision: 1, RoutingBalanceRevision: 1, ModelID: 100,
			Candidates: []SnapshotCandidateInput{{
				EndpointID: endpointID, ChannelID: channelID,
				EndpointBaseURLRevision: 1, EndpointStatusRevision: 1,
				ChannelConfigRevision: 1, ChannelAdmissionRevision: 1,
			}},
		})
		if err != nil {
			t.Fatalf("snapshot explicit unlimited channel: %v", err)
		}
		got := snapshot.Candidates[0]
		if got.Status != CandidateSnapshotCurrent || got.RPM.Used != 2 || got.RPM.Limit != 0 {
			t.Fatalf("snapshot must expose explicit unlimited RPM with two active permits, got status=%s rpm=%+v", got.Status, got.RPM)
		}

		if err := s.Abort(context.Background(), *first.Permit); err != nil {
			t.Fatalf("abort first permit: %v", err)
		}
		if err := s.Abort(context.Background(), *second.Permit); err != nil {
			t.Fatalf("abort second permit: %v", err)
		}
	})

	malformedTargets := []struct {
		name   string
		target func(*Store, int64) string
	}{
		{name: "channel rate", target: func(s *Store, _ int64) string { return s.keys.admissionChannelRate() }},
		{name: "global concurrency", target: func(s *Store, _ int64) string { return s.keys.admissionGlobalConcurrency() }},
		{name: "channel admission", target: func(s *Store, channelID int64) string { return s.keys.admissionChannel(channelID) }},
		{name: "circuit breaker", target: func(s *Store, _ int64) string { return s.keys.runtimeControlSetting("gateway.circuit_breaker") }},
	}
	for index, tc := range malformedTargets {
		t.Run("malformed "+tc.name, func(t *testing.T) {
			s, _, _ := newTestStore(t)
			cfg := testConfig()
			channelID := int64(200 + index)
			endpointID := int64(2000 + index)
			seedAttemptControls(t, s, cfg, channelID, `{"rpm":null,"rpd":null,"tpm":null,"concurrency":null}`)
			if err := s.client.HSet(context.Background(), tc.target(s, channelID), "active_payload", `{"unknown":1}`).Err(); err != nil {
				t.Fatal(err)
			}
			in := authoritativeAttemptInput("malformed-attempt", endpointID, channelID)
			result, err := acquireAttempt(t, s, in)
			if err != nil || result.Mode != AdmissionDenied || result.Reason != ReasonRuntimeSyncRequired {
				t.Fatalf("malformed control must fail closed, got %+v err=%v", result, err)
			}
			assertNoAttemptResources(t, s, in.PermitID, endpointID, channelID)
		})
	}
}

func TestCommittedBreakerConfigDrivesFinish(t *testing.T) {
	s, _, _ := newTestStore(t)
	active := testConfig()
	opened := false
	for attempt := 0; attempt < 10; attempt++ {
		admission := acquire(t, s, active, "breaker-authority-"+i(int64(attempt)), 303, 3030)
		if admission.Mode == AdmissionDenied && admission.Reason == ReasonOpen {
			opened = true
			break
		}
		if admission.Mode != AdmissionPermit {
			t.Fatalf("attempt %d want permit or open, got %+v", attempt, admission)
		}
		if _, err := s.Finish(context.Background(), *admission.Permit, FinishOutcome{
			EndpointOutcome: OutcomeIgnored,
			ChannelOutcome:  OutcomeEligibleFailure,
		}); err != nil {
			t.Fatalf("finish attempt %d: %v", attempt, err)
		}
	}
	if !opened {
		t.Fatal("Redis active breaker did not open when caller supplied Enabled=false")
	}
}
