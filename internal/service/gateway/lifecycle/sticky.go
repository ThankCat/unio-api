package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// 会话粘性路由（sticky routing，大 uncache 缺口 P0）。
//
// 问题：多轮对话每请求按 balanced 重排候选，同会话请求漂移到不同上游渠道导致
// prompt cache 断裂，本应 cache_read 计价的上下文变成大量 uncached_input，客户话费暴涨。
// 方案：协议提取器产出 sessionKey（OpenAI prompt_cache_key / Claude Code 会话头）→
// 协议无关核心以 (protocol, route, api_key, session) 为键在 Redis 记住上次成功渠道 →
// PrepareCandidates 把该渠道置顶（绝对优先于 mode 排序与失败软冷却 demote，R5）→
// attempt 成功后 bind/改绑（SETNX 防首轮并发竞态，R8）。
//
// 明确边界：粘住 ≠ 保证 cache hit（决议 8）；sticky 解决「无谓换道」，不解决便宜渠道容量不足（R1）。
// StickyRouter 同时承载队首短等全局配置（tpm_wait_ms，P1），供 AttemptRunner 热读。

// StickyStore 定义 sticky 核心依赖的绑定存取能力（Redis 实现在 platform/stickysession）。
// 实现必须 fail-open：Lookup 失败返回 ok=false，Bind/Rebind/Clear 失败静默（只记日志，R7）。
type StickyStore interface {
	Lookup(ctx context.Context, key string) (channelID int64, ok bool)
	Bind(ctx context.Context, key string, channelID int64, ttl time.Duration)
	Rebind(ctx context.Context, key string, channelID int64, ttl time.Duration)
	Clear(ctx context.Context, key string)
}

// StickyEventRecorder 记录会话粘性路由事件（hit/miss/bind/rebind/clear/pinned_* /pin_lost）。
// nil 表示不采集；实现由 platform/observability/metrics 提供。
type StickyEventRecorder interface {
	IncStickyEvent(event string)
}

// StickyRouter 是跨协议 sticky 核心：解析一次请求的粘性上下文并提供绑定读写。
//
// enabledDefault / ttl / headWait 由 settings applier 周期推送（app_settings gateway.routing_sticky 热更新），
// 用 atomic 存储供每请求无锁读取。nil *StickyRouter 与未启用线路均安全退化为「不粘」。
type StickyRouter struct {
	store   StickyStore
	metrics StickyEventRecorder
	logger  *zap.Logger

	enabledDefault atomic.Bool
	ttlNanos       atomic.Int64
	waitNanos      atomic.Int64
	jitterNanos    atomic.Int64
}

// NewStickyRouter 创建 sticky 核心，初始配置取 appsettings 默认（enabled=true、TTL 60min、
// 队首短等 500ms+100ms 抖动），随后由 settings applier 以真实系统设置覆盖。
func NewStickyRouter(store StickyStore) *StickyRouter {
	if store == nil {
		panic("lifecycle: sticky router requires sticky store")
	}
	r := &StickyRouter{store: store}
	r.SetConfig(true, time.Hour, 500*time.Millisecond, 100*time.Millisecond)
	return r
}

// SetMetrics 注入粘性事件指标采集器；nil 表示不采集。
func (r *StickyRouter) SetMetrics(m StickyEventRecorder) {
	if r == nil {
		return
	}
	r.metrics = m
}

// SetConfig 原子替换全局默认开关、绑定 TTL 与队首短等（settings applier 热更新入口）。
func (r *StickyRouter) SetConfig(enabledDefault bool, ttl, wait, jitter time.Duration) {
	if ttl <= 0 {
		ttl = time.Hour
	}
	if wait < 0 {
		wait = 0
	}
	if jitter < 0 {
		jitter = 0
	}
	r.enabledDefault.Store(enabledDefault)
	r.ttlNanos.Store(int64(ttl))
	r.waitNanos.Store(int64(wait))
	r.jitterNanos.Store(int64(jitter))
}

