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
	inputEstimate int64,
) (*Store, *redis.Client, *AttemptPermit, map[string]string) {
	t.Helper()
	s, client, _ := newTestStore(t)
	const channelID = int64(921)
	seedAttemptControls(
		t,
		s,
		testConfig(),
		channelID,
		`{"concurrency":10}`,
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

// TestAttemptTerminalFreezesTPMStateWithoutChannelBuckets 冻结 §1.2/§8：Channel TPM 不再是准入门槛，
// Finish 不再占用/结算任何 Channel TPM 桶，只在 permit 上记录终态口径供审计。
func TestAttemptTerminalFreezesTPMStateWithoutChannelBuckets(t *testing.T) {
	for _, tc := range []struct {
		name      string
		actual    *int64
		wantState string
	}{
		{name: "reliable usage settles", actual: int64Pointer(160), wantState: "settled"},
		{name: "no reliable usage retains", actual: nil, wantState: "retained"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, client, permit, fields := acquireAttemptTPMContract(t, "attempt-terminal-"+tc.name, 100)
			// permit 不再冻结任何 Channel 三维桶 key。
			for _, field := range []string{"ch_rpm_bucket", "ch_rpd_bucket", "ch_tpm_bucket", "rpd_day_bucket"} {
				if value, ok := fields[field]; ok && value != "" {
					t.Fatalf("permit must not carry channel %s anymore, got %q", field, value)
				}
			}
			if _, err := s.Finish(context.Background(), *permit, FinishOutcome{
				ProviderOutcome: OutcomeIgnored, ChannelOutcome: OutcomeIgnored,
				RequestWriteState: RequestWriteCompleted, ActualTotalTokens: tc.actual,
			}); err != nil {
				t.Fatalf("attempt finish: %v", err)
			}
			if state := client.HGet(context.Background(), s.keys.permit(permit.PermitID), "tpm_state").Val(); state != tc.wantState {
				t.Fatalf("terminal tpm_state=%q want %q", state, tc.wantState)
			}
		})
	}
}

// TestAttemptConcurrencyDenialHasNoPartialResourceWrites 验证唯一的渠道级硬门槛（并发满）
// 返回 concurrency_full 且不留下任何部分写入。
func TestAttemptConcurrencyDenialHasNoPartialResourceWrites(t *testing.T) {
	s, client, _ := newTestStore(t)
	const channelID, providerID = int64(941), int64(943)
	seedAttemptControls(t, s, testConfig(), channelID, `{"concurrency":1}`)
	newInput := func(permitID string) AcquireAttemptInput {
		return withAttemptControlRevisions(AcquireAttemptInput{
			PermitID: permitID, AdmissionFingerprint: permitID + "-fp",
			RequestAdmissionID: "request-conc-full", RouteID: 942,
			ProviderID: providerID, ChannelID: channelID, OriginRevision: 1, ProviderStatusRevision: 1,
			ChannelConfigRevision: 1, ModelID: 944, UpstreamEndpoint: EndpointChatCompletions,
			RequestMode: ModeNonStream, InputEstimate: 100,
		})
	}
	held, err := acquireAttempt(t, s, newInput("conc-held"))
	if err != nil || held.Mode != AdmissionPermit {
		t.Fatalf("first acquire: admission=%+v err=%v", held, err)
	}

	denied := newInput("conc-denied")
	admission, err := acquireAttempt(t, s, denied)
	if err != nil || admission.Mode != AdmissionDenied || admission.Reason != ReasonConcurrencyFull {
		t.Fatalf("concurrency denial: admission=%+v err=%v", admission, err)
	}
	if exists := client.Exists(context.Background(), s.keys.permit(denied.PermitID)).Val(); exists != 0 {
		t.Fatalf("concurrency denial wrote a permit key")
	}
}

func int64Pointer(v int64) *int64 { return &v }

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
