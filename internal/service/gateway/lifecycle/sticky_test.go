package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/logfields"
	"github.com/ThankCat/unio-gateway/internal/platform/stickysession"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
)

// fakeStickyStore 是 §10.4 三个 CAS 写操作的内存实现，语义与 Redis 版严格一致：
// 只在 (channel_id, binding_version) 完全匹配时才 refresh/clear。
type fakeStickyStore struct {
	bindings map[string]stickysession.Binding

	bindCalls    []stickyWriteCall
	refreshCalls []stickyWriteCall
	clearCalls   []stickyWriteCall

	nextVersion int64
	// unavailable 为 true 时所有操作报告 store_unavailable（fail-open 验证）。
	unavailable bool
}

type stickyWriteCall struct {
	key            string
	channelID      int64
	bindingVersion int64
	ttl            time.Duration
}

func newFakeStickyStore() *fakeStickyStore {
	return &fakeStickyStore{bindings: map[string]stickysession.Binding{}}
}

func (s *fakeStickyStore) allocVersion() int64 {
	s.nextVersion++
	return s.nextVersion
}

func (s *fakeStickyStore) Lookup(_ context.Context, key string) stickysession.LookupResult {
	if s.unavailable {
		return stickysession.LookupResult{StoreUnavailable: true}
	}
	bound, ok := s.bindings[key]
	if !ok {
		return stickysession.LookupResult{}
	}
	return stickysession.LookupResult{Found: true, Binding: bound}
}

func (s *fakeStickyStore) BindIfAbsent(
	_ context.Context, key string, channelID int64, ttl time.Duration,
) (stickysession.Binding, stickysession.CASResult) {
	s.bindCalls = append(s.bindCalls, stickyWriteCall{key: key, channelID: channelID, ttl: ttl})
	if s.unavailable {
		return stickysession.Binding{}, stickysession.CASResult{StoreUnavailable: true}
	}
	if _, exists := s.bindings[key]; exists {
		return stickysession.Binding{}, stickysession.CASResult{Conflict: true}
	}
	next := stickysession.Binding{
		ChannelID: channelID, BindingVersion: s.allocVersion(), LastSuccessAt: time.Now(),
	}
	s.bindings[key] = next
	return next, stickysession.CASResult{Applied: true}
}

func (s *fakeStickyStore) RefreshIfCurrent(
	_ context.Context, key string, channelID, bindingVersion int64, ttl time.Duration,
) (stickysession.Binding, stickysession.CASResult) {
	s.refreshCalls = append(s.refreshCalls, stickyWriteCall{
		key: key, channelID: channelID, bindingVersion: bindingVersion, ttl: ttl,
	})
	if s.unavailable {
		return stickysession.Binding{}, stickysession.CASResult{StoreUnavailable: true}
	}
	current, ok := s.bindings[key]
	if !ok || current.ChannelID != channelID || current.BindingVersion != bindingVersion {
		return stickysession.Binding{}, stickysession.CASResult{Conflict: true}
	}
	current.LastSuccessAt = time.Now()
	s.bindings[key] = current
	return current, stickysession.CASResult{Applied: true}
}

func (s *fakeStickyStore) ClearIfCurrent(
	_ context.Context, key string, channelID, bindingVersion int64,
) stickysession.CASResult {
	s.clearCalls = append(s.clearCalls, stickyWriteCall{
		key: key, channelID: channelID, bindingVersion: bindingVersion,
	})
	if s.unavailable {
		return stickysession.CASResult{StoreUnavailable: true}
	}
	current, ok := s.bindings[key]
	if !ok || current.ChannelID != channelID || current.BindingVersion != bindingVersion {
		return stickysession.CASResult{Conflict: true}
	}
	delete(s.bindings, key)
	return stickysession.CASResult{Applied: true}
}

