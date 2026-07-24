package breakerstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func TestRuntimeInfrastructureFaultLatchIsSharedAndRequiresExplicitCASClear(t *testing.T) {
	storeA, client, namespace := newTestStore(t)
	storeB := NewStore(client, namespace)
	ctx := context.Background()
	channelID := int64(7202)
	originID := int64(7201)
	seedAttemptControls(t, storeA, testConfig(), channelID,
		`{"rpm":null,"rpd":null,"tpm":null,"concurrency":null}`)

	// A request-time WRONGTYPE is a confirmed Store failure. SnapshotMany is before authorization
	// and transport, so this request cannot reach an upstream.
	if err := client.Set(ctx, storeA.keys.origin(originID), "wrong-type", 0).Err(); err != nil {
		t.Fatal(err)
	}
	_, err := storeA.SnapshotMany(ctx, testRuntimeSnapshotInput(originID, channelID))
	if err == nil || !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("snapshot error=%v", err)
	}
	if faultType := client.Type(ctx, storeA.keys.runtimeInfrastructureFault()).Val(); faultType != "string" {
		t.Fatalf("shared fault latch type=%q", faultType)
	}

	readinessInput := testRuntimeReadinessInput()
	if err := storeA.Ping(ctx); err != nil {
		t.Fatalf("ping after fault: %v", err)
	}
	for name, store := range map[string]*Store{"local": storeA, "shared": storeB} {
		result, checkErr := store.CheckRuntimeReadiness(ctx, readinessInput)
		if checkErr != nil || result.Ready || result.Reason != RuntimeReadinessReasonStoreFaultLatched {
			t.Fatalf("%s readiness=%+v err=%v", name, result, checkErr)
		}
	}

	blockedRequest := RequestAdmissionInput{
		RequestAdmissionID: "blocked-request", Fingerprint: "blocked-request-fingerprint",
		RouteID: 31, UserID: 41, IntegrityEpoch: testAttemptIntegrityEpoch,
		IntegrityRevision: testAttemptIntegrityRevision, RouteRateRevision: testRouteRateRevision,
		GlobalConcurrencyRevision: 1,
	}
	requestResult, err := storeB.AcquireRequestAdmission(ctx, blockedRequest)
	if err != nil || requestResult.Outcome != RequestStoreUnavailable {
		t.Fatalf("request result=%+v err=%v", requestResult, err)
	}
	if exists := client.Exists(ctx, storeB.keys.admissionRequest(blockedRequest.RequestAdmissionID)).Val(); exists != 0 {
		t.Fatalf("blocked request created %d token keys", exists)
	}

	attempt := withAttemptControlRevisions(AcquireAttemptInput{
		PermitID: "blocked-permit", AdmissionFingerprint: "blocked-permit-fingerprint",
		RequestAdmissionID: "unused-request", OriginID: originID, ChannelID: channelID,
		OriginBaseURLRevision: 1, OriginStatusRevision: 1, ChannelConfigRevision: 1,
		ModelID: 99, UpstreamEndpoint: EndpointChatCompletions, RequestMode: ModeNonStream,
	})
	attemptResult, err := storeB.AcquireAttempt(ctx, attempt)
	if err != nil || attemptResult.Mode != AdmissionDenied || attemptResult.Reason != ReasonBreakerStoreUnavailable {
		t.Fatalf("attempt result=%+v err=%v", attemptResult, err)
	}
	if exists := client.Exists(ctx, storeB.keys.permit(attempt.PermitID)).Val(); exists != 0 {
		t.Fatalf("blocked attempt created %d permit keys", exists)
	}

	// A generation captured by an old reconciliation cannot clear a newer shared failure.
	oldGeneration, err := storeA.BeginRuntimeReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, storeA.keys.runtimeInfrastructureFault(), "other-gateway:new-fault", 0).Err(); err != nil {
		t.Fatal(err)
	}
	oldProof := testRuntimeReconciliationProof(oldGeneration, originID, channelID)
	clearResult, err := storeA.ClearRuntimeInfrastructureFaultAfterReconciliation(ctx, readinessInput, oldProof)
	if err != nil || clearResult.Ready || clearResult.Reason != RuntimeReadinessReasonStoreFaultLatched {
		t.Fatalf("stale clear result=%+v err=%v", clearResult, err)
	}

	// Simulate the next full reconciliation repairing the corrupted Origin before explicit clear.
	if err := client.Del(ctx, storeA.keys.origin(originID)).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := storeA.RestoreMissingOriginControl(ctx, originID, 1, 1, "enabled"); err != nil {
		t.Fatal(err)
	}
	cleanGeneration, err := storeA.BeginRuntimeReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cleanProof := testRuntimeReconciliationProof(cleanGeneration, originID, channelID)
	clearResult, err = storeA.ClearRuntimeInfrastructureFaultAfterReconciliation(ctx, readinessInput, cleanProof)
	if err != nil || !clearResult.Ready {
		t.Fatalf("clean clear result=%+v err=%v", clearResult, err)
	}
	if exists := client.Exists(ctx, storeA.keys.runtimeInfrastructureFault()).Val(); exists != 0 {
		t.Fatalf("fault latch still exists after clear: %d", exists)
	}
	for name, store := range map[string]*Store{"local": storeA, "shared": storeB} {
		result, checkErr := store.CheckRuntimeReadiness(ctx, readinessInput)
		if checkErr != nil || !result.Ready {
			t.Fatalf("%s readiness after clear=%+v err=%v", name, result, checkErr)
		}
	}
}

