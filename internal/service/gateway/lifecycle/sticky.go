package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/stickysession"
	"go.uber.org/zap"
)

// 会话粘性路由（sticky routing）。
//
// 问题：多轮对话每请求按评分重排候选，同会话请求漂移到不同上游渠道导致 prompt cache 断裂，
// 本应 cache_read 计价的上下文变成大量 uncached_input，客户话费暴涨。
// 方案：协议提取器产出 sessionKey（OpenAI prompt_cache_key / Claude Code 会话头）→
// 协议无关核心以 (protocol, route, api_key, model, session) 为键在 Redis 记住上次成功渠道 →
// PrepareCandidates 把该渠道置顶 → 完整成功后 CAS 建绑/续期。
//
// 状态只有 Unbound 与 Bound(channel_id, binding_version)（§10.3）：不引入「候选绑定」
// 「迟滞中」「待换绑」等中间业务状态。写操作只有 BindIfAbsent / RefreshIfCurrent /
// ClearIfCurrent 三种 CAS（§10.4），没有直接 Rebind——改绑必须表达为「先清 A 再绑 B」，
// 于是审计能分别解释「因何清 A」和「因何建 B」（§10.9）。
//
// 明确边界：粘住 ≠ 保证 cache hit；sticky 解决「无谓换道」，不解决便宜渠道容量不足。

// StickyAction 是冻结的 sticky 审计动作枚举（§10.12/§19.3）。
type StickyAction string

const (
	// StickyActionDisabled 本请求未启用 sticky（无会话信号 / fixed 线路 / 开关关闭）。
	StickyActionDisabled StickyAction = "disabled"
	// StickyActionMiss 启用但当前无绑定。
	StickyActionMiss StickyAction = "miss"
	// StickyActionHit 启用且读到有效绑定。
	StickyActionHit StickyAction = "hit"
	// StickyActionBindIfAbsent 以 Unbound 状态新建绑定。
	StickyActionBindIfAbsent StickyAction = "bind_if_absent"
	// StickyActionRefreshIfCurrent 原绑定完整成功后滑动续期。
	StickyActionRefreshIfCurrent StickyAction = "refresh_if_current"
	// StickyActionClearIfCurrent 已确认永久故障，CAS 清除原绑定。
	StickyActionClearIfCurrent StickyAction = "clear_if_current"
	// StickyActionPreserveOnTemporaryBypass 并发满 / 429 / 等待耗尽导致本次绕行，保留原绑定且不续期（§10.6）。
	StickyActionPreserveOnTemporaryBypass StickyAction = "preserve_on_temporary_bypass"
	// StickyActionCASConflict CAS 失败：绑定已被其他请求改变，本请求不得覆盖。
	StickyActionCASConflict StickyAction = "cas_conflict"
	// StickyActionStoreUnavailable sticky 存储故障（fail-open，只影响粘性不影响路由）。
	StickyActionStoreUnavailable StickyAction = "store_unavailable"
)

// StickyStore 定义 sticky 核心依赖的绑定存取能力（Redis 实现在 platform/stickysession）。
// 实现必须 fail-open：读失败当 miss、写失败只报告 StoreUnavailable（§10.11）。
type StickyStore interface {
	Lookup(ctx context.Context, key string) stickysession.LookupResult
	BindIfAbsent(ctx context.Context, key string, channelID int64, ttl time.Duration) (stickysession.Binding, stickysession.CASResult)
	RefreshIfCurrent(ctx context.Context, key string, channelID, bindingVersion int64, ttl time.Duration) (stickysession.Binding, stickysession.CASResult)
	ClearIfCurrent(ctx context.Context, key string, channelID, bindingVersion int64) stickysession.CASResult
}

// StickyEventRecorder 记录会话粘性路由事件（动作枚举 + pinned_*/pin_lost）。
// nil 表示不采集；实现由 platform/observability/metrics 提供。
type StickyEventRecorder interface {
	IncStickyEvent(event string)
}

// StickyRouter 是跨协议 sticky 核心：解析一次请求的粘性上下文并提供绑定读写。
//
// enabledDefault / ttl 由 settings applier 周期推送（app_settings gateway.routing_sticky 热更新），
// 用 atomic 存储供每请求无锁读取。nil *StickyRouter 与未启用线路均安全退化为「不粘」。
type StickyRouter struct {
	store   StickyStore
	metrics StickyEventRecorder
	logger  *zap.Logger

	enabledDefault atomic.Bool
	ttlNanos       atomic.Int64
}

// NewStickyRouter 创建 sticky 核心，初始配置取 appsettings 默认（enabled=true、TTL 30min），
// 随后由 settings applier 以真实系统设置覆盖。
func NewStickyRouter(store StickyStore) *StickyRouter {
	if store == nil {
		panic("lifecycle: sticky router requires sticky store")
	}
	r := &StickyRouter{store: store}
	r.SetConfig(true, 30*time.Minute)
	return r
}

