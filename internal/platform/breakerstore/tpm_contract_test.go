package breakerstore

import (
	"context"
	"fmt"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func acquireRequestTPMContract(
	t *testing.T,
	requestID string,
	inputEstimate, tpmLimit int64,
) (*Store, *redis.Client, string, int64, string, string) {
	t.Helper()
	s, client, _ := newTestStore(t)
	epoch, revision := seedAdmissionEnvWithControls(
		t,
		s,
		fmt.Sprintf(`{"rpm":100,"tpm":%d,"rpd":100}`, tpmLimit),
		`{"key_limit":0,"channel_limit":0}`,
		testConfig(),
	)
	const routeID, userID = int64(901), int64(902)
	result, err := s.AcquireRequestAdmission(
		context.Background(),
		raInput(requestID, routeID, userID, epoch, revision),
	)
	if err != nil || result.Outcome != RequestAllowed {
		t.Fatalf("request acquire: result=%+v err=%v", result, err)
	}
	reserve, err := s.ReserveRequestTokens(
		context.Background(), requestID, routeID, userID, inputEstimate, epoch, revision,
	)
	if err != nil || reserve != ReserveReserved {
		t.Fatalf("request reserve input=%d: result=%s err=%v", inputEstimate, reserve, err)
	}
	tokenKey := s.keys.admissionRequest(requestID)
	bucketKey, err := client.HGet(context.Background(), tokenKey, "tpm_bucket").Result()
	if err != nil {
		t.Fatalf("read frozen request TPM bucket: %v", err)
	}
	return s, client, epoch, revision, tokenKey, bucketKey
}

func TestRequestTPMSettlesReliableActualAgainstInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		actual int64
	}{
		{name: "smaller", actual: 40},
		{name: "equal", actual: 100},
		{name: "larger", actual: 160},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestID := "request-settle-" + tc.name
			s, client, epoch, revision, tokenKey, bucketKey := acquireRequestTPMContract(t, requestID, 100, 1000)
			outcome, err := s.FinishRequestAdmission(
				context.Background(), requestID, 901, 902, tc.actual, "actual", epoch, revision,
			)
			if err != nil || outcome != RequestLifecycleFinished {
				t.Fatalf("request finish actual=%d: outcome=%s err=%v", tc.actual, outcome, err)
			}
			if used := client.Get(context.Background(), bucketKey).Val(); used != fmt.Sprint(tc.actual) {
				t.Fatalf("request TPM after actual=%d is %q", tc.actual, used)
			}
			fields := client.HMGet(context.Background(), tokenKey, "tpm_state", "tpm_actual_total", "tpm_terminal_reason").Val()
			if fmt.Sprint(fields[0]) != "settled" || fmt.Sprint(fields[1]) != fmt.Sprint(tc.actual) || fmt.Sprint(fields[2]) != "actual" {
				t.Fatalf("request terminal TPM fields=%v", fields)
			}

			repeated, err := s.FinishRequestAdmission(
				context.Background(), requestID, 901, 902, tc.actual+1, "actual", epoch, revision,
			)
			if err != nil || repeated != RequestLifecycleTerminal {
				t.Fatalf("repeated request finish: outcome=%s err=%v", repeated, err)
			}
			if used := client.Get(context.Background(), bucketKey).Val(); used != fmt.Sprint(tc.actual) {
				t.Fatalf("repeated request finish changed TPM to %q", used)
			}
		})
	}
}