func stickyResolveParams(sessionKey string) StickyResolveParams {
	routeID := int64(7)
	return StickyResolveParams{
		Protocol:   routing.ProtocolOpenAI,
		RouteID:    &routeID,
		APIKeyID:   42,
		ModelID:    31,
		SessionKey: sessionKey,
		Mode:       "balanced",
		Candidates: []routing.ChatRouteCandidate{
			stickyCandidate(1, nil, nil), stickyCandidate(7, nil, nil),
			stickyCandidate(101, nil, nil), stickyCandidate(202, nil, nil),
		},
	}
}

func TestStickyActionPropagatesToRequestSummary(t *testing.T) {
	ctx, fields := logfields.NewContext(context.Background(), "trace-sticky")
	router := NewStickyRouter(newFakeStickyStore())
	params := stickyResolveParams("session-action")
	params.Source = "prompt_cache_key"

	session := router.Resolve(ctx, params)
	if got := stringField(fields.ZapFields(), "sticky_action"); got != string(StickyActionMiss) {
		t.Fatalf("sticky action after lookup = %q", got)
	}
	session.BindSuccess(ctx, stickyCandidate(101, nil, nil))
	if got := stringField(fields.ZapFields(), "sticky_action"); got != string(StickyActionBindIfAbsent) {
		t.Fatalf("sticky action after bind = %q", got)
	}
}

func stringField(fields []zap.Field, key string) string {
	for _, field := range fields {
		if field.Key == key {
			return field.String
		}
	}
	return ""
}

func stickyCandidate(channelID int64, enabled *bool, ttl *time.Duration) routing.ChatRouteCandidate {
	return routing.ChatRouteCandidate{
		Channel:       channel.Runtime{ID: channelID},
		StickyEnabled: enabled,
		StickyTTL:     ttl,
	}
}

// TestStickyMissThenBindIfAbsentThenRefresh 覆盖 §10.5 的建绑与续期主路径。
func TestStickyMissThenBindIfAbsentThenRefresh(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)
	router.SetConfig(true, 30*time.Minute)

	session := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	if !session.Enabled() || session.BoundChannelID() != 0 {
		t.Fatalf("expected an enabled miss, got %+v", session.Audit())
	}
	if session.Audit().Action != StickyActionMiss {
		t.Fatalf("miss action = %q", session.Audit().Action)
	}

	session.BindSuccess(context.Background(), stickyCandidate(101, nil, nil))
	if len(store.bindCalls) != 1 || len(store.refreshCalls) != 0 || len(store.clearCalls) != 0 {
		t.Fatalf("first success must only BindIfAbsent: bind=%d refresh=%d clear=%d",
			len(store.bindCalls), len(store.refreshCalls), len(store.clearCalls))
	}
	if store.bindCalls[0].ttl != 30*time.Minute {
		t.Fatalf("bind TTL = %v want 30m", store.bindCalls[0].ttl)
	}
	audit := session.Audit()
	if audit.Action != StickyActionBindIfAbsent || audit.BeforeChannelID != 0 || audit.AfterChannelID != 101 {
		t.Fatalf("bind audit = %+v", audit)
	}

	// 二轮：lookup 命中本身不续期；同渠道完整成功才 RefreshIfCurrent。
	second := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	if second.BoundChannelID() != 101 || second.Audit().Action != StickyActionHit {
		t.Fatalf("expected a hit on channel 101, got %+v", second.Audit())
	}
	if len(store.refreshCalls) != 0 {
		t.Fatalf("lookup must not refresh TTL: %+v", store.refreshCalls)
	}
	second.BindSuccess(context.Background(), stickyCandidate(101, nil, nil))
	if len(store.bindCalls) != 1 || len(store.refreshCalls) != 1 {
		t.Fatalf("same-channel success must refresh, not rebind: bind=%d refresh=%d",
			len(store.bindCalls), len(store.refreshCalls))
	}
	if store.refreshCalls[0].channelID != 101 || store.refreshCalls[0].ttl != 30*time.Minute {
		t.Fatalf("refresh must carry the bound identity and full TTL: %+v", store.refreshCalls[0])
	}
	if got := second.Audit().Action; got != StickyActionRefreshIfCurrent {
		t.Fatalf("refresh action = %q", got)
	}
}

