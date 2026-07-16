package lifecycle

import (
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
)

// ChannelBreaker 定义 gateway 在选择 channel 时所需的熔断能力。
//
// 它是协议无关的共享 lifecycle 能力：OpenAI 与 Anthropic 两个协议族的 service 编排都复用
// 同一套 channel 熔断保护。由 ChannelCircuitBreaker 实现；nil 表示不启用熔断（始终放行、不记录）。
type ChannelBreaker interface {
	// Available 只读判断某个 channel 当前是否可能进入 fallback plan。
	// 它不占用 half-open 探测名额；真正尝试前必须继续调用 Allow。
	Available(channelKey string) bool

	// HealthScore 返回某个 channel 的健康分（越小越健康，0 最佳），供 stable 线路排序。
	// 约定：closed 且窗口内有样本 → 近窗口失败率；窗口无样本 → 0；half-open → 偏高；open → 最差(1)。
	HealthScore(channelKey string) float64

	// Allow 判断某个 channel 当前是否允许尝试。
	// open 状态返回 false；到达探测时机或 half-open 时放行一次探测请求。
	Allow(channelKey string) bool

	// RecordSuccess 记录一次成功调用。
	RecordSuccess(channelKey string)

	// RecordFailure 记录一次归因于 channel 的失败调用。
	RecordFailure(channelKey string)
}

// circuitState 表示单个 channel 的熔断状态。
type circuitState int

const (
	// circuitClosed 正常放行并统计错误率。
	circuitClosed circuitState = iota

	// circuitOpen 熔断中，直接拒绝直到冷却时间到达。
	circuitOpen

	// circuitHalfOpen 冷却后允许一次探测，根据结果恢复或重新熔断。
	circuitHalfOpen
)

// ChannelCircuitBreakerConfig 保存熔断器阈值参数。
type ChannelCircuitBreakerConfig struct {
	// Window 是统计错误率的固定时间窗；窗口过期后计数清零。
	Window time.Duration

	// MinRequests 是窗口内触发熔断判定所需的最小样本数。
	MinRequests int

	// FailureRatio 是触发熔断的失败比例阈值（failures/total）。
	FailureRatio float64

	// OpenDuration 是熔断后进入半开探测前的冷却时长。
	OpenDuration time.Duration
}

// channelBreakerState 是单个 channel 的运行时熔断状态。
type channelBreakerState struct {
	state            circuitState
	windowStart      time.Time
	failures         int
	successes        int
	openedAt         time.Time
	halfOpenInFlight bool
}

// ChannelCircuitBreaker 是按 channel 维度的进程内熔断器。
//
// 设计取舍：
//   - 进程内状态，每个 gateway 实例独立保护自己，不依赖共享存储；
//     跨实例共享健康和后台手动恢复属于阶段 13 admin 能力。
//   - 使用固定时间窗统计错误率，窗口过期清零；实现简单、足够保护上游。
//   - half-open 通过 inFlight 保证同一时刻只放行一个探测请求。
//   - 总开关与阈值均可运行时热改（SetEnabled / SetConfig）：实例始终构造，
//     禁用时放行全部且不记状态，因而「运行期启/停熔断」无需重建实例。
type ChannelCircuitBreaker struct {
	now func() time.Time

	// disabled 用反义使零值即「启用」，兼容既有直接构造路径。
	disabled atomic.Bool

	mu    sync.Mutex
	cfg   ChannelCircuitBreakerConfig
	items map[string]*channelBreakerState
}

// normalizeChannelCircuitBreakerConfig 对非法/缺省阈值做保守兜底（构造与热改共用）。
func normalizeChannelCircuitBreakerConfig(cfg ChannelCircuitBreakerConfig) ChannelCircuitBreakerConfig {
	if cfg.Window <= 0 {
		cfg.Window = 30 * time.Second
	}
	if cfg.MinRequests <= 0 {
		cfg.MinRequests = 20
	}
	if cfg.FailureRatio <= 0 || cfg.FailureRatio > 1 {
		cfg.FailureRatio = 0.5
	}
	if cfg.OpenDuration <= 0 {
		cfg.OpenDuration = 30 * time.Second
	}
	return cfg
}