func TestRequestTPMRetainsReleasesAndIgnoresExpiredBucket(t *testing.T) {
	for _, tc := range []struct {
		name         string
		reason       string
		wantState    string
		wantUsed     string
		expireBucket bool
		floorBucket  bool
	}{
		{name: "not reached releases", reason: "not_reached", wantState: "released", wantUsed: "0"},
		{name: "reached retains", reason: "reached_without_usage", wantState: "retained", wantUsed: "100"},
		{name: "uncertain retains", reason: "uncertain", wantState: "retained", wantUsed: "100"},
		{name: "release floors zero", reason: "not_reached", wantState: "released", wantUsed: "0", floorBucket: true},
		{name: "expired release no-op", reason: "not_reached", wantState: "released", expireBucket: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestID := "request-terminal-" + tc.name
			s, client, epoch, revision, tokenKey, bucketKey := acquireRequestTPMContract(t, requestID, 100, 1000)
			if tc.floorBucket {
				if err := client.Set(context.Background(), bucketKey, 20, 0).Err(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.expireBucket {
				if err := client.Del(context.Background(), bucketKey).Err(); err != nil {
					t.Fatal(err)
				}
			}
			outcome, err := s.FinishRequestAdmission(
				context.Background(), requestID, 901, 902, -1, tc.reason, epoch, revision,
			)
			if err != nil || outcome != RequestLifecycleFinished {
				t.Fatalf("request finish reason=%s: outcome=%s err=%v", tc.reason, outcome, err)
			}
			if state := client.HGet(context.Background(), tokenKey, "tpm_state").Val(); state != tc.wantState {
				t.Fatalf("request TPM state=%q, want %q", state, tc.wantState)
			}
			if tc.expireBucket {
				if exists := client.Exists(context.Background(), bucketKey).Val(); exists != 0 {
					t.Fatal("expired request TPM bucket was recreated")
				}
			} else if used := client.Get(context.Background(), bucketKey).Val(); used != tc.wantUsed {
				t.Fatalf("request TPM=%q, want %q", used, tc.wantUsed)
			}
		})
	}
}

func TestRequestTPMLimitedPreservesIngressRPMAndRPD(t *testing.T) {
	s, client, _ := newTestStore(t)
	epoch, revision := seedAdmissionEnvWithControls(
		t, s, `{"rpm":10,"tpm":50,"rpd":10}`, `{"key_limit":0,"channel_limit":0}`, testConfig(),
	)
	const requestID = "request-tpm-limited"
	const routeID, userID = int64(911), int64(912)
	result, err := s.AcquireRequestAdmission(context.Background(), raInput(requestID, routeID, userID, epoch, revision))
	if err != nil || result.Outcome != RequestAllowed {
		t.Fatalf("request acquire: result=%+v err=%v", result, err)
	}
	tokenKey := s.keys.admissionRequest(requestID)
	resourceKeys := client.HMGet(context.Background(), tokenKey, "rpm_bucket", "rpd_bucket").Val()
	reserve, err := s.ReserveRequestTokens(context.Background(), requestID, routeID, userID, 100, epoch, revision)
	if err != nil || reserve != ReserveLimited {
		t.Fatalf("request TPM limit: result=%s err=%v", reserve, err)
	}
	if rpm := client.Get(context.Background(), fmt.Sprint(resourceKeys[0])).Val(); rpm != "1" {
		t.Fatalf("limited request RPM=%q, want 1", rpm)
	}
	if rpd := client.Get(context.Background(), fmt.Sprint(resourceKeys[1])).Val(); rpd != "1" {
		t.Fatalf("limited request RPD=%q, want 1", rpd)
	}
	fields := client.HMGet(context.Background(), tokenKey, "tpm_state", "tpm_input_estimate").Val()
	if fmt.Sprint(fields[0]) != "limited" || fmt.Sprint(fields[1]) != "100" {
		t.Fatalf("limited request TPM fields=%v", fields)
	}
}

func acquireAttemptTPMContract(
	t *testing.T,
	permitID string,
	inputEstimate, tpmLimit int64,
) (*Store, *redis.Client, *AttemptPermit, map[string]string) {
	t.Helper()
	s, client, _ := newTestStore(t)
	const channelID = int64(921)
	seedAttemptControls(
		t,
		s,
		testConfig(),
		channelID,
		fmt.Sprintf(`{"rpm":100,"rpd":100,"tpm":%d,"concurrency":10}`, tpmLimit),
	)
	admission, err := acquireAttempt(t, s, withAttemptControlRevisions(AcquireAttemptInput{
		PermitID: permitID, AdmissionFingerprint: permitID + "-fp", RequestAdmissionID: "request-" + permitID,
		RouteID: 922, ProviderID: 923, ChannelID: channelID,
		OriginRevision: 1, ProviderStatusRevision: 1, ChannelConfigRevision: 1,
		ModelID: 924, UpstreamEndpoint: EndpointChatCompletions, RequestMode: ModeNonStream,
		InputEstimate: inputEstimate,
	}))
	if err != nil || admission.Mode != AdmissionPermit || admission.Permit == nil {
		t.Fatalf("attempt acquire input=%d: admission=%+v err=%v", inputEstimate, admission, err)
	}
	permitKey := s.keys.permit(permitID)
	fields, err := client.HGetAll(context.Background(), permitKey).Result()
	if err != nil {
		t.Fatalf("read attempt permit: %v", err)
	}
	return s, client, admission.Permit, fields
}

func TestAttemptTPMSettlesReliableActualAgainstInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		actual int64
	}{
		{name: "smaller", actual: 40},
		{name: "equal", actual: 100},
		{name: "larger", actual: 160},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, client, permit, fields := acquireAttemptTPMContract(t, "attempt-settle-"+tc.name, 100, 1000)
			actual := tc.actual
			first, err := s.Finish(context.Background(), *permit, FinishOutcome{
				ProviderOutcome: OutcomeIgnored, ChannelOutcome: OutcomeIgnored,
				RequestWriteState: RequestWriteCompleted, ActualTotalTokens: &actual,
			})
			if err != nil {
				t.Fatalf("attempt finish actual=%d: result=%+v err=%v", actual, first, err)
			}
			bucketKey := fields["ch_tpm_bucket"]
			if used := client.Get(context.Background(), bucketKey).Val(); used != fmt.Sprint(actual) {
				t.Fatalf("attempt TPM after actual=%d is %q", actual, used)
			}
			terminal := client.HMGet(context.Background(), s.keys.permit(permit.PermitID), "tpm_state", "tpm_actual_total").Val()
			if fmt.Sprint(terminal[0]) != "settled" || fmt.Sprint(terminal[1]) != fmt.Sprint(actual) {
				t.Fatalf("attempt terminal TPM fields=%v", terminal)
			}

			otherActual := actual + 1
			second, err := s.Finish(context.Background(), *permit, FinishOutcome{
				ProviderOutcome: OutcomeEligibleFailure, ChannelOutcome: OutcomeEligibleFailure,
				RequestWriteState: RequestWriteCompleted, ActualTotalTokens: &otherActual,
			})
			if err != nil || second != first {
				t.Fatalf("repeated attempt finish: first=%+v second=%+v err=%v", first, second, err)
			}
			if used := client.Get(context.Background(), bucketKey).Val(); used != fmt.Sprint(actual) {
				t.Fatalf("repeated attempt finish changed TPM to %q", used)
			}
		})
	}
}