func testRuntimeReconciliationProof(generation RuntimeReconciliationGeneration, originID, channelID int64) RuntimeReconciliationProof {
	return RuntimeReconciliationProof{
		Generation: generation,
		OriginControls: []RuntimeOriginControlProof{{
			OriginID: originID, BaseURLRevision: 1, StatusRevision: 1, EffectiveStatus: "enabled",
		}},
		ChannelAdmissionControls: []RuntimeChannelAdmissionControlProof{{
			ChannelID: channelID, Revision: 1,
			Payload: `{"rpm":null,"rpd":null,"tpm":null,"concurrency":null}`,
		}},
	}
}

func TestMalformedSharedFaultLatchFailsClosedAndCannotBeClearedByProbe(t *testing.T) {
	store, client, _ := newTestStore(t)
	ctx := context.Background()
	seedAttemptControls(t, store, testConfig(), 7302,
		`{"rpm":null,"rpd":null,"tpm":null,"concurrency":null}`)
	if err := client.HSet(ctx, store.keys.runtimeInfrastructureFault(), "malformed", "1").Err(); err != nil {
		t.Fatal(err)
	}

	result, err := store.CheckRuntimeReadiness(ctx, testRuntimeReadinessInput())
	if err != nil || result.Ready || result.Reason != RuntimeReadinessReasonStoreFaultLatched {
		t.Fatalf("readiness=%+v err=%v", result, err)
	}
	if _, err := store.BeginRuntimeReconciliation(ctx); err == nil || !errors.Is(err, ErrStoreUnavailable) ||
		failure.CodeOf(err) != failure.CodeDependencyRedisUnavailable {
		t.Fatalf("begin reconciliation error=%v code=%q", err, failure.CodeOf(err))
	}
	// The failed begin operation latches locally and replaces only its own malformed fence key.
	if got := client.Type(ctx, store.keys.runtimeInfrastructureFault()).Val(); got != "string" {
		t.Fatalf("fault latch type after confirmed error=%q", got)
	}
	result, err = store.CheckRuntimeReadiness(ctx, testRuntimeReadinessInput())
	if err != nil || result.Ready || result.Reason != RuntimeReadinessReasonStoreFaultLatched {
		t.Fatalf("readiness after malformed latch repair=%+v err=%v", result, err)
	}
}