// TestStickyTemporaryBypassKeepsOriginalBinding 冻结 §10.6：绕行成功不改绑、不续期任何一方，
// 且绝不产生隐式 Rebind。
func TestStickyTemporaryBypassKeepsOriginalBinding(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)

	first := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	first.BindSuccess(context.Background(), stickyCandidate(101, nil, nil))
	original := store.bindings[first.key]

	second := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	second.PreserveOnTemporaryBypass(context.Background(), 101, "concurrency_full")
	second.BindSuccess(context.Background(), stickyCandidate(202, nil, nil))

	if len(store.bindCalls) != 1 || len(store.refreshCalls) != 0 || len(store.clearCalls) != 0 {
		t.Fatalf("temporary bypass must not write anything: bind=%d refresh=%d clear=%d",
			len(store.bindCalls), len(store.refreshCalls), len(store.clearCalls))
	}
	if got := store.bindings[first.key]; got.ChannelID != 101 || got.BindingVersion != original.BindingVersion ||
		!got.LastSuccessAt.Equal(original.LastSuccessAt) {
		t.Fatalf("original binding must be untouched: got %+v want %+v", got, original)
	}
	if action := second.Audit().Action; action != StickyActionPreserveOnTemporaryBypass {
		t.Fatalf("bypass action = %q", action)
	}
	if second.BoundChannelID() != 101 {
		t.Fatalf("bypass must keep the original binding locally too, got %d", second.BoundChannelID())
	}
}

// TestStickyPermanentFailureClearsThenBindsSecondChannel 冻结 §10.9：改绑必须表达为
// 先 ClearIfCurrent(A) 再 BindIfAbsent(B)，且审计能分别解释两步。
func TestStickyPermanentFailureClearsThenBindsSecondChannel(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)

	first := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	first.BindSuccess(context.Background(), stickyCandidate(101, nil, nil))

	second := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	second.ClearOnPermanentFailure(context.Background(), "upstream_server_error")
	if len(store.clearCalls) != 1 || store.clearCalls[0].channelID != 101 {
		t.Fatalf("expected a CAS clear of channel 101: %+v", store.clearCalls)
	}
	clearAudit := second.Audit()
	if clearAudit.Action != StickyActionClearIfCurrent || clearAudit.Reason != "upstream_server_error" ||
		clearAudit.BeforeChannelID != 101 || clearAudit.AfterChannelID != 0 {
		t.Fatalf("clear audit must explain why A was dropped: %+v", clearAudit)
	}

	second.BindSuccess(context.Background(), stickyCandidate(202, nil, nil))
	if len(store.bindCalls) != 2 {
		t.Fatalf("channel B must be bound via BindIfAbsent, not a rebind: %+v", store.bindCalls)
	}
	bindAudit := second.Audit()
	if bindAudit.Action != StickyActionBindIfAbsent || bindAudit.AfterChannelID != 202 ||
		bindAudit.BeforeChannelID != 101 {
		t.Fatalf("bind audit must show both the origin and the new binding: %+v", bindAudit)
	}
	if got := store.bindings[second.key]; got.ChannelID != 202 {
		t.Fatalf("final binding = %+v want channel 202", got)
	}
}

// TestStickyCASConflictNeverOverwritesTheWinner 冻结 §10.9：CAS 冲突时本请求既不删也不覆盖。
func TestStickyCASConflictNeverOverwritesTheWinner(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)

	loser := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	loser.BindSuccess(context.Background(), stickyCandidate(101, nil, nil))

	// 另一个请求已经把绑定换掉了（清 A 后绑 B）。
	stale := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	winnerKey := stale.key
	delete(store.bindings, winnerKey)
	winner := stickysession.Binding{ChannelID: 202, BindingVersion: store.allocVersion(), LastSuccessAt: time.Now()}
	store.bindings[winnerKey] = winner

	// 慢请求带着旧身份尝试续期与清除，两者都必须失败且不动新绑定。
	stale.BindSuccess(context.Background(), stickyCandidate(101, nil, nil))
	if action := stale.Audit().Action; action != StickyActionCASConflict {
		t.Fatalf("stale refresh action = %q, want cas_conflict", action)
	}
	stale.ClearOnPermanentFailure(context.Background(), "upstream_server_error")
	if action := stale.Audit().Action; action != StickyActionCASConflict {
		t.Fatalf("stale clear action = %q, want cas_conflict", action)
	}
	if got := store.bindings[winnerKey]; got.ChannelID != 202 || got.BindingVersion != winner.BindingVersion {
		t.Fatalf("stale request modified the winning binding: %+v", got)
	}
}

