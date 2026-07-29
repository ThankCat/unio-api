package lifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
)

// fakeStickyStore 记录 sticky 存取调用，供绑定语义断言。
type fakeStickyStore struct {
	bindings map[string]int64
	boundAt  map[string]time.Time

	bindCalls    []string
	rebindCalls  []string
	clearCalls   []string
	refreshCalls []stickyRefreshCall
	lastTTL      time.Duration
}

type stickyRefreshCall struct {
	key       string
	channelID int64
	ttl       time.Duration
}

func newFakeStickyStore() *fakeStickyStore {
	return &fakeStickyStore{bindings: map[string]int64{}, boundAt: map[string]time.Time{}}
}

func (s *fakeStickyStore) Lookup(_ context.Context, key string) (int64, time.Time, bool) {
	id, ok := s.bindings[key]
	return id, s.boundAt[key], ok
}

func (s *fakeStickyStore) Bind(_ context.Context, key string, channelID int64, ttl time.Duration) {
	s.bindCalls = append(s.bindCalls, key)
	s.lastTTL = ttl
	// SETNX 语义：已有绑定不覆盖。
	if _, exists := s.bindings[key]; !exists {
		s.bindings[key] = channelID
		s.boundAt[key] = time.Now()
	}
}

func (s *fakeStickyStore) Rebind(_ context.Context, key string, channelID int64, ttl time.Duration) {
	s.rebindCalls = append(s.rebindCalls, key)
	s.lastTTL = ttl
	s.bindings[key] = channelID
	s.boundAt[key] = time.Now()
}

func (s *fakeStickyStore) Clear(_ context.Context, key string) {
	s.clearCalls = append(s.clearCalls, key)
	delete(s.bindings, key)
	delete(s.boundAt, key)
}

func (s *fakeStickyStore) RefreshIfBound(_ context.Context, key string, channelID int64, ttl time.Duration) {
	s.refreshCalls = append(s.refreshCalls, stickyRefreshCall{key: key, channelID: channelID, ttl: ttl})
	if s.bindings[key] != channelID {
		return
	}
	s.boundAt[key] = time.Now()
}

func stickyResolveParams(sessionKey string) StickyResolveParams {
	routeID := int64(7)
	return StickyResolveParams{
		Protocol:   routing.ProtocolOpenAI,
		RouteID:    &routeID,
		APIKeyID:   42,
		SessionKey: sessionKey,
		Mode:       "balanced",
		Candidates: []routing.ChatRouteCandidate{
			stickyCandidate(1, nil, nil), stickyCandidate(7, nil, nil),
			stickyCandidate(101, nil, nil), stickyCandidate(202, nil, nil),
		},
	}
}

func stickyCandidate(channelID int64, enabled *bool, ttl *time.Duration) routing.ChatRouteCandidate {
	return routing.ChatRouteCandidate{
		Channel:       channel.Runtime{ID: channelID},
		StickyEnabled: enabled,
		StickyTTL:     ttl,
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

	session.BindSuccess(context.Background(), stickyCandidate(101, nil, nil))
	if len(store.bindCalls) != 1 || len(store.rebindCalls) != 0 {
		t.Fatalf("expected exactly one Bind and no Rebind, got bind=%d rebind=%d", len(store.bindCalls), len(store.rebindCalls))
	}
	if store.lastTTL != 30*time.Minute {
		t.Fatalf("expected TTL 30m, got %v", store.lastTTL)
	}

	// 二轮：lookup 命中本身不续期；同渠道成功后滑动续完整 TTL。
	second := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	if second.BoundChannelID() != 101 {
		t.Fatalf("expected bound channel 101, got %d", second.BoundChannelID())
	}
	if second.ResolvedChannelID() != 101 {
		t.Fatalf("expected resolved channel 101, got %d", second.ResolvedChannelID())
	}
	if len(store.refreshCalls) != 0 {
		t.Fatalf("lookup must not refresh sticky TTL: success=%v", store.refreshCalls)
	}
	second.BindSuccess(context.Background(), stickyCandidate(101, nil, nil))
	if len(store.bindCalls) != 1 || len(store.rebindCalls) != 0 {
		t.Fatalf("same-channel success must not bind/rebind, got bind=%d rebind=%d", len(store.bindCalls), len(store.rebindCalls))
	}
	if len(store.refreshCalls) != 1 || store.refreshCalls[0].channelID != 101 || store.refreshCalls[0].ttl != 30*time.Minute {
		t.Fatalf("same-channel success must refresh full TTL, got %+v", store.refreshCalls)
	}
}

// TestStickyRebindAfterFailover 验证 failover 成功（胜出渠道 ≠ 绑定渠道）后改绑（决议 2/3）。
func TestStickyRebindAfterFailover(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)

	session := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	session.BindSuccess(context.Background(), stickyCandidate(101, nil, nil))

	second := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	second.BindSuccess(context.Background(), stickyCandidate(202, nil, nil))
	if len(store.rebindCalls) != 1 {
		t.Fatalf("expected one Rebind after failover, got %d", len(store.rebindCalls))
	}
	if got, _, _ := store.Lookup(context.Background(), second.key); got != 202 {
		t.Fatalf("expected rebind to 202, got %d", got)
	}
}