func TestRedisInstanceProofMismatchBlocksEveryNewAdmissionBeforeResourceWrite(t *testing.T) {
	store, client, namespace := newTestStore(t)
	ctx := context.Background()
	const originID int64 = 7401
	const channelID int64 = 7402
	const routeID int64 = 7403
	const userID int64 = 7404
	seedAttemptControls(t, store, testConfig(), channelID,
		`{"rpm":null,"rpd":null,"tpm":null,"concurrency":null}`)
	if _, err := store.InitOriginControl(ctx, originID, 1, 1, "enabled"); err != nil {
		t.Fatal(err)
	}

	// Keep all old runtime data but replace only the shared proof, exactly as an AOF restore from a
	// previous Redis process would look to admission scripts after run_id changes.
	if err := client.Set(ctx, store.keys.runtimeReconciliationProof(), "0000000000000000000000000000000000000000", 0).Err(); err != nil {
		t.Fatal(err)
	}
	requestInput := RequestAdmissionInput{
		RequestAdmissionID: "instance-changed-request", Fingerprint: "instance-changed-request-fp",
		RouteID: routeID, UserID: userID, IntegrityEpoch: testAttemptIntegrityEpoch,
		IntegrityRevision: testAttemptIntegrityRevision, RouteRateRevision: testRouteRateRevision, GlobalConcurrencyRevision: 1,
	}
	requestResult, err := store.AcquireRequestAdmission(ctx, requestInput)
	if err != nil || requestResult.Outcome != RequestStoreUnavailable {
		t.Fatalf("request result=%+v err=%v", requestResult, err)
	}
	if got := client.Exists(ctx,
		store.keys.admissionRequest(requestInput.RequestAdmissionID),
		store.keys.requestRPMBucket(routeID, userID, minuteBucket(time.Now())),
		store.keys.requestRPDBucket(routeID, userID, dayBucket(time.Now())),
		store.keys.requestConcurrency(routeID, userID),
	).Val(); got != 0 {
		t.Fatalf("instance mismatch wrote %d request admission resources", got)
	}
	if got := client.Type(ctx, store.keys.runtimeInfrastructureFault()).Val(); got != "string" {
		t.Fatalf("instance mismatch did not publish shared latch, type=%q", got)
	}

	// Another Gateway without a local latch still sees and obeys the shared latch.
	if err := client.Del(ctx, store.keys.runtimeInfrastructureFault()).Err(); err != nil {
		t.Fatal(err)
	}
	other := NewStore(client, namespace)
	if _, err := other.SnapshotMany(ctx, testRuntimeSnapshotInput(originID, channelID)); failure.CodeOf(err) != failure.CodeGatewayBreakerStoreUnavailable {
		t.Fatalf("snapshot mismatch error=%v code=%q", err, failure.CodeOf(err))
	}
	if got := client.Type(ctx, store.keys.runtimeInfrastructureFault()).Val(); got != "string" {
		t.Fatalf("snapshot mismatch did not republish shared latch, type=%q", got)
	}

	if err := client.Del(ctx, store.keys.runtimeInfrastructureFault()).Err(); err != nil {
		t.Fatal(err)
	}
	attemptStore := NewStore(client, namespace)
	attempt := withAttemptControlRevisions(AcquireAttemptInput{
		PermitID: "instance-changed-permit", AdmissionFingerprint: "instance-changed-permit-fp",
		RequestAdmissionID: "instance-changed-reserved", OriginID: originID, ChannelID: channelID,
		OriginBaseURLRevision: 1, OriginStatusRevision: 1, ChannelConfigRevision: 1,
		ModelID: 99, UpstreamEndpoint: EndpointChatCompletions, RequestMode: ModeNonStream,
	})
	seedReservedRequestAdmission(t, attemptStore, attempt)
	attemptResult, err := attemptStore.AcquireAttempt(ctx, attempt)
	if err != nil || attemptResult.Mode != AdmissionDenied || attemptResult.Reason != ReasonBreakerStoreUnavailable {
		t.Fatalf("attempt mismatch result=%+v err=%v", attemptResult, err)
	}
	if got := client.Exists(ctx, attemptStore.keys.permit(attempt.PermitID)).Val(); got != 0 {
		t.Fatalf("instance mismatch wrote %d attempt permit resources", got)
	}

	if err := client.Del(ctx, store.keys.runtimeInfrastructureFault()).Err(); err != nil {
		t.Fatal(err)
	}
	reserveStore := NewStore(client, namespace)
	reserveResult, err := reserveStore.ReserveRequestTokens(
		ctx, attempt.RequestAdmissionID, routeID, userID, 10,
		testAttemptIntegrityEpoch, testAttemptIntegrityRevision,
	)
	if err != nil || reserveResult != ReserveStoreUnavailable {
		t.Fatalf("reserve mismatch result=%s err=%v", reserveResult, err)
	}
	if got := client.Exists(ctx, reserveStore.keys.requestTPMBucket(routeID, userID, minuteBucket(time.Now()))).Val(); got != 0 {
		t.Fatalf("instance mismatch wrote %d request TPM resources", got)
	}
}

