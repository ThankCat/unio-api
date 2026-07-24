package breakerstore

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

type recordedStoreOperation struct {
	operation string
	result    string
	duration  time.Duration
}

type recordingStoreObserver struct {
	mu         sync.Mutex
	operations []recordedStoreOperation
}

func (o *recordingStoreObserver) ObserveBreakerStoreOperation(operation, result string, duration time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.operations = append(o.operations, recordedStoreOperation{operation: operation, result: result, duration: duration})
}

func (o *recordingStoreObserver) require(t *testing.T, operation, result string) {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, observed := range o.operations {
		if observed.operation == operation && observed.result == result {
			if observed.duration < 0 {
				t.Fatalf("operation %s/%s recorded negative duration %s", operation, result, observed.duration)
			}
			return
		}
	}
	t.Fatalf("missing operation observation %s/%s; got %+v", operation, result, o.operations)
}

func newObservedTestStore(t *testing.T, observer OperationObserver) *Store {
	t.Helper()
	_, client, namespace := newTestStore(t)
	return NewStore(client, namespace, observer)
}

func TestBoundedOperationResultDoesNotForwardDynamicValues(t *testing.T) {
	if got := boundedOperationResult("upstream supplied result with id=123", nil); got != operationResultError {
		t.Fatalf("unknown result = %q, want %q", got, operationResultError)
	}
	if got := boundedOperationResult("ignored", failure.New(failure.CodeConfigInvalid)); got != operationResultInvalid {
		t.Fatalf("config error result = %q, want %q", got, operationResultInvalid)
	}
	if got := boundedOperationResult("ignored", storeUnavailable(context.DeadlineExceeded, "secret redis error")); got != operationResultUnavailable {
		t.Fatalf("store error result = %q, want %q", got, operationResultUnavailable)
	}
}

func TestOperationObserverCoversHealthSnapshotsCooldownAndPermissionCAS(t *testing.T) {
	observer := &recordingStoreObserver{}
	store := newObservedTestStore(t, observer)
	ctx := context.Background()

	if err := store.VerifySingleNodeDeployment(ctx); err != nil {
		t.Fatalf("verify deployment: %v", err)
	}
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if snapshot, err := store.StateIntegrity(ctx); err != nil || snapshot.Exists {
		t.Fatalf("empty integrity snapshot = %+v, err=%v", snapshot, err)
	}
	if readiness, err := store.CheckRuntimeReadiness(ctx, RuntimeReadinessInput{}); err != nil || readiness.Ready {
		t.Fatalf("empty runtime readiness = %+v, err=%v", readiness, err)
	}
	if snapshot, err := store.Snapshot(ctx, ScopeChannel, 41); err != nil || snapshot.Exists {
		t.Fatalf("empty breaker snapshot = %+v, err=%v", snapshot, err)
	}
	if _, err := store.SetChannel429Cooldown(ctx, 41, 30_000, 5_000); err != nil {
		t.Fatalf("set cooldown: %v", err)
	}
	if remaining, err := store.Channel429CooldownRemainingMs(ctx, 41); err != nil || remaining <= 0 {
		t.Fatalf("cooldown remaining = %d, err=%v", remaining, err)
	}

	if err := store.PauseChannelModelPermission(ctx, 41, 99, 2, 2, 2); err != nil {
		t.Fatalf("pause current permission: %v", err)
	}
	if err := store.PauseChannelModelPermission(ctx, 41, 99, 1, 1, 1); err != nil {
		t.Fatalf("pause stale permission: %v", err)
	}
	permissionKey := store.keys.channelModelPermission(41, 99)
	if revision, err := store.client.HGet(ctx, permissionKey, "channel_config_revision").Int64(); err != nil || revision != 2 {
		t.Fatalf("stale pause changed current revision: revision=%d err=%v", revision, err)
	}
	if cleared, err := store.ClearChannelModelPermission(ctx, 41, 99, 1, 1, 1); err != nil || cleared {
		t.Fatalf("stale clear = %t, err=%v", cleared, err)
	}
	if cleared, err := store.ClearChannelModelPermission(ctx, 41, 99, 2, 2, 2); err != nil || !cleared {
		t.Fatalf("current clear = %t, err=%v", cleared, err)
	}

	observer.require(t, operationVerifyDeployment, operationResultSuccess)
	observer.require(t, operationPing, operationResultSuccess)
	observer.require(t, operationStateIntegrity, string(PermissionRecheckAbsent))
	observer.require(t, operationRuntimeReadiness, operationResultNotReady)
	observer.require(t, operationSnapshot, string(PermissionRecheckAbsent))
	observer.require(t, operationSet429Cooldown, operationResultSuccess)
	observer.require(t, operationRead429Cooldown, operationResultActive)
	observer.require(t, operationPausePermission, operationResultPaused)
	observer.require(t, operationPausePermission, "stale")
	observer.require(t, operationClearPermission, "stale")
	observer.require(t, operationClearPermission, string(PermissionRecheckCleared))
}