// SetMetrics 注入粘性事件指标采集器；nil 表示不采集。
func (r *StickyRouter) SetMetrics(m StickyEventRecorder) {
	if r == nil {
		return
	}
	r.metrics = m
}

// SetConfig 原子替换全局默认开关与绑定 TTL（settings applier 热更新入口）。
// 容量等待不再属于 sticky：它是全池共享的一次有界短等（§9.4，gateway.capacity_wait_timeout_ms）。
func (r *StickyRouter) SetConfig(enabledDefault bool, ttl time.Duration) {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	r.enabledDefault.Store(enabledDefault)
	r.ttlNanos.Store(int64(ttl))
}

func (r *StickyRouter) ttl() time.Duration {
	return time.Duration(r.ttlNanos.Load())
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
	// APIKeyID 进入 Redis 键：不同客户 Key 即使会话键碰撞也互不影响。
	APIKeyID int64
	// ModelID 进入 Redis 键（§10.1）：同一会话跨模型不得共享绑定，否则换模型会继承
	// 与新模型无关的渠道选择，而 prompt cache 本身就是按模型隔离的。
	ModelID int64
	// SessionKey 是协议提取器产出的原始会话键；空串表示本请求无会话信号，不粘。
	SessionKey string
	// Candidates are the hard-filtered channel facts. Sticky policy is resolved from the bound Channel,
	// never from Route. Fixed routes skip Sticky because they already have one channel.
	Candidates []routing.ChatRouteCandidate
	Mode       string
}

// Resolve 解析一次请求的粘性上下文：判定开关、构造 Redis 键并 lookup 既有绑定。
// 返回值恒非 nil 指针语义安全（router 为 nil 时返回 nil，*StickySession 方法均 nil-safe）。
func (r *StickyRouter) Resolve(ctx context.Context, params StickyResolveParams) *StickySession {
	if r == nil {
		return nil
	}
	if params.SessionKey == "" || params.RouteID == nil || params.Protocol == "" ||
		params.ModelID <= 0 || params.Mode == "fixed" {
		return &StickySession{action: StickyActionDisabled}
	}

	session := &StickySession{
		router: r,
		key: stickyRedisKey(
			params.Protocol, *params.RouteID, params.APIKeyID, params.ModelID, params.SessionKey,
		),
	}
	result := r.store.Lookup(ctx, session.key)
	if result.StoreUnavailable {
		session.action = StickyActionStoreUnavailable
		r.inc(string(StickyActionStoreUnavailable))
		return session
	}
	if !result.Found {
		session.action = StickyActionMiss
		r.inc(string(StickyActionMiss))
		r.logSticky(ctx, "sticky miss", zap.String("sticky_key", session.key))
		return session
	}

	session.bound = result.Binding
	session.before = result.Binding
	// TTL 到期由 Redis 负责；这里只额外尊重「渠道策略已关闭 sticky」这一永久性变化。
	if candidate, ok := findStickyCandidate(params.Candidates, result.Binding.ChannelID); ok {
		if enabled, _ := r.policy(candidate); !enabled {
			session.clearIfCurrent(ctx, "sticky_disabled_on_channel")
			return session
		}
	}
	session.action = StickyActionHit
	r.inc(string(StickyActionHit))
	r.logSticky(ctx, "sticky hit",
		zap.Int64("sticky_channel_id", result.Binding.ChannelID),
		zap.String("sticky_key", session.key),
	)
	return session
}

func findStickyCandidate(candidates []routing.ChatRouteCandidate, channelID int64) (routing.ChatRouteCandidate, bool) {
	for _, candidate := range candidates {
		if candidate.Channel.ID == channelID {
			return candidate, true
		}
	}
	return routing.ChatRouteCandidate{}, false
}

func (r *StickyRouter) policy(candidate routing.ChatRouteCandidate) (bool, time.Duration) {
	if candidate.StickyEnabled != nil {
		if !*candidate.StickyEnabled || candidate.StickyTTL == nil || *candidate.StickyTTL <= 0 {
			return false, 0
		}
		return true, *candidate.StickyTTL
	}
	return r.enabledDefault.Load(), r.ttl()
}

// StickySession 是一次请求的粘性上下文（Resolve 产物）。
// 零值/nil 均表示「本请求不粘」，所有方法 nil-safe，调用方无需判空。
type StickySession struct {
	router *StickyRouter
	key    string

	// bound 是当前有效绑定（CAS 身份）；ChannelID==0 表示 Unbound。
	bound stickysession.Binding
	// before 冻结请求开始时观测到的绑定，供审计解释「从哪里来」（§10.12）。
	before stickysession.Binding

	action StickyAction
	reason string
}