// TestStickyClearSemantics 验证硬摘除清绑定：ClearBinding / ClearIfBound（仅命中绑定渠道时）。
func TestStickyClearSemantics(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)

	session := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	session.BindSuccess(context.Background(), stickyCandidate(101, nil, nil))

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

// TestStickyDisabledPaths 验证 Channel 三态、fixed、无会话键与 nil router/session。
func TestStickyDisabledPaths(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)

	// 全局默认关：请求仍保留 sticky 上下文，以便显式开启的 Channel 成功后建绑；继承渠道不建绑。
	router.SetConfig(false, time.Hour, 0, 0)
	session := router.Resolve(context.Background(), stickyResolveParams("k"))
	session.BindSuccess(context.Background(), stickyCandidate(1, nil, nil))
	if len(store.bindCalls) != 0 {
		t.Fatal("inherited channel must not bind when global default is off")
	}

	// Channel 显式开启压过全局默认关。
	enabled := true
	ttl := 10 * time.Minute
	session.BindSuccess(context.Background(), stickyCandidate(1, &enabled, &ttl))
	if len(store.bindCalls) != 1 || store.lastTTL != ttl {
		t.Fatal("explicit channel policy must bind with channel TTL")
	}

	// Channel 显式关闭会清旧绑定。
	router.SetConfig(true, time.Hour, 500*time.Millisecond, 100*time.Millisecond)
	disabled := false
	session.BindSuccess(context.Background(), stickyCandidate(1, &disabled, nil))
	if session.BoundChannelID() != 0 || len(store.clearCalls) != 1 {
		t.Fatal("disabled channel must clear the old binding")
	}

	params := stickyResolveParams("fixed")
	params.Mode = "fixed"
	if s := router.Resolve(context.Background(), params); s.Enabled() {
		t.Fatal("fixed routes must skip sticky")
	}

	// 无会话键。
	if s := router.Resolve(context.Background(), stickyResolveParams("")); s.Enabled() {
		t.Fatal("expected disabled without session key")
	}

	// nil router / nil session：全部方法安全 no-op。
	var nilRouter *StickyRouter
	session = nilRouter.Resolve(context.Background(), stickyResolveParams("k"))
	if session.Enabled() || session.BoundChannelID() != 0 {
		t.Fatal("nil router must resolve to disabled session")
	}
	session.BindSuccess(context.Background(), stickyCandidate(1, nil, nil))
	session.ClearBinding(context.Background())
	session.ClearIfBound(context.Background(), 1)
}

func TestStickyResolveAppliesChannelPolicyAndTTLChanges(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)
	router.SetConfig(true, 30*time.Minute, 0, 0)

	params := stickyResolveParams("ttl-edit")
	session := router.Resolve(context.Background(), params)
	session.BindSuccess(context.Background(), stickyCandidate(7, nil, nil))
	store.boundAt[session.key] = time.Now().Add(-20 * time.Minute)

	resolved := router.Resolve(context.Background(), params)
	if resolved.BoundChannelID() != 7 || len(store.refreshCalls) != 0 {
		t.Fatalf("inherited policy must retain without lookup refresh: session=%+v refresh=%v", resolved, store.refreshCalls)
	}
	resolved.BindSuccess(context.Background(), stickyCandidate(7, nil, nil))
	if len(store.refreshCalls) != 1 || store.refreshCalls[0].ttl != 30*time.Minute {
		t.Fatalf("successful inherited channel must refresh full TTL, got %+v", store.refreshCalls)
	}

	shortTTL := 10 * time.Minute
	enabled := true
	store.boundAt[session.key] = time.Now().Add(-20 * time.Minute)
	params.Candidates[1] = stickyCandidate(7, &enabled, &shortTTL)
	expired := router.Resolve(context.Background(), params)
	if expired.BoundChannelID() != 0 || len(store.clearCalls) != 1 {
		t.Fatalf("shortened Channel TTL must expire the old binding lazily: bound=%d clears=%d", expired.BoundChannelID(), len(store.clearCalls))
	}
}

func TestStickyResolveClearsBindingWhenChannelDisablesSticky(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)
	params := stickyResolveParams("disabled-edit")
	session := router.Resolve(context.Background(), params)
	session.BindSuccess(context.Background(), stickyCandidate(7, nil, nil))

	disabled := false
	params.Candidates[1] = stickyCandidate(7, &disabled, nil)
	resolved := router.Resolve(context.Background(), params)
	if resolved.BoundChannelID() != 0 || resolved.ResolvedChannelID() != 7 || len(store.clearCalls) != 1 {
		t.Fatalf("disabled Channel must lazily clear its old binding while preserving trace facts: %+v", resolved)
	}
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

// TestPrepareCandidatesStickyPinOverridesMode 验证 sticky 置顶绝对优先于 balanced 排序（R5），
// 且渠道不在池时 StickyPinned=false（调用方据此清绑定）。
func TestPrepareCandidatesStickyPinOverridesMode(t *testing.T) {
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
		t.Fatal("expected StickyPinnedNonPreferred when sticky channel was not first")
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