func TestOperationObserverCoversRequestAdmissionLifecycle(t *testing.T) {
	observer := &recordingStoreObserver{}
	store := newObservedTestStore(t, observer)
	epoch, revision := seedAdmissionEnv(t, store)
	ctx := context.Background()
	in := raInput("observed-request", 71, 81, epoch, revision)

	if result, err := store.AcquireRequestAdmission(ctx, in); err != nil || result.Outcome != RequestAllowed {
		t.Fatalf("acquire request = %+v, err=%v", result, err)
	}
	if result, err := store.ReserveRequestTokens(ctx, in.RequestAdmissionID, in.RouteID, in.UserID, 7,
		in.IntegrityEpoch, in.IntegrityRevision); err != nil || result != ReserveReserved {
		t.Fatalf("reserve request = %s, err=%v", result, err)
	}
	if result, err := store.RenewRequestAdmission(ctx, in.RequestAdmissionID, in.RouteID, in.UserID, epoch, revision); err != nil || result != RequestLifecycleRenewed {
		t.Fatalf("renew request = %s, err=%v", result, err)
	}
	if result, err := store.FinishRequestAdmission(ctx, in.RequestAdmissionID, in.RouteID, in.UserID, 7, epoch, revision); err != nil || result != RequestLifecycleFinished {
		t.Fatalf("finish request = %s, err=%v", result, err)
	}

	observer.require(t, operationAcquireRequest, string(RequestAllowed))
	observer.require(t, operationReserveRequest, string(ReserveReserved))
	observer.require(t, operationRenewRequest, string(RequestLifecycleRenewed))
	observer.require(t, operationFinishRequest, string(RequestLifecycleFinished))
}

func TestOperationObserverCoversAttemptPermitLifecycle(t *testing.T) {
	observer := &recordingStoreObserver{}
	store := newObservedTestStore(t, observer)
	ctx := context.Background()
	cfg := testConfig()
	seedAttemptControls(t, store, cfg, 501, `{"rpm":null,"rpd":null,"tpm":null,"concurrency":null}`)

	newInput := func(permitID, requestID string) AcquireAttemptInput {
		return withAttemptControlRevisions(AcquireAttemptInput{
			PermitID: permitID, AdmissionFingerprint: permitID + "-fp", RequestAdmissionID: requestID,
			OriginID: 601, ChannelID: 501,
			OriginBaseURLRevision: 1, OriginStatusRevision: 1, ChannelConfigRevision: 1,
			ModelID: 701, UpstreamEndpoint: EndpointChatCompletions, RequestMode: ModeNonStream,
		})
	}

	finishInput := newInput("observed-finish", "observed-request-finish")
	seedReservedRequestAdmission(t, store, finishInput)
	finishAdmission, err := store.AcquireAttempt(ctx, finishInput)
	if err != nil || finishAdmission.Mode != AdmissionPermit || finishAdmission.Permit == nil {
		t.Fatalf("acquire finish permit = %+v, err=%v", finishAdmission, err)
	}
	if err := store.Renew(ctx, *finishAdmission.Permit); err != nil {
		t.Fatalf("renew finish permit: %v", err)
	}
	if result, err := store.Finish(ctx, *finishAdmission.Permit, FinishOutcome{
		OriginOutcome: OutcomeEligibleSuccess,
		ChannelOutcome:  OutcomeEligibleSuccess,
	}); err != nil || result.OriginDisposition != DispositionApplied || result.ChannelDisposition != DispositionApplied {
		t.Fatalf("finish permit = %+v, err=%v", result, err)
	}

	abortInput := newInput("observed-abort", "observed-request-abort")
	seedReservedRequestAdmission(t, store, abortInput)
	abortAdmission, err := store.AcquireAttempt(ctx, abortInput)
	if err != nil || abortAdmission.Mode != AdmissionPermit || abortAdmission.Permit == nil {
		t.Fatalf("acquire abort permit = %+v, err=%v", abortAdmission, err)
	}
	if err := store.Abort(ctx, *abortAdmission.Permit); err != nil {
		t.Fatalf("abort permit: %v", err)
	}

	observer.require(t, operationAcquireAttempt, operationResultAllowed)
	observer.require(t, operationRenewAttempt, operationResultSuccess)
	observer.require(t, operationFinishAttempt, operationResultApplied)
	observer.require(t, operationAbortAttempt, operationResultSuccess)
}