// Enabled 报告本请求是否启用 sticky（有会话键且线路/全局开关打开）。
func (s *StickySession) Enabled() bool {
	return s != nil && s.key != ""
}

// BoundChannelID 返回当前有效绑定渠道 ID；0 表示 Unbound 或未启用。
func (s *StickySession) BoundChannelID() int64 {
	if s == nil {
		return 0
	}
	return s.bound.ChannelID
}

// ResolvedChannelID returns the binding observed at the start of this request.
func (s *StickySession) ResolvedChannelID() int64 {
	if s == nil {
		return 0
	}
	return s.before.ChannelID
}

// Audit 返回 §10.12 要求的 sticky 审计事实快照。
func (s *StickySession) Audit() StickyAudit {
	if s == nil {
		return StickyAudit{Action: StickyActionDisabled}
	}
	action := s.action
	if action == "" {
		action = StickyActionDisabled
	}
	return StickyAudit{
		KeyPresent:      s.key != "",
		BeforeChannelID: s.before.ChannelID,
		BeforeVersion:   s.before.BindingVersion,
		Action:          action,
		Reason:          s.reason,
		AfterChannelID:  s.bound.ChannelID,
		AfterVersion:    s.bound.BindingVersion,
	}
}

// StickyAudit 是写入 routing trace 的 sticky 决策事实（§10.12）。
type StickyAudit struct {
	KeyPresent      bool
	BeforeChannelID int64
	BeforeVersion   int64
	Action          StickyAction
	Reason          string
	AfterChannelID  int64
	AfterVersion    int64
}

// ApplyPlanOutcome 消费 PrepareCandidates 置顶结果：记录 pinned_* / pin_lost 指标。
//
// 置顶失败（绑定渠道已不在候选池）本身就是「永久失去候选资格」，按 §10.7 清绑定。
func (s *StickySession) ApplyPlanOutcome(ctx context.Context, plan CandidatePlan) {
	if !s.Enabled() || s.bound.ChannelID == 0 {
		return
	}
	if !plan.StickyPinned {
		s.router.inc("pin_lost")
		s.router.logSticky(ctx, "sticky pin_lost",
			zap.Int64("sticky_channel_id", s.bound.ChannelID),
			zap.String("sticky_key", s.key),
		)
		s.clearIfCurrent(ctx, "bound_channel_not_eligible")
		return
	}
	if plan.StickyPinnedNonPreferred {
		s.router.inc("pinned_non_preferred")
		s.router.logSticky(ctx, "sticky pinned_non_preferred",
			zap.Int64("sticky_channel_id", s.bound.ChannelID),
			zap.String("sticky_key", s.key),
		)
	} else {
		s.router.inc("pinned_preferred")
	}
}

// BindSuccess 在一次 attempt 完整成功后推进绑定（§10.5/§10.6）：
//   - Unbound → BindIfAbsent（首轮并发只有第一个 CAS 成功者建绑）；
//   - Bound(A) 且 A 成功 → RefreshIfCurrent(A, version) 滑动续期；
//   - Bound(A) 但成功的是 B → 保留 A、不续期任何一方（临时绕行，§10.6）。
//
// 第三种情况是「不做隐式 Rebind」的直接体现：A 只会在原 TTL 内保留，
// 若持续不可用就自然过期，不需要额外迟滞状态。真正的改绑只能来自
// 先 ClearIfCurrent(A) 之后的 BindIfAbsent(B)（§10.9）。
func (s *StickySession) BindSuccess(ctx context.Context, candidate routing.ChatRouteCandidate) {
	if !s.Enabled() || candidate.Channel.ID <= 0 {
		return
	}
	enabled, ttl := s.router.policy(candidate)
	if !enabled {
		// 成功渠道已关闭 sticky：清掉可能存在的旧绑定，且不为它建新绑定。
		s.clearIfCurrent(ctx, "sticky_disabled_on_channel")
		return
	}
	channelID := candidate.Channel.ID

	switch {
	case s.bound.ChannelID == 0:
		next, result := s.router.store.BindIfAbsent(ctx, s.key, channelID, ttl)
		s.applyWriteResult(result, StickyActionBindIfAbsent, "complete_success", next)
	case s.bound.ChannelID == channelID:
		next, result := s.router.store.RefreshIfCurrent(
			ctx, s.key, channelID, s.bound.BindingVersion, ttl,
		)
		s.applyWriteResult(result, StickyActionRefreshIfCurrent, "complete_success", next)
	default:
		// 绕到了别的渠道并成功：保留原绑定，两边 TTL 都不刷新（§10.6）。
		s.action = StickyActionPreserveOnTemporaryBypass
		s.reason = "temporary_bypass_success_on_other_channel"
		s.router.inc(string(StickyActionPreserveOnTemporaryBypass))
		s.router.logSticky(ctx, "sticky preserve_on_temporary_bypass",
			zap.Int64("sticky_channel_id", s.bound.ChannelID),
			zap.Int64("succeeded_channel_id", channelID),
			zap.String("sticky_key", s.key),
		)
	}
}