func (r *StickyRouter) ttl() time.Duration {
	return time.Duration(r.ttlNanos.Load())
}

// SampleHeadWait 采样一次队首短等预算：tpm_wait_ms + [0, jitter]（R10）。
// wait=0 表示关闭短等；router 为 nil 时返回 0。
func (r *StickyRouter) SampleHeadWait() time.Duration {
	if r == nil {
		return 0
	}
	wait := time.Duration(r.waitNanos.Load())
	if wait <= 0 {
		return 0
	}
	jitter := time.Duration(r.jitterNanos.Load())
	if jitter > 0 {
		wait += time.Duration(rand.Int64N(int64(jitter) + 1))
	}
	return wait
}

func (r *StickyRouter) inc(event string) {
	if r == nil || r.metrics == nil || event == "" {
		return
	}
	r.metrics.IncStickyEvent(event)
}

// StickyResolveParams 是解析一次请求粘性上下文所需的事实。
type StickyResolveParams struct {
	// Protocol 是 ingress 协议族（routing.ProtocolOpenAI / ProtocolAnthropic），进入 Redis 键。
	Protocol string
	// RouteID 是本次请求命中的线路 ID；nil 时不粘（线路必填，理论恒有值）。
	RouteID *int64
	// APIKeyID 进入 Redis 键：不同客户 Key 即使会话键碰撞也互不影响（决议 6）。
	APIKeyID int64
	// SessionKey 是协议提取器产出的原始会话键；空串表示本请求无会话信号，不粘（决议 7）。
	SessionKey string
	// RouteStickyEnabled 是线路行 sticky_enabled 覆盖：nil=继承全局默认（决议 1/F）。
	RouteStickyEnabled *bool
}

// Resolve 解析一次请求的粘性上下文：判定开关、构造 Redis 键并 lookup 既有绑定。
// 返回值恒非 nil 指针语义安全（router 为 nil 时返回 nil，*StickySession 方法均 nil-safe）。
func (r *StickyRouter) Resolve(ctx context.Context, params StickyResolveParams) *StickySession {
	if r == nil {
		return nil
	}
	enabled := r.enabledDefault.Load()
	if params.RouteStickyEnabled != nil {
		enabled = *params.RouteStickyEnabled
	}
	if !enabled || params.SessionKey == "" || params.RouteID == nil || params.Protocol == "" {
		return &StickySession{}
	}

	session := &StickySession{
		router: r,
		key:    stickyRedisKey(params.Protocol, *params.RouteID, params.APIKeyID, params.SessionKey),
	}
	session.boundChannelID, _ = r.store.Lookup(ctx, session.key)
	session.resolvedChannelID = session.boundChannelID
	if session.boundChannelID != 0 {
		r.inc("hit")
		r.logSticky(ctx, "sticky hit",
			zap.Int64("sticky_channel_id", session.boundChannelID),
			zap.String("sticky_key", session.key),
		)
	} else {
		r.inc("miss")
		r.logSticky(ctx, "sticky miss",
			zap.String("sticky_key", session.key),
		)
	}
	return session
}

// StickySession 是一次请求的粘性上下文（Resolve 产物）。
// 零值/nil 均表示「本请求不粘」，所有方法 nil-safe，调用方无需判空。
type StickySession struct {
	router         *StickyRouter
	key            string
	boundChannelID int64
	// resolvedChannelID stays immutable so clearing or rebinding cannot erase trace facts.
	resolvedChannelID int64
}

// Enabled 报告本请求是否启用 sticky（有会话键且线路/全局开关打开）。
func (s *StickySession) Enabled() bool {
	return s != nil && s.key != ""
}

// BoundChannelID 返回 lookup 命中的既有绑定渠道 ID；0 表示 miss 或未启用。
func (s *StickySession) BoundChannelID() int64 {
	if s == nil {
		return 0
	}
	return s.boundChannelID
}

// ResolvedChannelID returns the binding observed at the start of this request.
func (s *StickySession) ResolvedChannelID() int64 {
	if s == nil {
		return 0
	}
	return s.resolvedChannelID
}