// TestStickyClearIfBoundOnlyTargetsTheBoundChannel 验证非绑定渠道故障不影响绑定。
func TestStickyClearIfBoundOnlyTargetsTheBoundChannel(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)

	session := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	session.BindSuccess(context.Background(), stickyCandidate(101, nil, nil))

	second := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	second.ClearIfBound(context.Background(), 999, "open")
	if len(store.clearCalls) != 0 {
		t.Fatalf("non-bound channel must not clear: %+v", store.clearCalls)
	}
	second.ClearIfBound(context.Background(), 101, "open")
	if len(store.clearCalls) != 1 || second.BoundChannelID() != 0 {
		t.Fatalf("bound channel must clear: clears=%+v bound=%d", store.clearCalls, second.BoundChannelID())
	}
	if second.ResolvedChannelID() != 101 {
		t.Fatalf("resolved channel must stay stable for tracing, got %d", second.ResolvedChannelID())
	}
	// 已清后重复清：no-op。
	second.ClearOnPermanentFailure(context.Background(), "open")
	if len(store.clearCalls) != 1 {
		t.Fatalf("clear must be idempotent per session: %+v", store.clearCalls)
	}
}

// TestStickyStoreUnavailableIsFailOpen 冻结 §10.11：存储故障只记录动作，不阻断路由。
func TestStickyStoreUnavailableIsFailOpen(t *testing.T) {
	store := newFakeStickyStore()
	store.unavailable = true
	router := NewStickyRouter(store)

	session := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	if !session.Enabled() || session.BoundChannelID() != 0 {
		t.Fatalf("store failure must degrade to no binding, got %+v", session.Audit())
	}
	if action := session.Audit().Action; action != StickyActionStoreUnavailable {
		t.Fatalf("lookup failure action = %q", action)
	}
	session.BindSuccess(context.Background(), stickyCandidate(101, nil, nil))
	if action := session.Audit().Action; action != StickyActionStoreUnavailable {
		t.Fatalf("bind failure action = %q", action)
	}
	if session.BoundChannelID() != 0 {
		t.Fatalf("failed bind must not claim a binding, got %d", session.BoundChannelID())
	}
}

