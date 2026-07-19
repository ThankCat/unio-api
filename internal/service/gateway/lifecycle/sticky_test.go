package lifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/routing"
)

// fakeStickyStore 记录 sticky 存取调用，供绑定语义断言。
type fakeStickyStore struct {
	bindings map[string]int64

	bindCalls   []string
	rebindCalls []string
	clearCalls  []string
	lastTTL     time.Duration
}

func newFakeStickyStore() *fakeStickyStore {
	return &fakeStickyStore{bindings: map[string]int64{}}
}

func (s *fakeStickyStore) Lookup(_ context.Context, key string) (int64, bool) {
	id, ok := s.bindings[key]
	return id, ok
}

func (s *fakeStickyStore) Bind(_ context.Context, key string, channelID int64, ttl time.Duration) {
	s.bindCalls = append(s.bindCalls, key)
	s.lastTTL = ttl
	// SETNX 语义：已有绑定不覆盖。
	if _, exists := s.bindings[key]; !exists {
		s.bindings[key] = channelID
	}
}

func (s *fakeStickyStore) Rebind(_ context.Context, key string, channelID int64, ttl time.Duration) {
	s.rebindCalls = append(s.rebindCalls, key)
	s.lastTTL = ttl
	s.bindings[key] = channelID
}

func (s *fakeStickyStore) Clear(_ context.Context, key string) {
	s.clearCalls = append(s.clearCalls, key)
	delete(s.bindings, key)
}

func stickyResolveParams(sessionKey string) StickyResolveParams {
	routeID := int64(7)
	return StickyResolveParams{
		Protocol:   routing.ProtocolOpenAI,
		RouteID:    &routeID,
		APIKeyID:   42,
		SessionKey: sessionKey,
	}
}

// TestStickyResolveMissThenBindSuccess 验证首轮 miss → attempt 成功后 Bind（SETNX 路径，R8）。
func TestStickyResolveMissThenBindSuccess(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)
	router.SetConfig(true, 30*time.Minute, 500*time.Millisecond, 100*time.Millisecond)

	session := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	if !session.Enabled() {
		t.Fatal("expected sticky session enabled")
	}
	if session.BoundChannelID() != 0 {
		t.Fatalf("expected miss, got bound channel %d", session.BoundChannelID())
	}

	session.BindSuccess(context.Background(), 101)
	if len(store.bindCalls) != 1 || len(store.rebindCalls) != 0 {
		t.Fatalf("expected exactly one Bind and no Rebind, got bind=%d rebind=%d", len(store.bindCalls), len(store.rebindCalls))
	}
	if store.lastTTL != 30*time.Minute {
		t.Fatalf("expected TTL 30m, got %v", store.lastTTL)
	}

	// 二轮：lookup 命中同渠道 → 成功后不刷新（绝对 TTL，R2）。
	second := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	if second.BoundChannelID() != 101 {
		t.Fatalf("expected bound channel 101, got %d", second.BoundChannelID())
	}
	if second.ResolvedChannelID() != 101 {
		t.Fatalf("expected resolved channel 101, got %d", second.ResolvedChannelID())
	}
	second.BindSuccess(context.Background(), 101)
	if len(store.bindCalls) != 1 || len(store.rebindCalls) != 0 {
		t.Fatalf("same-channel success must not touch binding, got bind=%d rebind=%d", len(store.bindCalls), len(store.rebindCalls))
	}
}

// TestStickyRebindAfterFailover 验证 failover 成功（胜出渠道 ≠ 绑定渠道）后改绑（决议 2/3）。
func TestStickyRebindAfterFailover(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)

	session := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	session.BindSuccess(context.Background(), 101)

	second := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	second.BindSuccess(context.Background(), 202)
	if len(store.rebindCalls) != 1 {
		t.Fatalf("expected one Rebind after failover, got %d", len(store.rebindCalls))
	}
	if got, _ := store.Lookup(context.Background(), second.key); got != 202 {
		t.Fatalf("expected rebind to 202, got %d", got)
	}
}

// TestStickyClearSemantics 验证硬摘除清绑定：ClearBinding / ClearIfBound（仅命中绑定渠道时）。
func TestStickyClearSemantics(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)

	session := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	session.BindSuccess(context.Background(), 101)

	second := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	// 非绑定渠道被熔断跳过：不清。
	second.ClearIfBound(context.Background(), 999)
	if len(store.clearCalls) != 0 {
		t.Fatalf("ClearIfBound on non-bound channel must not clear, got %d clears", len(store.clearCalls))
	}
	// 绑定渠道被熔断跳过：清。
	second.ClearIfBound(context.Background(), 101)
	if len(store.clearCalls) != 1 {
		t.Fatalf("expected one clear, got %d", len(store.clearCalls))
	}
	if second.BoundChannelID() != 0 {
		t.Fatalf("expected bound channel reset after clear, got %d", second.BoundChannelID())
	}
	if second.ResolvedChannelID() != 101 {
		t.Fatalf("resolved channel must remain stable for tracing, got %d", second.ResolvedChannelID())
	}
	// 已清后重复清：no-op。
	second.ClearBinding(context.Background())
	if len(store.clearCalls) != 1 {
		t.Fatalf("expected clear to be idempotent per session, got %d", len(store.clearCalls))
	}
}