// applyWriteResult 把一次 CAS 结果落成审计动作。CAS 冲突与存储故障都不改变本地 bound 状态：
// 前者说明别的请求已经赢了，后者是 fail-open。
func (s *StickySession) applyWriteResult(
	result stickysession.CASResult, action StickyAction, reason string, next stickysession.Binding,
) {
	switch {
	case result.StoreUnavailable:
		s.action = StickyActionStoreUnavailable
		s.reason = reason
		s.router.inc(string(StickyActionStoreUnavailable))
	case result.Conflict:
		s.action = StickyActionCASConflict
		s.reason = reason
		s.router.inc(string(StickyActionCASConflict))
	case result.Applied:
		s.bound = next
		s.action = action
		s.reason = reason
		s.router.inc(string(action))
	}
}

// ClearOnPermanentFailure CAS 清除当前绑定（§10.7：已确认的永久故障 / 失去候选资格）。
// 临时状态（并发满、429、等待耗尽、客户取消、普通 4xx、平台错误）不得调用（§10.8）。
func (s *StickySession) ClearOnPermanentFailure(ctx context.Context, reason string) {
	s.clearIfCurrent(ctx, reason)
}

func (s *StickySession) clearIfCurrent(ctx context.Context, reason string) {
	if !s.Enabled() || s.bound.ChannelID == 0 {
		return
	}
	channelID, version := s.bound.ChannelID, s.bound.BindingVersion
	result := s.router.store.ClearIfCurrent(ctx, s.key, channelID, version)
	switch {
	case result.StoreUnavailable:
		s.action = StickyActionStoreUnavailable
		s.reason = reason
		s.router.inc(string(StickyActionStoreUnavailable))
	case result.Conflict:
		// 绑定已被其他请求改变：不删除新绑定，也不把本地状态当成 Unbound 去建绑。
		s.action = StickyActionCASConflict
		s.reason = reason
		s.router.inc(string(StickyActionCASConflict))
	case result.Applied:
		s.bound = stickysession.Binding{}
		s.action = StickyActionClearIfCurrent
		s.reason = reason
		s.router.inc(string(StickyActionClearIfCurrent))
		s.router.logSticky(ctx, "sticky clear_if_current",
			zap.Int64("sticky_channel_id", channelID),
			zap.String("sticky_reason", reason),
			zap.String("sticky_key", s.key),
		)
	}
}

// ClearIfBound 仅当 channelID 恰为当前绑定渠道时按永久故障清除。
func (s *StickySession) ClearIfBound(ctx context.Context, channelID int64, reason string) {
	if !s.Enabled() || s.bound.ChannelID == 0 || s.bound.ChannelID != channelID {
		return
	}
	s.clearIfCurrent(ctx, reason)
}

// PreserveOnTemporaryBypass 记录一次「原绑定临时不可用、本次绕行」的审计事实（§10.6）：
// 并发满、429 冷却、上游真实 429、全池等待耗尽。绑定与 TTL 都不动。
func (s *StickySession) PreserveOnTemporaryBypass(ctx context.Context, channelID int64, reason string) {
	if !s.Enabled() || s.bound.ChannelID == 0 || s.bound.ChannelID != channelID {
		return
	}
	s.action = StickyActionPreserveOnTemporaryBypass
	s.reason = reason
	s.router.inc(string(StickyActionPreserveOnTemporaryBypass))
	s.router.logSticky(ctx, "sticky preserve_on_temporary_bypass",
		zap.Int64("sticky_channel_id", channelID),
		zap.String("sticky_reason", reason),
		zap.String("sticky_key", s.key),
	)
}

// stickyRedisKey 构造绑定键：sticky:{protocol}:{route_id}:{api_key_id}:{model_id}:{session_hash}。
// sessionKey 是客户端可控任意串，入键前定长哈希：防长度/基数膨胀与键注入，也避免把原始会话
// 标识写进 Redis key 或日志；原值仍原样转发上游。
func stickyRedisKey(protocol string, routeID, apiKeyID, modelID int64, sessionKey string) string {
	return fmt.Sprintf(
		"sticky:%s:%d:%d:%d:%s",
		protocol, routeID, apiKeyID, modelID, hashStickySessionKey(sessionKey),
	)
}

// hashStickySessionKey 把任意会话键归一为 32 hex 字符（SHA-256 前 16 字节）。
func hashStickySessionKey(sessionKey string) string {
	sum := sha256.Sum256([]byte(sessionKey))
	return hex.EncodeToString(sum[:16])
}