// ApplyPlanOutcome 消费 PrepareCandidates 置顶结果：记录 pinned_* / pin_lost 指标，
// 粘住渠道被硬摘除时清绑定重选（R5）。协议 service 在 prepare 后统一调用，替代手写 ClearBinding。
func (s *StickySession) ApplyPlanOutcome(ctx context.Context, plan CandidatePlan) {
	if !s.Enabled() || s.boundChannelID == 0 {
		return
	}
	if !plan.StickyPinned {
		s.router.inc("pin_lost")
		s.router.logSticky(ctx, "sticky pin_lost",
			zap.Int64("sticky_channel_id", s.boundChannelID),
			zap.String("sticky_key", s.key),
		)
		s.ClearBinding(ctx)
		return
	}
	if plan.StickyPinnedNonPreferred {
		s.router.inc("pinned_non_preferred")
		s.router.logSticky(ctx, "sticky pinned_non_preferred",
			zap.Int64("sticky_channel_id", s.boundChannelID),
			zap.String("sticky_key", s.key),
		)
	} else {
		s.router.inc("pinned_preferred")
	}
}

// BindSuccess 在 attempt 成功后登记绑定（决议 2/3 + R8）：
//   - 无既有绑定 → SETNX 写入（首轮并发竞态只有第一个成功者生效）；
//   - 有绑定且胜出渠道不同（failover 成功）→ 覆盖改绑；
//   - 胜出渠道与绑定一致 → 不动（绝对 TTL 不刷新，R2：到期自然回落 mode 排序回迁便宜渠道）。
func (s *StickySession) BindSuccess(ctx context.Context, channelID int64) {
	if !s.Enabled() || channelID <= 0 {
		return
	}
	switch {
	case s.boundChannelID == 0:
		s.router.store.Bind(ctx, s.key, channelID, s.router.ttl())
		s.router.inc("bind")
		s.boundChannelID = channelID
	case s.boundChannelID != channelID:
		from := s.boundChannelID
		s.router.store.Rebind(ctx, s.key, channelID, s.router.ttl())
		s.router.inc("rebind")
		s.router.logSticky(ctx, "sticky failover rebind",
			zap.Int64("from_channel_id", from),
			zap.Int64("to_channel_id", channelID),
			zap.String("sticky_key", s.key),
		)
		s.boundChannelID = channelID
	}
}

// ClearBinding 清除既有绑定（粘住渠道被硬摘除：不在候选池 / credential invalid / breaker open）。
// 无绑定时 no-op。软冷却/限流跳过不清绑定（短时状态，R5）。
func (s *StickySession) ClearBinding(ctx context.Context) {
	if !s.Enabled() || s.boundChannelID == 0 {
		return
	}
	s.router.store.Clear(ctx, s.key)
	s.router.inc("clear")
	s.boundChannelID = 0
}

// ClearIfBound 仅当 channelID 恰为当前绑定渠道时清除绑定（attempt 循环内熔断跳过时调用）。
func (s *StickySession) ClearIfBound(ctx context.Context, channelID int64) {
	if !s.Enabled() || s.boundChannelID == 0 || s.boundChannelID != channelID {
		return
	}
	s.ClearBinding(ctx)
}

// stickyRedisKey 构造绑定键：sticky:{protocol}:{route_id}:{api_key_id}:{session_key_hash}。
// sessionKey 是客户端可控任意串，入键前定长哈希（R6）：防长度/基数膨胀与键注入；原值仍原样转发上游。
func stickyRedisKey(protocol string, routeID, apiKeyID int64, sessionKey string) string {
	return fmt.Sprintf("sticky:%s:%d:%d:%s", protocol, routeID, apiKeyID, hashStickySessionKey(sessionKey))
}

// hashStickySessionKey 把任意会话键归一为 32 hex 字符（SHA-256 前 16 字节）。
func hashStickySessionKey(sessionKey string) string {
	sum := sha256.Sum256([]byte(sessionKey))
	return hex.EncodeToString(sum[:16])
}
