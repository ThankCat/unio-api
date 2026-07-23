package lifecycle

import (
	"sync"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// recordingInvalidator 记录被翻失效的渠道(线程安全,供并发测试)。
type recordingInvalidator struct {
	mu        sync.Mutex
	revisions []CredentialRevision
}

func (r *recordingInvalidator) MarkChannelCredentialInvalid(revision CredentialRevision) {
	r.mu.Lock()
	r.revisions = append(r.revisions, revision)
	r.mu.Unlock()
}

func (r *recordingInvalidator) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.revisions)
}

func credentialRevision(channelID, configRevision int64) CredentialRevision {
	return CredentialRevision{
		ChannelID: channelID, ChannelConfigRevision: configRevision,
		EndpointBaseURLRevision: 1, EndpointStatusRevision: 1,
	}
}

func authErr() error {
	return adapter.NewUpstreamError(adapter.UpstreamErrorAuth, adapter.UpstreamMetadata{StatusCode: 401}, failure.New(failure.CodeAdapterUpstreamStatus))
}

func TestCredentialGateTripsAtThreshold(t *testing.T) {
	inv := &recordingInvalidator{}
	g := NewChannelCredentialGate(3, inv)

	revision := credentialRevision(7, 3)
	g.RecordResult(revision, authErr())
	g.RecordResult(revision, authErr())
	if inv.count() != 0 {
		t.Fatal("should not trip below threshold")
	}
	g.RecordResult(revision, authErr())
	if inv.count() != 1 {
		t.Fatalf("should trip at threshold, got %d invalidations", inv.count())
	}
}

func TestCredentialGateSetThresholdTakesEffect(t *testing.T) {
	inv := &recordingInvalidator{}
	g := NewChannelCredentialGate(5, inv)

	revision := credentialRevision(7, 3)
	g.RecordResult(revision, authErr())
	g.RecordResult(revision, authErr())

	// 热改阈值为 3:已有连续计数 2 保留,下一次 401 即达标。
	g.SetThreshold(3)
	g.RecordResult(revision, authErr())
	if inv.count() != 1 {
		t.Fatalf("should trip using hot-reloaded threshold, got %d", inv.count())
	}

	// <=0 沿用兜底 3。
	g.SetThreshold(0)
	revision = credentialRevision(8, 4)
	g.RecordResult(revision, authErr())
	g.RecordResult(revision, authErr())
	if inv.count() != 1 {
		t.Fatal("fallback threshold should be 3, not lower")
	}
	g.RecordResult(revision, authErr())
	if inv.count() != 2 {
		t.Fatalf("fallback threshold 3 should trip, got %d", inv.count())
	}
}

// TestCredentialGateConcurrentReload 在 -race 下验证热改与热路径读写无竞态。
func TestCredentialGateConcurrentReload(t *testing.T) {
	g := NewChannelCredentialGate(3, &recordingInvalidator{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			g.SetThreshold(i%10 + 1)
		}
	}()
	for i := 0; i < 500; i++ {
		revision := credentialRevision(1, 1)
		g.RecordResult(revision, authErr())
		g.RecordResult(revision, nil)
	}
	<-done
}

func TestCredentialGateKeepsRevisionsIsolated(t *testing.T) {
	inv := &recordingInvalidator{}
	g := NewChannelCredentialGate(2, inv)
	oldRevision := credentialRevision(7, 3)
	newRevision := credentialRevision(7, 4)

	g.RecordResult(oldRevision, authErr())
	g.RecordResult(newRevision, authErr())
	if inv.count() != 0 {
		t.Fatal("401 results from different channel generations must not share a counter")
	}
	g.RecordResult(newRevision, authErr())
	if inv.count() != 1 {
		t.Fatalf("current generation should trip independently, got %d invalidations", inv.count())
	}
	inv.mu.Lock()
	got := inv.revisions[0]
	inv.mu.Unlock()
	if got != newRevision {
		t.Fatalf("invalidated revision=%+v, want %+v", got, newRevision)
	}
}