func TestAttemptTPMRetainsInputWithoutReliableUsage(t *testing.T) {
	for _, tc := range []struct {
		name            string
		writeState      RequestWriteState
		responseHeaders bool
		channelOutcome  Outcome
	}{
		{name: "uncertain transport", writeState: RequestWriteUncertain, channelOutcome: OutcomeIgnored},
		{name: "http 4xx", writeState: RequestWriteNotStarted, responseHeaders: true, channelOutcome: OutcomeIgnored},
		{name: "http 5xx", writeState: RequestWriteCompleted, responseHeaders: true, channelOutcome: OutcomeEligibleFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, client, permit, fields := acquireAttemptTPMContract(t, "attempt-retain-"+tc.name, 100, 1000)
			_, err := s.Finish(context.Background(), *permit, FinishOutcome{
				ProviderOutcome: OutcomeIgnored, ChannelOutcome: tc.channelOutcome,
				RequestWriteState: tc.writeState, ResponseHeadersReceived: tc.responseHeaders,
			})
			if err != nil {
				t.Fatalf("attempt finish without usage: %v", err)
			}
			if used := client.Get(context.Background(), fields["ch_tpm_bucket"]).Val(); used != "100" {
				t.Fatalf("retained attempt TPM=%q, want 100", used)
			}
			if state := client.HGet(context.Background(), s.keys.permit(permit.PermitID), "tpm_state").Val(); state != "retained" {
				t.Fatalf("retained attempt TPM state=%q", state)
			}
		})
	}
}

func TestAttemptAbortReleasesWithFloorAndIsFirstTerminal(t *testing.T) {
	s, client, permit, fields := acquireAttemptTPMContract(t, "attempt-abort-floor", 100, 1000)
	for field, value := range map[string]int64{
		"ch_rpm_bucket": 0,
		"ch_rpd_bucket": 0,
		"ch_tpm_bucket": 20,
	} {
		if err := client.Set(context.Background(), fields[field], value, 0).Err(); err != nil {
			t.Fatalf("seed %s: %v", field, err)
		}
	}
	if err := s.Abort(context.Background(), *permit); err != nil {
		t.Fatalf("attempt abort: %v", err)
	}
	for _, field := range []string{"ch_rpm_bucket", "ch_rpd_bucket", "ch_tpm_bucket"} {
		if used := client.Get(context.Background(), fields[field]).Val(); used != "0" {
			t.Fatalf("aborted %s=%q, want floor 0", field, used)
		}
	}
	if state := client.HGet(context.Background(), s.keys.permit(permit.PermitID), "tpm_state").Val(); state != "released" {
		t.Fatalf("aborted attempt TPM state=%q", state)
	}
	if err := s.Abort(context.Background(), *permit); err != nil {
		t.Fatalf("repeated attempt abort: %v", err)
	}
	if _, err := s.Finish(context.Background(), *permit, FinishOutcome{
		ProviderOutcome: OutcomeIgnored, ChannelOutcome: OutcomeIgnored, RequestWriteState: RequestWriteCompleted,
	}); err != nil {
		t.Fatalf("finish after abort should be an idempotent terminal result: %v", err)
	}
	if used := client.Get(context.Background(), fields["ch_tpm_bucket"]).Val(); used != "0" {
		t.Fatalf("terminal retries changed released TPM to %q", used)
	}
}

