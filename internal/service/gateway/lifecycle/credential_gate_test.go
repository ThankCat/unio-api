package lifecycle

import (
	"sync"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// recordingInvalidator 记录被翻失效的渠道(线程安全,供并发测试)。
type recordingInvalidator struct {
	mu  sync.Mutex
	ids []int64
}

func (r *recordingInvalidator) MarkChannelCredentialInvalid(channelID int64) {
	r.mu.Lock()
	r.ids = append(r.ids, channelID)
	r.mu.Unlock()
}

func (r *recordingInvalidator) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ids)
}

func authErr() error {
	return adapter.NewUpstreamError(adapter.UpstreamErrorAuth, adapter.UpstreamMetadata{StatusCode: 401}, failure.New(failure.CodeAdapterUpstreamStatus))
}

func TestCredentialGateTripsAtThreshold(t *testing.T) {
	inv := &recordingInvalidator{}
	g := NewChannelCredentialGate(3, inv)

	g.RecordResult(7, authErr())
	g.RecordResult(7, authErr())
	if inv.count() != 0 {
		t.Fatal("should not trip below threshold")
	}
	g.RecordResult(7, authErr())
	if inv.count() != 1 {
		t.Fatalf("should trip at threshold, got %d invalidations", inv.count())
	}
}

func TestCredentialGateSetThresholdTakesEffect(t *testing.T) {
	inv := &recordingInvalidator{}
	g := NewChannelCredentialGate(5, inv)

	g.RecordResult(7, authErr())
	g.RecordResult(7, authErr())

	// 热改阈值为 3:已有连续计数 2 保留,下一次 401 即达标。
	g.SetThreshold(3)
	g.RecordResult(7, authErr())
	if inv.count() != 1 {
		t.Fatalf("should trip using hot-reloaded threshold, got %d", inv.count())
	}

	// <=0 沿用兜底 3。
	g.SetThreshold(0)
	g.RecordResult(8, authErr())
	g.RecordResult(8, authErr())
	if inv.count() != 1 {
		t.Fatal("fallback threshold should be 3, not lower")
	}
	g.RecordResult(8, authErr())
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
		g.RecordResult(1, authErr())
		g.RecordResult(1, nil)
	}
	<-done
}