func TestStickyDisabledPaths(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)

	// 全局默认关：仍保留 sticky 上下文，以便显式开启的 Channel 成功后建绑；继承渠道不建绑。
	router.SetConfig(false, time.Hour)
	session := router.Resolve(context.Background(), stickyResolveParams("k"))
	session.BindSuccess(context.Background(), stickyCandidate(1, nil, nil))
	if len(store.bindCalls) != 0 {
		t.Fatal("inherited channel must not bind when the global default is off")
	}

	// Channel 显式开启压过全局默认关。
	enabled := true
	ttl := 10 * time.Minute
	session.BindSuccess(context.Background(), stickyCandidate(1, &enabled, &ttl))
	if len(store.bindCalls) != 1 || store.bindCalls[0].ttl != ttl {
		t.Fatalf("explicit channel policy must bind with the channel TTL: %+v", store.bindCalls)
	}

	// Channel 显式关闭会清旧绑定。
	router.SetConfig(true, time.Hour)
	disabled := false
	session.BindSuccess(context.Background(), stickyCandidate(1, &disabled, nil))
	if session.BoundChannelID() != 0 || len(store.clearCalls) != 1 {
		t.Fatalf("a sticky-disabled channel must clear the old binding: bound=%d clears=%d",
			session.BoundChannelID(), len(store.clearCalls))
	}

	params := stickyResolveParams("fixed")
	params.Mode = "fixed"
	if s := router.Resolve(context.Background(), params); s.Enabled() {
		t.Fatal("fixed routes must skip sticky")
	}
	if s := router.Resolve(context.Background(), stickyResolveParams("")); s.Enabled() {
		t.Fatal("expected disabled without a session key")
	}
	noModel := stickyResolveParams("k")
	noModel.ModelID = 0
	if s := router.Resolve(context.Background(), noModel); s.Enabled() {
		t.Fatal("sticky requires a resolved model id for the key")
	}

	// nil router / nil session：全部方法安全 no-op。
	var nilRouter *StickyRouter
	session = nilRouter.Resolve(context.Background(), stickyResolveParams("k"))
	if session.Enabled() || session.BoundChannelID() != 0 {
		t.Fatal("nil router must resolve to a disabled session")
	}
	session.BindSuccess(context.Background(), stickyCandidate(1, nil, nil))
	session.ClearOnPermanentFailure(context.Background(), "open")
	session.ClearIfBound(context.Background(), 1, "open")
	session.PreserveOnTemporaryBypass(context.Background(), 1, "concurrency_full")
	if session.Audit().Action != StickyActionDisabled {
		t.Fatalf("nil session audit = %+v", session.Audit())
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
		t.Fatalf("a sticky-disabled channel must clear its old binding while preserving trace facts: %+v",
			resolved.Audit())
	}
}

// TestStickyRedisKeyIncludesModelAndHashesSession 冻结 §10.1 的 key 形状。
func TestStickyRedisKeyIncludesModelAndHashesSession(t *testing.T) {
	key := stickyRedisKey(routing.ProtocolOpenAI, 7, 42, 31, "raw-session-key")
	if !strings.HasPrefix(key, "sticky:openai:7:42:31:") {
		t.Fatalf("unexpected key prefix: %s", key)
	}
	if strings.Contains(key, "raw-session-key") {
		t.Fatalf("raw session key must be hashed, got %s", key)
	}
	if hash := strings.TrimPrefix(key, "sticky:openai:7:42:31:"); len(hash) != 32 {
		t.Fatalf("expected a 32-hex hash, got %q (len %d)", hash, len(hash))
	}
	if key != stickyRedisKey(routing.ProtocolOpenAI, 7, 42, 31, "raw-session-key") {
		t.Fatal("key derivation must be deterministic")
	}
	// 同一会话换模型必须落到不同 key，避免继承与新模型无关的渠道选择。
	if key == stickyRedisKey(routing.ProtocolOpenAI, 7, 42, 32, "raw-session-key") {
		t.Fatal("different model must yield a different redis key")
	}
	if key == stickyRedisKey(routing.ProtocolOpenAI, 7, 43, 31, "raw-session-key") {
		t.Fatal("different api key must yield a different redis key")
	}
	if key == stickyRedisKey(routing.ProtocolAnthropic, 7, 42, 31, "raw-session-key") {
		t.Fatal("different protocol must yield a different redis key")
	}
}

// TestStickySessionsAreIsolatedPerModel 端到端确认换模型不会复用旧绑定。
func TestStickySessionsAreIsolatedPerModel(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)

	modelA := stickyResolveParams("shared-session")
	first := router.Resolve(context.Background(), modelA)
	first.BindSuccess(context.Background(), stickyCandidate(101, nil, nil))

	modelB := stickyResolveParams("shared-session")
	modelB.ModelID = 32
	second := router.Resolve(context.Background(), modelB)
	if second.BoundChannelID() != 0 || second.Audit().Action != StickyActionMiss {
		t.Fatalf("a different model must not inherit the binding: %+v", second.Audit())
	}
}

