package breakerstore

import (
	"context"
	"strconv"
	"testing"
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

func assertNoAttemptResources(t *testing.T, s *Store, permitID string, providerID, channelID int64) {
	t.Helper()
	patterns := []string{
		s.keys.permit(permitID),
		s.keys.provider(providerID),
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

func authoritativeAttemptInput(id string, providerID, channelID int64) AcquireAttemptInput {
	return withAttemptControlRevisions(AcquireAttemptInput{
		PermitID:               id,
		AdmissionFingerprint:   id + "-fp",
		RequestAdmissionID:     "request-active",
		ProviderID:             providerID,
		ChannelID:              channelID,
		OriginRevision:         1,
		ProviderStatusRevision: 1,
		ChannelConfigRevision:  1,
		ModelID:                100,
		UpstreamEndpoint:       EndpointChatCompletions,
		RequestMode:            ModeNonStream,
		InputEstimate:          10,
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

func TestAttemptReadsAuthoritativeLimitsAndFailsClosedWithoutPartialWrites(t *testing.T) {
	malformedTargets := []struct {
		name   string
		target func(*Store, int64) string
	}{
		{name: "global concurrency", target: func(s *Store, _ int64) string { return s.keys.admissionGlobalConcurrency() }},
		{name: "channel capacity", target: func(s *Store, channelID int64) string { return s.keys.channelCapacity(channelID) }},
		{name: "circuit breaker", target: func(s *Store, _ int64) string { return s.keys.runtimeControlSetting("gateway.circuit_breaker") }},
	}
	for index, tc := range malformedTargets {
		t.Run("malformed "+tc.name, func(t *testing.T) {
			s, _, _ := newTestStore(t)
			cfg := testConfig()
			channelID := int64(200 + index)
			providerID := int64(2000 + index)
			seedAttemptControls(t, s, cfg, channelID, `{"concurrency":null}`)
			if err := s.client.HSet(context.Background(), tc.target(s, channelID), "active_payload", `{"unknown":1}`).Err(); err != nil {
				t.Fatal(err)
			}
			in := authoritativeAttemptInput("malformed-attempt", providerID, channelID)
			result, err := acquireAttempt(t, s, in)
			if err != nil || result.Mode != AdmissionDenied || result.Reason != ReasonRuntimeSyncRequired {
				t.Fatalf("malformed control must fail closed, got %+v err=%v", result, err)
			}
			assertNoAttemptResources(t, s, in.PermitID, providerID, channelID)
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
			ProviderOutcome:   OutcomeIgnored,
			ChannelOutcome:    OutcomeEligibleFailure,
			RequestWriteState: RequestWriteCompleted,
		}); err != nil {
			t.Fatalf("finish attempt %d: %v", attempt, err)
		}
	}
	if !opened {
		t.Fatal("Redis active breaker did not open when caller supplied Enabled=false")
	}
}