func TestRuntimeFaultClearKeepsSharedLatchWhenLocalGenerationChanges(t *testing.T) {
	store, client, _ := newTestStore(t)
	ctx := context.Background()
	const originID int64 = 7501
	const channelID int64 = 7502
	seedAttemptControls(t, store, testConfig(), channelID,
		`{"rpm":null,"rpd":null,"tpm":null,"concurrency":null}`)
	if _, err := store.InitOriginControl(ctx, originID, 1, 1, "enabled"); err != nil {
		t.Fatal(err)
	}
	store.latchRuntimeInfrastructureFault(ctx)
	generation, err := store.BeginRuntimeReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	proof := testRuntimeReconciliationProof(generation, originID, channelID)

	// Simulate a request-time fault confirmed after the full reconciliation began. The clear must
	// never make the shared namespace visible as ready, even briefly, for this stale local proof.
	store.fault.latch()
	result, err := store.ClearRuntimeInfrastructureFaultAfterReconciliation(ctx, testRuntimeReadinessInput(), proof)
	if err != nil || result.Ready || result.Reason != RuntimeReadinessReasonStoreFaultLatched {
		t.Fatalf("stale local generation clear result=%+v err=%v", result, err)
	}
	if got := client.Type(ctx, store.keys.runtimeInfrastructureFault()).Val(); got != "string" {
		t.Fatalf("stale local generation removed shared latch, type=%q", got)
	}
}

func TestRuntimeFaultClearRequiresEveryOriginAndChannelControlProof(t *testing.T) {
	store, client, _ := newTestStore(t)
	ctx := context.Background()
	const originID int64 = 7601
	const channelID int64 = 7602
	seedAttemptControls(t, store, testConfig(), channelID,
		`{"rpm":null,"rpd":null,"tpm":null,"concurrency":null}`)
	if _, err := store.InitOriginControl(ctx, originID, 1, 1, "enabled"); err != nil {
		t.Fatal(err)
	}
	store.latchRuntimeInfrastructureFault(ctx)
	generation, err := store.BeginRuntimeReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	proof := testRuntimeReconciliationProof(generation, originID, channelID)

	if err := client.Set(ctx, store.keys.origin(originID), "wrong-type", 0).Err(); err != nil {
		t.Fatal(err)
	}
	result, err := store.ClearRuntimeInfrastructureFaultAfterReconciliation(ctx, testRuntimeReadinessInput(), proof)
	if err == nil || !errors.Is(err, ErrStoreUnavailable) || result.Ready {
		t.Fatalf("wrong-type origin clear result=%+v err=%v", result, err)
	}
	if got := client.Type(ctx, store.keys.runtimeInfrastructureFault()).Val(); got != "string" {
		t.Fatalf("origin proof failure removed shared latch, type=%q", got)
	}
	if err := client.Del(ctx, store.keys.origin(originID)).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RestoreMissingOriginControl(ctx, originID, 1, 1, "enabled"); err != nil {
		t.Fatal(err)
	}

	generation, err = store.BeginRuntimeReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	proof = testRuntimeReconciliationProof(generation, originID, channelID)
	if err := client.Set(ctx, store.keys.admissionChannel(channelID), "wrong-type", 0).Err(); err != nil {
		t.Fatal(err)
	}
	result, err = store.ClearRuntimeInfrastructureFaultAfterReconciliation(ctx, testRuntimeReadinessInput(), proof)
	if err == nil || !errors.Is(err, ErrStoreUnavailable) || result.Ready {
		t.Fatalf("wrong-type channel clear result=%+v err=%v", result, err)
	}
	if got := client.Type(ctx, store.keys.runtimeInfrastructureFault()).Val(); got != "string" {
		t.Fatalf("channel proof failure removed shared latch, type=%q", got)
	}
}