// TestClassifyStickyFailure 冻结 §10.7（清绑定）与 §10.8（不清绑定）的分界。
func TestClassifyStickyFailure(t *testing.T) {
	upstream := func(category adapter.UpstreamErrorCategory) error {
		return adapter.NewUpstreamError(category, adapter.UpstreamMetadata{}, errors.New("upstream"))
	}
	tests := []struct {
		name          string
		err           error
		wantClear     bool
		wantTemporary bool
	}{
		{name: "nil error", err: nil},
		{name: "upstream 5xx clears", err: upstream(adapter.UpstreamErrorServer), wantClear: true},
		{name: "first token timeout clears", err: upstream(adapter.UpstreamErrorTimeout), wantClear: true},
		{name: "401 credential clears", err: upstream(adapter.UpstreamErrorAuth), wantClear: true},
		{name: "403 permission clears", err: upstream(adapter.UpstreamErrorPermission), wantClear: true},
		{name: "429 preserves", err: upstream(adapter.UpstreamErrorRateLimit), wantTemporary: true},
		{name: "client cancel preserves", err: upstream(adapter.UpstreamErrorCanceled)},
		{name: "client bad request preserves", err: upstream(adapter.UpstreamErrorBadRequest)},
		{
			name: "gateway store fault preserves",
			err:  failure.New(failure.CodeGatewayBreakerStoreUnavailable),
		},
		{
			name: "runtime sync fault preserves",
			err:  failure.New(failure.CodeGatewayRuntimeSyncRequired),
		},
		{
			name:          "capacity exhausted preserves",
			err:           failure.New(failure.CodeRoutingChannelCapacityExhausted),
			wantTemporary: false,
		},
		{
			name:          "channel cooldown is a temporary bypass",
			err:           failure.New(failure.CodeGatewayChannelRateLimited),
			wantTemporary: true,
		},
		{name: "unclassified preserves", err: errors.New("plain")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyStickyFailure(tc.err)
			if got.clear != tc.wantClear {
				t.Fatalf("clear = %v want %v (verdict=%+v)", got.clear, tc.wantClear, got)
			}
			if got.temporaryBypass != tc.wantTemporary {
				t.Fatalf("temporaryBypass = %v want %v (verdict=%+v)",
					got.temporaryBypass, tc.wantTemporary, got)
			}
		})
	}
}

// TestApplyStickyAttemptFailureOnlyActsOnTheBoundChannel 验证失败渠道不是绑定渠道时不动绑定。
func TestApplyStickyAttemptFailureOnlyActsOnTheBoundChannel(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)
	session := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	session.BindSuccess(context.Background(), stickyCandidate(101, nil, nil))

	serverErr := adapter.NewUpstreamError(adapter.UpstreamErrorServer, adapter.UpstreamMetadata{}, errors.New("boom"))
	applyStickyAttemptFailure(context.Background(), session, 202, serverErr)
	if len(store.clearCalls) != 0 {
		t.Fatalf("failure on a non-bound channel must not clear: %+v", store.clearCalls)
	}
	applyStickyAttemptFailure(context.Background(), session, 101, serverErr)
	if len(store.clearCalls) != 1 || session.BoundChannelID() != 0 {
		t.Fatalf("failure on the bound channel must clear: clears=%+v bound=%d",
			store.clearCalls, session.BoundChannelID())
	}
}

// TestPrepareCandidatesStickyPinOverridesMode 验证 sticky 置顶绝对优先于评分排序，
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
		t.Fatal("expected the sticky channel to be pinned")
	}
	if plan.Candidates[0].Route.Channel.ID != 2 {
		t.Fatalf("expected sticky channel 2 pinned to front, got %d", plan.Candidates[0].Route.Channel.ID)
	}
	if !plan.StickyPinnedNonPreferred {
		t.Fatal("expected StickyPinnedNonPreferred when the sticky channel was not first")
	}
	if len(plan.Candidates) != 3 {
		t.Fatalf("pinning must not drop candidates, got %d", len(plan.Candidates))
	}

	// 粘住渠道不在候选池（永久失格）：StickyPinned=false，其余顺序不受影响。
	params.StickyChannelID = 99
	plan, err = executor.PrepareCandidates(context.Background(), params)
	if err != nil {
		t.Fatalf("PrepareCandidates returned error: %v", err)
	}
	if plan.StickyPinned {
		t.Fatal("expected StickyPinned=false when the sticky channel is absent")
	}
}

