package appsettings

import (
	"context"
	"testing"
	"time"

	messagesadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
)

// fakeStore 是内存版 app_settings 存储,用于单测。
type fakeStore struct {
	data     map[string][]byte
	getCalls int
	getErr   error
}

func newFakeStore() *fakeStore { return &fakeStore{data: map[string][]byte{}} }

func (f *fakeStore) GetAppSetting(_ context.Context, key string) ([]byte, error) {
	f.getCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	v, ok := f.data[key]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	return v, nil
}

func (f *fakeStore) UpsertAppSetting(_ context.Context, arg sqlc.UpsertAppSettingParams) error {
	f.data[arg.Key] = arg.Value
	return nil
}

func TestGetAnthropicBetaPolicyDefaultWhenAbsent(t *testing.T) {
	got, err := GetAnthropicBetaPolicy(context.Background(), newFakeStore())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	def := messagesadapter.DefaultBetaPolicy()
	if got.Mode != def.Mode {
		t.Fatalf("mode = %q, want %q", got.Mode, def.Mode)
	}
}

func TestSetThenGetAnthropicBetaPolicyRoundtrip(t *testing.T) {
	store := newFakeStore()
	want := messagesadapter.BetaPolicy{
		Mode: messagesadapter.BetaModePassthrough,
		List: []string{"context-1m-2025-08-07"},
	}
	if err := SetAnthropicBetaPolicy(context.Background(), store, want); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := GetAnthropicBetaPolicy(context.Background(), store)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Mode != want.Mode || len(got.List) != 1 || got.List[0] != want.List[0] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestSetAnthropicBetaPolicyRejectsInvalidMode(t *testing.T) {
	err := SetAnthropicBetaPolicy(context.Background(), newFakeStore(), messagesadapter.BetaPolicy{Mode: "bogus"})
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestAnthropicBetaProviderCachesWithinTTL(t *testing.T) {
	store := newFakeStore()
	_ = SetAnthropicBetaPolicy(context.Background(), store, messagesadapter.BetaPolicy{
		Mode: messagesadapter.BetaModePassthrough,
	})
	p := NewAnthropicBetaProvider(store, time.Minute, nil)

	_ = p.BetaPolicy(context.Background())
	_ = p.BetaPolicy(context.Background())
	got := p.BetaPolicy(context.Background())

	if got.Mode != messagesadapter.BetaModePassthrough {
		t.Fatalf("mode = %q, want passthrough", got.Mode)
	}
	// 三次读取只应打库一次(TTL 内命中缓存)。
	if store.getCalls != 1 {
		t.Fatalf("getCalls = %d, want 1 (TTL cache)", store.getCalls)
	}
}

func TestAnthropicBetaProviderFallsBackToDefaultOnError(t *testing.T) {
	store := newFakeStore()
	store.getErr = context.DeadlineExceeded
	p := NewAnthropicBetaProvider(store, time.Minute, nil)

	got := p.BetaPolicy(context.Background())
	if got.Mode != messagesadapter.DefaultBetaPolicy().Mode {
		t.Fatalf("mode = %q, want default", got.Mode)
	}
}
