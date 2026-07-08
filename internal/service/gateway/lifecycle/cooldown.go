package lifecycle

import (
	"sync"
	"time"

	"github.com/ThankCat/unio-api/internal/core/adapter"
)

// ChannelCooldownRegistry 记录因上游 429 而进入限时冷却的渠道（P2-7）。
//
// 与熔断器（按错误率统计、固定 OpenDuration）正交：本注册表对「单次 429 + Retry-After」
// 即时生效，让 routing fallback 在冷却窗口内直接跳过该渠道（plan 阶段与 attempt 阶段都跳），
// 冷却到期自动恢复。计数全在进程内存（与熔断器一致），多实例各自统计可接受。
//
// defaultCooldown/cap 可运行时热改（SetCooldown），故与 until 一并由 mu 保护。
type ChannelCooldownRegistry struct {
	now func() time.Time

	mu              sync.Mutex
	defaultCooldown time.Duration
	cap             time.Duration
	until           map[string]time.Time
}

// NewChannelCooldownRegistry 创建渠道冷却注册表。
//
// defaultCooldown 是上游 429 但未给 Retry-After 时套用的冷却时长（<=0 表示此情形不冷却）；
// cap 是对 Retry-After 建议值的上限保护（<=0 表示不额外封顶，仅受 adapter 解析阶段硬上限约束）。
func NewChannelCooldownRegistry(defaultCooldown, cap time.Duration) *ChannelCooldownRegistry {
	return &ChannelCooldownRegistry{
		now:             time.Now,
		defaultCooldown: defaultCooldown,
		cap:             cap,
		until:           make(map[string]time.Time),
	}
}

// SetCooldown 原子替换默认冷却与封顶（运行时热改入口）。
// 只影响之后的 RecordRateLimit 计算；已登记的在途冷却条目不受影响，到期自然恢复。
func (r *ChannelCooldownRegistry) SetCooldown(defaultCooldown, cap time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.defaultCooldown = defaultCooldown
	r.cap = cap
	r.mu.Unlock()
}

// Allowed 报告渠道当前是否不在冷却窗口内（true=可用）。
func (r *ChannelCooldownRegistry) Allowed(channelKey string) bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	until, ok := r.until[channelKey]
	if !ok {
		return true
	}
	if !r.now().Before(until) {
		delete(r.until, channelKey)
		return true
	}
	return false
}

// Until 返回渠道冷却到期时间（用于观测）；无冷却或已到期返回 (zero, false)。
func (r *ChannelCooldownRegistry) Until(channelKey string) (time.Time, bool) {
	if r == nil {
		return time.Time{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	until, ok := r.until[channelKey]
	if !ok || !r.now().Before(until) {
		return time.Time{}, false
	}
	return until, true
}

// RecordRateLimit 在上游返回 429 时登记渠道冷却。
//
// retryAfter>0 时按其登记（受 cap 约束）；retryAfter<=0 时回退 defaultCooldown。
// 计算出的冷却时长 <=0 表示不冷却（直接返回）。返回实际生效的冷却到期时间与是否生效。
func (r *ChannelCooldownRegistry) RecordRateLimit(channelKey string, retryAfter time.Duration) (time.Time, bool) {
	if r == nil {
		return time.Time{}, false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	cooldown := retryAfter
	if cooldown <= 0 {
		cooldown = r.defaultCooldown
	}
	if r.cap > 0 && cooldown > r.cap {
		cooldown = r.cap
	}
	if cooldown <= 0 {
		return time.Time{}, false
	}

	until := r.now().Add(cooldown)
	// 取较晚到期时间，避免并发下用较短的 Retry-After 缩短已登记的冷却。
	if existing, ok := r.until[channelKey]; !ok || until.After(existing) {
		r.until[channelKey] = until
	} else {
		until = existing
	}
	return until, true
}

// channelRateLimitRetryAfter 从上游错误链中提取 429 的 Retry-After 建议（P2-7）。
// 仅在 rate_limit 分类时返回 (duration, true)；其它分类或无建议返回 (0, false)。
func channelRateLimitRetryAfter(err error) (time.Duration, bool) {
	category, ok := adapter.UpstreamCategoryOf(err)
	if !ok || category != adapter.UpstreamErrorRateLimit {
		return 0, false
	}
	meta, ok := adapter.UpstreamMetadataOf(err)
	if !ok {
		return 0, false
	}
	return meta.RetryAfter, true
}