func TestStickyCooldownExclusionPreservesBindingAcrossBypassSuccess(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)
	first := router.Resolve(context.Background(), stickyResolveParams("sess-cooldown"))
	first.BindSuccess(context.Background(), stickyCandidate(101, nil, nil))

	second := router.Resolve(context.Background(), stickyResolveParams("sess-cooldown"))
	executor := NewExecutor(candidateCapabilityRegistry{allowed: map[int64]bool{101: true, 202: true}})
	ctx := requestadmission.ContextWithUsageSession(context.Background(), &candidateSnapshotSession{
		result: breakerstore.SnapshotManyResult{Candidates: []breakerstore.CandidateSnapshot{
			{Status: breakerstore.CandidateSnapshotRateLimited, CooldownRemainingMs: 4_296},
			{Status: breakerstore.CandidateSnapshotCurrent},
		}},
	})
	plan, err := executor.PrepareCandidates(ctx, PrepareCandidatesParams{
		Protocol: routing.ProtocolOpenAI,
		Candidates: []routing.ChatRouteCandidate{
			candidateRoute(101, "bound"),
			candidateRoute(202, "bypass"),
		},
		EstimateInputTokens: func(context.Context, routing.ChatRouteCandidate) (int64, error) {
			return 1, nil
		},
		StickyChannelID: second.BoundChannelID(),
	})
	if err != nil {
		t.Fatalf("PrepareCandidates returned error: %v", err)
	}
	if plan.StickyPinned || len(plan.Candidates) != 1 || plan.Candidates[0].Route.Channel.ID != 202 {
		t.Fatalf("cooldown plan must bypass bound channel 101: %+v", plan)
	}

	second.ApplyPlanOutcome(ctx, plan)
	if len(store.clearCalls) != 0 || second.BoundChannelID() != 101 {
		t.Fatalf("cooldown must preserve the original binding: clears=%+v bound=%d",
			store.clearCalls, second.BoundChannelID())
	}
	if audit := second.Audit(); audit.Action != StickyActionPreserveOnTemporaryBypass ||
		audit.Reason != "cooldown" || audit.BeforeChannelID != 101 || audit.AfterChannelID != 101 {
		t.Fatalf("unexpected cooldown audit: %+v", audit)
	}

	second.BindSuccess(ctx, plan.Candidates[0].Route)
	if len(store.bindCalls) != 1 || len(store.refreshCalls) != 0 || second.BoundChannelID() != 101 {
		t.Fatalf("bypass success must not rebind or refresh: binds=%+v refreshes=%+v bound=%d",
			store.bindCalls, store.refreshCalls, second.BoundChannelID())
	}
	if audit := second.Audit(); audit.Action != StickyActionPreserveOnTemporaryBypass ||
		audit.Reason != "temporary_bypass_success_on_other_channel" || audit.AfterChannelID != 101 {
		t.Fatalf("unexpected bypass success audit: %+v", audit)
	}
}

// TestApplyPlanOutcomeClearsWhenPinLost 冻结绑定渠道因非临时原因失格时清绑定。
func TestApplyPlanOutcomeClearsWhenPinLost(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)
	session := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	session.BindSuccess(context.Background(), stickyCandidate(101, nil, nil))

	second := router.Resolve(context.Background(), stickyResolveParams("sess-abc"))
	second.ApplyPlanOutcome(context.Background(), CandidatePlan{
		StickyPinned: false,
		Excluded: []CandidateExclusion{{
			ChannelID: 101,
			Reason:    string(breakerstore.CandidateSnapshotOpen),
		}},
	})
	if len(store.clearCalls) != 1 || second.BoundChannelID() != 0 {
		t.Fatalf("pin_lost must clear the binding: clears=%+v bound=%d",
			store.clearCalls, second.BoundChannelID())
	}
	if reason := second.Audit().Reason; reason != "bound_channel_not_eligible" {
		t.Fatalf("pin_lost reason = %q", reason)
	}
}