// NewChannelCircuitBreaker 创建熔断器（默认启用），并对非法/缺省阈值做保守兜底。
func NewChannelCircuitBreaker(cfg ChannelCircuitBreakerConfig) *ChannelCircuitBreaker {
	return &ChannelCircuitBreaker{
		cfg:   normalizeChannelCircuitBreakerConfig(cfg),
		now:   time.Now,
		items: make(map[string]*channelBreakerState),
	}
}

// SetConfig 原子替换熔断阈值（运行时热改入口）；非法/缺省字段沿用与构造相同的保守兜底。
// 只替换阈值，不动各 channel 进行中的窗口计数与熔断状态；下次判定即用新阈值。
func (b *ChannelCircuitBreaker) SetConfig(cfg ChannelCircuitBreakerConfig) {
	cfg = normalizeChannelCircuitBreakerConfig(cfg)
	b.mu.Lock()
	b.cfg = cfg
	b.mu.Unlock()
}

// SetEnabled 热改熔断器总开关。禁用时 Allow/Available 恒放行、HealthScore 恒 0、不记状态，
// 并在「启用→禁用」边沿清空全部熔断状态（重新启用从干净状态起步，避免陈旧窗口误判）。
func (b *ChannelCircuitBreaker) SetEnabled(enabled bool) {
	wasDisabled := b.disabled.Swap(!enabled)
	if !enabled && !wasDisabled {
		b.mu.Lock()
		b.items = make(map[string]*channelBreakerState)
		b.mu.Unlock()
	}
}

// Enabled 报告熔断器当前是否启用（供观测）。
func (b *ChannelCircuitBreaker) Enabled() bool {
	return !b.disabled.Load()
}

// CircuitStateName 是熔断状态机对外暴露的稳定字符串（admin / internal API JSON）。
type CircuitStateName string

const (
	CircuitStateClosed   CircuitStateName = "closed"
	CircuitStateOpen     CircuitStateName = "open"
	CircuitStateHalfOpen CircuitStateName = "half_open"
)

// ChannelBreakerConfigSnapshot 是熔断阈值的只读快照（毫秒口径，便于 JSON）。
type ChannelBreakerConfigSnapshot struct {
	WindowMs       int64   `json:"window_ms"`
	MinRequests    int     `json:"min_requests"`
	FailureRatio   float64 `json:"failure_ratio"`
	OpenDurationMs int64   `json:"open_duration_ms"`
}

// ChannelBreakerEntry 是单个 channel 的只读熔断状态（不推进状态机）。
type ChannelBreakerEntry struct {
	ChannelID        int64            `json:"channel_id"`
	State            CircuitStateName `json:"state"`
	Failures         int              `json:"failures"`
	Successes        int              `json:"successes"`
	WindowStart      time.Time        `json:"window_start"`
	OpenedAt         *time.Time       `json:"opened_at,omitempty"`
	OpenRemainingMs  *int64           `json:"open_remaining_ms,omitempty"`
	HalfOpenInFlight bool             `json:"half_open_in_flight"`
	HealthScore      float64          `json:"health_score"`
}

// ChannelBreakerSnapshot 是进程内熔断器的只读全量快照。
type ChannelBreakerSnapshot struct {
	Enabled    bool                         `json:"enabled"`
	Instance   string                       `json:"instance,omitempty"`
	ObservedAt time.Time                    `json:"observed_at"`
	Config     ChannelBreakerConfigSnapshot `json:"config"`
	Channels   []ChannelBreakerEntry        `json:"channels"`
}

