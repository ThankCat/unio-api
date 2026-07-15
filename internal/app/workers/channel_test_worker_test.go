package workers

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/appsettings"
)

type fakeChannelTestStore struct {
	channels []sqlc.Channel
	listErr  error
	pruned   []sqlc.DeleteChannelTestLogsBeyondPerChannelParams
}

func (s *fakeChannelTestStore) ListChannelsForCredentialTest(context.Context) ([]sqlc.Channel, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.channels, nil
}

func (s *fakeChannelTestStore) DeleteChannelTestLogsBeyondPerChannel(
	_ context.Context,
	arg sqlc.DeleteChannelTestLogsBeyondPerChannelParams,
) (int64, error) {
	s.pruned = append(s.pruned, arg)
	return 0, nil
}

type fakeChannelTester struct {
	calls []int64
	err   error
}

func (t *fakeChannelTester) TestChannel(_ context.Context, channelID int64) error {
	t.calls = append(t.calls, channelID)
	return t.err
}

func newTestChannelWorker(store ChannelTestStore, tester ChannelCredentialTester) (*ChannelTestWorker, *time.Time) {
	worker := NewChannelTestWorker(store, tester, nil, slog.Default())
	clock := new(time.Time)
	*clock = time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return *clock }
	return worker, clock
}

func TestChannelTestWorkerRunsCycleWithDefaults(t *testing.T) {
	store := &fakeChannelTestStore{channels: []sqlc.Channel{{ID: 7}, {ID: 8}}}
	tester := &fakeChannelTester{}
	worker, clock := newTestChannelWorker(store, tester)

	worked, err := worker.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("first: want worked,nil; got %v,%v", worked, err)
	}
	worked, err = worker.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("second: want worked,nil; got %v,%v", worked, err)
	}
	if len(tester.calls) != 2 || tester.calls[0] != 7 || tester.calls[1] != 8 {
		t.Fatalf("calls = %v, want [7 8]", tester.calls)
	}
	if len(store.pruned) != 2 {
		t.Fatalf("pruned = %d, want 2", len(store.pruned))
	}
	if store.pruned[0].Keep != int32(appsettings.DefaultChannelTestLogRetentionSetting) {
		t.Fatalf("keep = %d, want default %d", store.pruned[0].Keep, appsettings.DefaultChannelTestLogRetentionSetting)
	}

	// 未到下一轮间隔：空转
	worked, err = worker.RunOnce(context.Background())
	if err != nil || worked {
		t.Fatalf("before interval: want idle; got %v,%v", worked, err)
	}

	*clock = clock.Add(appsettings.DefaultChannelTestWorkerIntervalSetting)
	worked, err = worker.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("after interval: want worked; got %v,%v", worked, err)
	}
	if len(tester.calls) != 3 {
		t.Fatalf("after next cycle calls = %d, want 3", len(tester.calls))
	}
}

func TestChannelTestWorkerName(t *testing.T) {
	worker := NewChannelTestWorker(&fakeChannelTestStore{}, &fakeChannelTester{}, nil, slog.Default())
	if worker.Name() != "channel_test" {
		t.Fatalf("name = %q", worker.Name())
	}
}
