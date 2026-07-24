package workers

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

type fakeChannelTestStore struct {
	channels []sqlc.ListChannelsForCredentialTestRow
	listErr  error
	pruned   []sqlc.DeleteChannelTestLogsBeyondPerChannelParams
}

func (s *fakeChannelTestStore) ListChannelsForCredentialTest(context.Context) ([]sqlc.ListChannelsForCredentialTestRow, error) {
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
	worker := NewChannelTestWorker(store, tester, nil, zap.NewNop())
	clock := new(time.Time)
	*clock = time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return *clock }
	return worker, clock
}

func TestChannelTestWorkerRunsCycleWithDefaults(t *testing.T) {
	store := &fakeChannelTestStore{channels: []sqlc.ListChannelsForCredentialTestRow{{ID: 7}, {ID: 8}}}
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

// TestChannelTestWorkerDowngradesBenignSkips 保证巡检把「配置态/生命周期竞态」记为 Info 跳过、
// 真实检测失败仍记 WARN；且任一情况都不中断本轮（照常清理日志、返回 worked）。
func TestChannelTestWorkerDowngradesBenignSkips(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantLevel zapcore.Level
		wantMsg   string
	}{
		{
			name:      "no enabled model binding logs info skip",
			err:       failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage("channel has no enabled model binding to test"), failure.WithField("field", "model")),
			wantLevel: zapcore.InfoLevel,
			wantMsg:   "channel auto-test skipped",
		},
		{
			name:      "channel deleted mid-sweep logs info skip",
			err:       failure.New(failure.CodeAdminNotFound, failure.WithMessage("channel not found")),
			wantLevel: zapcore.InfoLevel,
			wantMsg:   "channel auto-test skipped",
		},
		{
			name:      "credential/store failure stays warn",
			err:       failure.New(failure.CodeAdminStoreFailed, failure.WithMessage("boom")),
			wantLevel: zapcore.WarnLevel,
			wantMsg:   "channel auto-test execution failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.InfoLevel)
			store := &fakeChannelTestStore{channels: []sqlc.ListChannelsForCredentialTestRow{{ID: 51}}}
			tester := &fakeChannelTester{err: tc.err}
			worker := NewChannelTestWorker(store, tester, nil, zap.New(core))
			clock := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
			worker.now = func() time.Time { return clock }

			worked, err := worker.RunOnce(context.Background())
			if err != nil || !worked {
				t.Fatalf("want worked,nil; got %v,%v", worked, err)
			}

			entries := logs.All()
			if len(entries) != 1 {
				t.Fatalf("log entries = %d, want 1: %+v", len(entries), entries)
			}
			if got := entries[0]; got.Level != tc.wantLevel || got.Message != tc.wantMsg {
				t.Fatalf("log = %v %q, want %v %q", got.Level, got.Message, tc.wantLevel, tc.wantMsg)
			}

			// 跳过/失败都不应中断本轮：仍按渠道清理检测日志。
			if len(store.pruned) != 1 || store.pruned[0].ChannelID != 51 {
				t.Fatalf("pruned = %+v, want channel 51 pruned once", store.pruned)
			}
		})
	}
}

func TestChannelTestWorkerName(t *testing.T) {
	worker := NewChannelTestWorker(&fakeChannelTestStore{}, &fakeChannelTester{}, nil, zap.NewNop())
	if worker.Name() != "channel_test" {
		t.Fatalf("name = %q", worker.Name())
	}
}