// Snapshot 返回当前已跟踪 channel 的只读状态，不创建缺失 key、不推进 open→half-open。
// Instance 由调用方填入（hostname / GATEWAY_INSTANCE_ID）；本方法留空。
func (b *ChannelCircuitBreaker) Snapshot() ChannelBreakerSnapshot {
	if b == nil {
		return ChannelBreakerSnapshot{
			Enabled:    false,
			ObservedAt: time.Now().UTC(),
			Channels:   []ChannelBreakerEntry{},
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	out := ChannelBreakerSnapshot{
		Enabled:    !b.disabled.Load(),
		ObservedAt: now.UTC(),
		Config: ChannelBreakerConfigSnapshot{
			WindowMs:       b.cfg.Window.Milliseconds(),
			MinRequests:    b.cfg.MinRequests,
			FailureRatio:   b.cfg.FailureRatio,
			OpenDurationMs: b.cfg.OpenDuration.Milliseconds(),
		},
		Channels: make([]ChannelBreakerEntry, 0, len(b.items)),
	}
	if !out.Enabled {
		return out
	}

	for key, s := range b.items {
		channelID, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			continue
		}
		entry := ChannelBreakerEntry{
			ChannelID:        channelID,
			Failures:         s.failures,
			Successes:        s.successes,
			WindowStart:      s.windowStart.UTC(),
			HalfOpenInFlight: s.halfOpenInFlight,
		}
		switch s.state {
		case circuitOpen:
			entry.State = CircuitStateOpen
			opened := s.openedAt.UTC()
			entry.OpenedAt = &opened
			remaining := b.cfg.OpenDuration - now.Sub(s.openedAt)
			if remaining < 0 {
				remaining = 0
			}
			ms := remaining.Milliseconds()
			entry.OpenRemainingMs = &ms
			entry.HealthScore = 1
		case circuitHalfOpen:
			entry.State = CircuitStateHalfOpen
			entry.HealthScore = 0.75
		default:
			entry.State = CircuitStateClosed
			if now.Sub(s.windowStart) >= b.cfg.Window {
				entry.Failures = 0
				entry.Successes = 0
				entry.HealthScore = 0
			} else {
				total := s.failures + s.successes
				if total > 0 {
					entry.HealthScore = float64(s.failures) / float64(total)
				}
			}
		}
		out.Channels = append(out.Channels, entry)
	}
	return out
}

// Available 只读判断 channel 是否可进入 fallback plan，不推进熔断状态。
func (b *ChannelCircuitBreaker) Available(channelKey string) bool {
	if b.disabled.Load() {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	s := b.stateForLocked(channelKey)
	switch s.state {
	case circuitOpen:
		return b.now().Sub(s.openedAt) >= b.cfg.OpenDuration
	case circuitHalfOpen:
		return !s.halfOpenInFlight
	default:
		return true
	}
}

// HealthScore 实现 ChannelBreaker：只读返回近窗口失败率（越小越健康），不推进熔断状态。
func (b *ChannelCircuitBreaker) HealthScore(channelKey string) float64 {
	if b.disabled.Load() {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	s := b.stateForLocked(channelKey)
	switch s.state {
	case circuitOpen:
		return 1
	case circuitHalfOpen:
		// 半开恢复中，比健康闭合差、比熔断好，stable 排序时排在健康渠道之后。
		return 0.75
	default:
		// 窗口已过期视为无近况样本（最优）；避免在只读路径里清零计数。
		if b.now().Sub(s.windowStart) >= b.cfg.Window {
			return 0
		}
		total := s.failures + s.successes
		if total == 0 {
			return 0
		}
		return float64(s.failures) / float64(total)
	}
}

// Allow 实现 ChannelBreaker。
func (b *ChannelCircuitBreaker) Allow(channelKey string) bool {
	if b.disabled.Load() {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	s := b.stateForLocked(channelKey)
	switch s.state {
	case circuitOpen:
		if b.now().Sub(s.openedAt) < b.cfg.OpenDuration {
			return false
		}
		// 冷却结束，进入半开并放行一次探测。
		s.state = circuitHalfOpen
		s.halfOpenInFlight = true
		return true

	case circuitHalfOpen:
		if s.halfOpenInFlight {
			return false
		}
		s.halfOpenInFlight = true
		return true

	default:
		return true
	}
}

// RecordSuccess 实现 ChannelBreaker。
func (b *ChannelCircuitBreaker) RecordSuccess(channelKey string) {
	if b.disabled.Load() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	s := b.stateForLocked(channelKey)
	switch s.state {
	case circuitHalfOpen:
		// 探测成功，恢复闭合。
		b.closeLocked(s)
	case circuitClosed:
		b.rollLocked(s)
		s.successes++
	}
}

// RecordFailure 实现 ChannelBreaker。
func (b *ChannelCircuitBreaker) RecordFailure(channelKey string) {
	if b.disabled.Load() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	s := b.stateForLocked(channelKey)
	switch s.state {
	case circuitHalfOpen:
		// 探测失败，重新熔断。
		b.openLocked(s)
	case circuitClosed:
		b.rollLocked(s)
		s.failures++

		total := s.failures + s.successes
		if total >= b.cfg.MinRequests && float64(s.failures)/float64(total) >= b.cfg.FailureRatio {
			b.openLocked(s)
		}
	}
}

// stateForLocked 返回（必要时创建）某个 channel 的熔断状态。调用方必须持锁。
func (b *ChannelCircuitBreaker) stateForLocked(channelKey string) *channelBreakerState {
	s, ok := b.items[channelKey]
	if !ok {
		s = &channelBreakerState{state: circuitClosed, windowStart: b.now()}
		b.items[channelKey] = s
	}

	return s
}

// rollLocked 在固定窗口过期时清零计数。调用方必须持锁。
func (b *ChannelCircuitBreaker) rollLocked(s *channelBreakerState) {
	if b.now().Sub(s.windowStart) >= b.cfg.Window {
		s.windowStart = b.now()
		s.failures = 0
		s.successes = 0
	}
}

// openLocked 切换到熔断状态并重置计数。调用方必须持锁。
func (b *ChannelCircuitBreaker) openLocked(s *channelBreakerState) {
	s.state = circuitOpen
	s.openedAt = b.now()
	s.failures = 0
	s.successes = 0
	s.halfOpenInFlight = false
}

// closeLocked 切换到闭合状态并重置计数。调用方必须持锁。
func (b *ChannelCircuitBreaker) closeLocked(s *channelBreakerState) {
	s.state = circuitClosed
	s.windowStart = b.now()
	s.failures = 0
	s.successes = 0
	s.halfOpenInFlight = false
}

// IsChannelFaultError 判断一个上游错误是否应计入进程内熔断（瞬时故障保护）。
//
//   - timeout / server_error / rate_limit：上游瞬时故障，应计入熔断。
//   - permission（403）：账号/模型级授权问题，持续出现应摘除该 channel；不进凭据闸门（不自动 ban），
//     故保留在此由熔断做瞬时摘除。
//   - auth（401）：凭据失效，改由持久「凭据闸门」（credential_valid + 连续 401 计数）专管，
//     不再喂进程内熔断，避免两套摘除机制重叠（DEC 2026-07 凭据闸门 C-4）。
//   - bad_request：请求本身问题，channel 正常拒绝，不计故障。
//   - canceled / 无上游分类：非 channel 责任，不计故障。
func IsChannelFaultError(err error) bool {
	if err == nil {
		return false
	}

	category, ok := adapter.UpstreamCategoryOf(err)
	if !ok {
		return false
	}

	switch category {
	case adapter.UpstreamErrorTimeout,
		adapter.UpstreamErrorServer,
		adapter.UpstreamErrorRateLimit,
		adapter.UpstreamErrorPermission:
		return true
	default:
		return false
	}
}