// TestStickyDisabledPaths 验证各类不粘场景：全局默认关、线路覆盖关、无会话键、nil router/session。
func TestStickyDisabledPaths(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)

	// 全局默认关。
	router.SetConfig(false, time.Hour, 0, 0)
	if s := router.Resolve(context.Background(), stickyResolveParams("k")); s.Enabled() {
		t.Fatal("expected disabled when global default off")
	}

	// 线路覆盖开（压过全局关）。
	enabled := true
	params := stickyResolveParams("k")
	params.RouteStickyEnabled = &enabled
	if s := router.Resolve(context.Background(), params); !s.Enabled() {
		t.Fatal("expected route override to enable sticky")
	}

	// 线路覆盖关（压过全局开）。
	router.SetConfig(true, time.Hour, 500*time.Millisecond, 100*time.Millisecond)
	disabled := false
	params = stickyResolveParams("k")
	params.RouteStickyEnabled = &disabled
	if s := router.Resolve(context.Background(), params); s.Enabled() {
		t.Fatal("expected route override to disable sticky")
	}

	// 无会话键。
	if s := router.Resolve(context.Background(), stickyResolveParams("")); s.Enabled() {
		t.Fatal("expected disabled without session key")
	}

	// nil router / nil session：全部方法安全 no-op。
	var nilRouter *StickyRouter
	session := nilRouter.Resolve(context.Background(), stickyResolveParams("k"))
	if session.Enabled() || session.BoundChannelID() != 0 {
		t.Fatal("nil router must resolve to disabled session")
	}
	session.BindSuccess(context.Background(), 1)
	session.ClearBinding(context.Background())
	session.ClearIfBound(context.Background(), 1)
}

// TestStickyRedisKeyShape 验证键格式与会话键哈希（R6）：客户端可控原始键不直接入 Redis 键。
func TestStickyRedisKeyShape(t *testing.T) {
	key := stickyRedisKey(routing.ProtocolOpenAI, 7, 42, "raw-session-key")
	if !strings.HasPrefix(key, "sticky:openai:7:42:") {
		t.Fatalf("unexpected key prefix: %s", key)
	}
	if strings.Contains(key, "raw-session-key") {
		t.Fatalf("raw session key must be hashed, got %s", key)
	}
	hash := strings.TrimPrefix(key, "sticky:openai:7:42:")
	if len(hash) != 32 {
		t.Fatalf("expected 32-hex hash, got %q (len %d)", hash, len(hash))
	}
	// 同键稳定、异键不同。
	if key != stickyRedisKey(routing.ProtocolOpenAI, 7, 42, "raw-session-key") {
		t.Fatal("key derivation must be deterministic")
	}
	if key == stickyRedisKey(routing.ProtocolOpenAI, 7, 43, "raw-session-key") {
		t.Fatal("different api key must yield different redis key")
	}
}

// TestPrepareCandidatesStickyPinOverridesModeAndDemote 验证 sticky 置顶绝对优先于 balanced 排序
// 与失败软冷却 demote（R5），且渠道不在池时 StickyPinned=false（调用方据此清绑定）。
func TestPrepareCandidatesStickyPinOverridesModeAndDemote(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{
		allowed: map[int64]bool{1: true, 2: true, 3: true},
	})

	params := PrepareCandidatesParams{
		Protocol: "openai",
		Candidates: []routing.ChatRouteCandidate{
			candidateRoute(1, "a"),
			candidateRoute(2, "b"),
			candidateRoute(3, "c"),
		},
		EstimateInputTokens: func(_ context.Context, _ routing.ChatRouteCandidate) (int64, error) {
			return 1, nil
		},
		// channel 2 处于失败软冷却（demote 到队尾）——sticky 置顶必须压过它。
		FailurePreferred: func(c routing.ChatRouteCandidate) bool {
			return c.Channel.ID != 2
		},
		StickyChannelID: 2,
	}

	plan, err := executor.PrepareCandidates(context.Background(), params)
	if err != nil {
		t.Fatalf("PrepareCandidates returned error: %v", err)
	}
	if !plan.StickyPinned {
		t.Fatal("expected sticky channel pinned")
	}
	if plan.Candidates[0].Route.Channel.ID != 2 {
		t.Fatalf("expected sticky channel 2 pinned to front, got %d", plan.Candidates[0].Route.Channel.ID)
	}
	if !plan.StickyPinnedNonPreferred {
		t.Fatal("expected StickyPinnedNonPreferred when sticky channel was demoted away from front")
	}
	if len(plan.Candidates) != 3 {
		t.Fatalf("pin must not drop candidates, got %d", len(plan.Candidates))
	}

	// 粘住渠道不在候选池（硬摘除）：StickyPinned=false，其余顺序不受影响。
	params.StickyChannelID = 99
	plan, err = executor.PrepareCandidates(context.Background(), params)
	if err != nil {
		t.Fatalf("PrepareCandidates returned error: %v", err)
	}
	if plan.StickyPinned {
		t.Fatal("expected StickyPinned=false when sticky channel absent")
	}
}
