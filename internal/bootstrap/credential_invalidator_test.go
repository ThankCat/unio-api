package bootstrap

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

type blockingCredentialInvalidationStore struct {
	mu        sync.Mutex
	active    int
	maxActive int
	calls     int
	release   chan struct{}
	done      chan struct{}
}

func (s *blockingCredentialInvalidationStore) ApplyRuntime401CredentialInvalidation(
	ctx context.Context,
	_ sqlc.ApplyRuntime401CredentialInvalidationParams,
) (sqlc.ApplyRuntime401CredentialInvalidationRow, error) {
	s.mu.Lock()
	s.active++
	s.calls++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()

	select {
	case <-s.release:
	case <-ctx.Done():
		return sqlc.ApplyRuntime401CredentialInvalidationRow{}, ctx.Err()
	}

	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	s.done <- struct{}{}
	return sqlc.ApplyRuntime401CredentialInvalidationRow{}, nil
}

func (s *blockingCredentialInvalidationStore) snapshot() (calls, maxActive int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.maxActive
}

func TestCredentialInvalidatorBoundsAndDeduplicatesAsyncWork(t *testing.T) {
	store := &blockingCredentialInvalidationStore{
		release: make(chan struct{}),
		done:    make(chan struct{}, 4),
	}
	invalidator := newCredentialInvalidator(store, zap.NewNop())
	invalidator.slots = make(chan struct{}, 2)

	revision := func(channelID int64) lifecycle.CredentialRevision {
		return lifecycle.CredentialRevision{
			ChannelID:              channelID,
			ChannelConfigRevision:  1,
			OriginRevision:         1,
			ProviderStatusRevision: 1,
			Threshold:              3,
		}
	}

	invalidator.MarkChannelCredentialInvalid(revision(1))
	invalidator.MarkChannelCredentialInvalid(revision(1))
	invalidator.MarkChannelCredentialInvalid(revision(2))
	invalidator.MarkChannelCredentialInvalid(revision(3))

	deadline := time.Now().Add(time.Second)
	for {
		calls, maxActive := store.snapshot()
		if calls == 2 {
			if maxActive > 2 {
				t.Fatalf("max active invalidations = %d, want at most 2", maxActive)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("started invalidations = %d, want 2", calls)
		}
		time.Sleep(time.Millisecond)
	}

	invalidator.mu.Lock()
	tracked := len(invalidator.inflight)
	invalidator.mu.Unlock()
	if tracked != 2 {
		t.Fatalf("in-flight channel states = %d, want 2", tracked)
	}

	close(store.release)
	for range 2 {
		select {
		case <-store.done:
		case <-time.After(time.Second):
			t.Fatal("credential invalidation did not finish")
		}
	}
}
