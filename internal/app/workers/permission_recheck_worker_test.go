package workers

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channeltest"
)

type fakePermissionRecheckStore struct {
	task             *breakerstore.PermissionRecheckTask
	claimErr         error
	completedTask    breakerstore.PermissionRecheckTask
	completedOutcome breakerstore.PermissionRecheckOutcome
	retryAfter       time.Duration
	disposition      breakerstore.PermissionRecheckDisposition
	completeErr      error
	completeCalls    int
}

func (s *fakePermissionRecheckStore) ClaimPermissionRecheck(context.Context, string, time.Duration) (*breakerstore.PermissionRecheckTask, error) {
	return s.task, s.claimErr
}

func (s *fakePermissionRecheckStore) CompletePermissionRecheck(
	_ context.Context,
	task breakerstore.PermissionRecheckTask,
	outcome breakerstore.PermissionRecheckOutcome,
	retryAfter time.Duration,
) (breakerstore.PermissionRecheckDisposition, error) {
	s.completeCalls++
	s.completedTask = task
	s.completedOutcome = outcome
	s.retryAfter = retryAfter
	return s.disposition, s.completeErr
}

type fakePermissionRechecker struct {
	input  channeltest.PermissionRecheckInput
	result channeltest.PermissionRecheckResult
	err    error
	calls  int
}

func (r *fakePermissionRechecker) RecheckPermission(_ context.Context, in channeltest.PermissionRecheckInput) (channeltest.PermissionRecheckResult, error) {
	r.calls++
	r.input = in
	return r.result, r.err
}

func permissionTaskFixture() *breakerstore.PermissionRecheckTask {
	return &breakerstore.PermissionRecheckTask{
		ChannelID: 9, ModelID: 99, ChannelConfigRevision: 5,
		EndpointBaseURLRevision: 3, EndpointStatusRevision: 4,
		Attempt: 2, ClaimToken: "claim-token",
	}
}

func TestPermissionRecheckWorkerMapsProbeOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		result      channeltest.PermissionRecheckResult
		recheckErr  error
		wantOutcome breakerstore.PermissionRecheckOutcome
		wantBackoff bool
	}{
		{name: "success clears", result: channeltest.PermissionRecheckResult{Probe: channeltest.TestResult{Success: true}}, wantOutcome: breakerstore.PermissionRecheckSucceeded},
		{name: "probe failure retries", result: channeltest.PermissionRecheckResult{Probe: channeltest.TestResult{Success: false}}, wantOutcome: breakerstore.PermissionRecheckFailed, wantBackoff: true},
		{name: "stale leaves old queue", result: channeltest.PermissionRecheckResult{Probe: channeltest.TestResult{Success: true}, Stale: true}, wantOutcome: breakerstore.PermissionRecheckStale},
		{name: "service failure retries", recheckErr: errors.New("database unavailable"), wantOutcome: breakerstore.PermissionRecheckFailed, wantBackoff: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakePermissionRecheckStore{task: permissionTaskFixture(), disposition: breakerstore.PermissionRecheckCleared}
			rechecker := &fakePermissionRechecker{result: tt.result, err: tt.recheckErr}
			worker := NewPermissionRecheckWorker(store, rechecker, nil, "worker-test", zap.NewNop())
			worker.backoff = func(int64) time.Duration { return 7 * time.Second }

			worked, err := worker.RunOnce(context.Background())
			if err != nil || !worked {
				t.Fatalf("run once: worked=%v err=%v", worked, err)
			}
			if rechecker.calls != 1 || store.completeCalls != 1 {
				t.Fatalf("expected one recheck/completion, calls=%d/%d", rechecker.calls, store.completeCalls)
			}
			if store.completedOutcome != tt.wantOutcome {
				t.Fatalf("outcome want %s got %s", tt.wantOutcome, store.completedOutcome)
			}
			if tt.wantBackoff && store.retryAfter != 7*time.Second {
				t.Fatalf("retry backoff want 7s got %s", store.retryAfter)
			}
			if !tt.wantBackoff && store.retryAfter != 0 {
				t.Fatalf("terminal outcome must not back off, got %s", store.retryAfter)
			}
			if rechecker.input.ChannelID != 9 || rechecker.input.ModelID != 99 ||
				rechecker.input.ChannelConfigRevision != 5 || rechecker.input.EndpointBaseURLRevision != 3 ||
				rechecker.input.EndpointStatusRevision != 4 {
				t.Fatalf("worker did not preserve task identity: %+v", rechecker.input)
			}
		})
	}
}

func TestPermissionRecheckWorkerIdleAndCompletionFailure(t *testing.T) {
	idleStore := &fakePermissionRecheckStore{}
	idle := NewPermissionRecheckWorker(idleStore, &fakePermissionRechecker{}, nil, "worker-test", zap.NewNop())
	if worked, err := idle.RunOnce(context.Background()); err != nil || worked {
		t.Fatalf("idle worker: worked=%v err=%v", worked, err)
	}

	completeErr := errors.New("redis unavailable")
	failedStore := &fakePermissionRecheckStore{
		task: permissionTaskFixture(), disposition: breakerstore.PermissionRecheckRescheduled, completeErr: completeErr,
	}
	worker := NewPermissionRecheckWorker(failedStore, &fakePermissionRechecker{}, nil, "worker-test", zap.NewNop())
	worked, err := worker.RunOnce(context.Background())
	if !worked || !errors.Is(err, completeErr) {
		t.Fatalf("completion failure must surface: worked=%v err=%v", worked, err)
	}
}

func TestPermissionRecheckBackoffIsCapped(t *testing.T) {
	if got := permissionRecheckBackoff(1); got != 30*time.Second {
		t.Fatalf("first backoff want 30s got %s", got)
	}
	if got := permissionRecheckBackoff(100); got != permissionRecheckMaximumBackoff {
		t.Fatalf("backoff cap want %s got %s", permissionRecheckMaximumBackoff, got)
	}
}