func TestAttemptTerminalDoesNotRecreateExpiredMinuteBuckets(t *testing.T) {
	for _, terminal := range []string{"finish", "abort"} {
		t.Run(terminal, func(t *testing.T) {
			s, client, permit, fields := acquireAttemptTPMContract(t, "attempt-expired-"+terminal, 100, 1000)
			if err := client.Del(context.Background(), fields["ch_rpm_bucket"], fields["ch_tpm_bucket"]).Err(); err != nil {
				t.Fatal(err)
			}
			if terminal == "finish" {
				actual := int64(160)
				if _, err := s.Finish(context.Background(), *permit, FinishOutcome{
					ProviderOutcome: OutcomeIgnored, ChannelOutcome: OutcomeIgnored,
					RequestWriteState: RequestWriteCompleted, ActualTotalTokens: &actual,
				}); err != nil {
					t.Fatalf("finish with expired minute buckets: %v", err)
				}
			} else if err := s.Abort(context.Background(), *permit); err != nil {
				t.Fatalf("abort with expired minute buckets: %v", err)
			}
			if existing := client.Exists(context.Background(), fields["ch_rpm_bucket"], fields["ch_tpm_bucket"]).Val(); existing != 0 {
				t.Fatalf("%s recreated %d expired minute buckets", terminal, existing)
			}
		})
	}
}

func TestAttemptTPMLimitDenialHasNoPartialResourceWrites(t *testing.T) {
	s, client, _ := newTestStore(t)
	const channelID = int64(931)
	seedAttemptControls(t, s, testConfig(), channelID, `{"rpm":100,"rpd":100,"tpm":50,"concurrency":10}`)
	in := withAttemptControlRevisions(AcquireAttemptInput{
		PermitID: "attempt-tpm-denied", AdmissionFingerprint: "attempt-tpm-denied-fp",
		RequestAdmissionID: "request-attempt-tpm-denied", RouteID: 932,
		ProviderID: 933, ChannelID: channelID, OriginRevision: 1, ProviderStatusRevision: 1,
		ChannelConfigRevision: 1, ModelID: 934, UpstreamEndpoint: EndpointChatCompletions,
		RequestMode: ModeNonStream, InputEstimate: 100,
	})
	admission, err := acquireAttempt(t, s, in)
	if err != nil || admission.Mode != AdmissionDenied || admission.Reason != ReasonRateLimited {
		t.Fatalf("attempt TPM denial: admission=%+v err=%v", admission, err)
	}
	for _, pattern := range []string{
		s.keys.channelRPMBucketPrefix(channelID) + "*",
		s.keys.channelRPDBucketPrefix(channelID) + "*",
		s.keys.channelTPMBucketPrefix(channelID) + "*",
	} {
		if keys := client.Keys(context.Background(), pattern).Val(); len(keys) != 0 {
			t.Fatalf("attempt denial partially wrote resource keys %v", keys)
		}
	}
	if exists := client.Exists(
		context.Background(),
		s.keys.permit(in.PermitID),
		s.keys.channel(channelID),
		s.keys.channel(channelID)+":conc",
	).Val(); exists != 0 {
		t.Fatalf("attempt denial partially wrote %d permit/state/concurrency keys", exists)
	}
}

func TestTPMInputsRejectLuaInexactValuesBeforeRedis(t *testing.T) {
	s := &Store{}
	if _, err := s.ReserveRequestTokens(context.Background(), "request", 1, 1, maxLuaExactInteger+1, "epoch", 1); failure.CodeOf(err) != failure.CodeConfigInvalid {
		t.Fatalf("Lua-inexact request estimate error=%v", err)
	}
	if _, err := s.FinishRequestAdmission(context.Background(), "request", 1, 1, maxLuaExactInteger+1, "actual", "epoch", 1); failure.CodeOf(err) != failure.CodeConfigInvalid {
		t.Fatalf("Lua-inexact request actual error=%v", err)
	}
	if _, err := s.FinishRequestAdmission(context.Background(), "request", 1, 1, -2, "not_reached", "epoch", 1); failure.CodeOf(err) != failure.CodeConfigInvalid {
		t.Fatalf("request unavailable sentinel error=%v", err)
	}
	if _, err := s.AcquireAttempt(context.Background(), withAttemptControlRevisions(AcquireAttemptInput{
		PermitID: "permit", AdmissionFingerprint: "fingerprint", RequestAdmissionID: "request",
		ProviderID: 1, ChannelID: 1, OriginRevision: 1, ProviderStatusRevision: 1,
		ChannelConfigRevision: 1, ModelID: 1, UpstreamEndpoint: EndpointChatCompletions,
		RequestMode: ModeNonStream, InputEstimate: maxLuaExactInteger + 1,
	})); failure.CodeOf(err) != failure.CodeConfigInvalid {
		t.Fatalf("Lua-inexact attempt estimate error=%v", err)
	}
}
