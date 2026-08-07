package workers

import (
	"context"
	"testing"
	"time"
)

type discoveryExecutorStub struct {
	enqueueCalls int
	executeCalls int
	enqueued     int
	worked       bool
}

func (s *discoveryExecutorStub) EnqueueDueDiscoveries(context.Context) (int, error) {
	s.enqueueCalls++
	return s.enqueued, nil
}

func (s *discoveryExecutorStub) ExecuteNextDiscovery(context.Context) (bool, error) {
	s.executeCalls++
	return s.worked, nil
}

type verificationExecutorStub struct{ calls int }

func (s *verificationExecutorStub) ExecuteNextVerification(context.Context) (bool, error) {
	s.calls++
	return true, nil
}

func TestChannelModelDiscoveryWorkerSweepsAtMostOncePerMinute(t *testing.T) {
	executor := &discoveryExecutorStub{enqueued: 2, worked: true}
	worker := NewChannelModelDiscoveryWorker(executor, nil, nil)
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worked, err := worker.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	_, _ = worker.RunOnce(context.Background())
	if executor.enqueueCalls != 1 || executor.executeCalls != 2 {
		t.Fatalf("enqueue=%d execute=%d", executor.enqueueCalls, executor.executeCalls)
	}

	now = now.Add(time.Minute)
	_, _ = worker.RunOnce(context.Background())
	if executor.enqueueCalls != 2 {
		t.Fatalf("enqueue after next sweep=%d", executor.enqueueCalls)
	}
}

func TestChannelModelVerificationWorkerDelegatesOneBatch(t *testing.T) {
	executor := &verificationExecutorStub{}
	worker := NewChannelModelVerificationWorker(executor)
	worked, err := worker.RunOnce(context.Background())
	if err != nil || !worked || executor.calls != 1 {
		t.Fatalf("worked=%v calls=%d err=%v", worked, executor.calls, err)
	}
}