func TestRuntimeFaultClearCommitValidatesAllFiveCriticalControlProofs(t *testing.T) {
	controls := readinessControlFixtures()
	for index, control := range controls {
		t.Run(control.name, func(t *testing.T) {
			store, input := seedRuntimeReadinessFixture(t)
			ctx := context.Background()
			store.latchRuntimeInfrastructureFault(ctx)
			generation, err := store.BeginRuntimeReconciliation(ctx)
			if err != nil {
				t.Fatal(err)
			}

			keys := []string{
				store.keys.runtimeInfrastructureFault(),
				store.keys.stateIntegrityMarker(),
				store.keys.admissionRouteRate(),
				store.keys.admissionChannelRate(),
				store.keys.admissionGlobalConcurrency(),
				store.keys.runtimeControlSetting("gateway.circuit_breaker"),
				store.keys.runtimeControlSetting("gateway.routing_balance"),
				store.keys.runtimeReconciliationProof(),
			}
			proofArgs := append(runtimeReadinessArgs(input), generation.redisRunID)
			proofRaw, err := store.faultProof.Run(ctx, store.client, keys[:7], proofArgs...).Result()
			if err != nil {
				t.Fatalf("fault proof: %v", err)
			}
			proofReply, ok := proofRaw.([]interface{})
			if !ok || len(proofReply) != 12 || proofReply[0] != "ready" ||
				proofReply[1] != generation.sharedToken || !validRuntimeControlProofs(proofReply[2:]) {
				t.Fatalf("unexpected five-control proof: %#v", proofRaw)
			}

			mutatedPayload := `{"mutated":true}`
			if err := store.client.HSet(
				ctx, control.target(store).controlKey,
				"active_payload", mutatedPayload,
				"active_payload_hash", HashPayload(mutatedPayload),
			).Err(); err != nil {
				t.Fatal(err)
			}

			clearArgs := append(runtimeReadinessArgs(input), generation.redisRunID, proofReply[1])
			clearArgs = append(clearArgs, proofReply[2:]...)
			clearArgs = append(clearArgs, "0", "0")
			clearRaw, err := store.faultClear.Run(ctx, store.client, keys, clearArgs...).Result()
			if err != nil {
				t.Fatalf("fault clear commit: %v", err)
			}
			clearReply, ok := clearRaw.([]interface{})
			if !ok || len(clearReply) != 2 || clearReply[0] != "control_payload_changed" || clearReply[1] != int64(index+1) {
				t.Fatalf("changed %s proof must be rejected at index %d: %#v", control.name, index+1, clearRaw)
			}
			if got := store.client.Type(ctx, store.keys.runtimeInfrastructureFault()).Val(); got != "string" {
				t.Fatalf("changed %s proof removed fault latch: type=%q", control.name, got)
			}
		})
	}
}

func testRuntimeReadinessInput() RuntimeReadinessInput {
	return RuntimeReadinessInput{
		Epoch: testAttemptIntegrityEpoch, EpochRevision: testAttemptIntegrityRevision,
		RouteRateLimitRevision: testRouteRateRevision, ChannelRateLimitRevision: testChannelRateRevision,
		ConcurrencyRevision:    1,
		CircuitBreakerRevision: 1, RoutingBalanceRevision: 1,
	}
}

func testRuntimeSnapshotInput(originID, channelID int64) SnapshotManyInput {
	return SnapshotManyInput{
		IntegrityEpoch: testAttemptIntegrityEpoch, IntegrityRevision: testAttemptIntegrityRevision,
		ChannelRateRevision: testChannelRateRevision, GlobalConcurrencyRevision: 1,
		CircuitBreakerRevision: 1, RoutingBalanceRevision: 1, ModelID: 99,
		Candidates: []SnapshotCandidateInput{{
			OriginID: originID, ChannelID: channelID,
			OriginBaseURLRevision: 1, OriginStatusRevision: 1,
			ChannelConfigRevision: 1, ChannelAdmissionRevision: 1,
		}},
	}
}
