package lifecycle

import (
	"sync"
	"time"

	"github.com/ThankCat/unio-api/internal/core/adapter"
)

// ChannelBreaker 定义 gateway 在选择 channel 时所需的熔断能力。
//
// 它是协议无关的共享 lifecycle 能力：OpenAI 与 Anthropic 两个协议族的 service 编排都复用
// 同一套 channel 熔断保护。由 ChannelCircuitBreaker 实现；nil 表示不启用熔断（始终放行、不记录）。
type ChannelBreaker interface {
	// Available 只读判断某个 channel 当前是否可能进入 fallback plan。
	// 它不占用 half-open 探测名额；真正尝试前必须继续调用 Allow。
	Available(channelKey string) bool

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
//     跨实例共享健康和后台手动恢复属于阶段 11 admin 能力。
//   - 使用固定时间窗统计错误率，窗口过期清零；实现简单、足够保护上游。
//   - half-open 通过 inFlight 保证同一时刻只放行一个探测请求。
type ChannelCircuitBreaker struct {
	cfg   ChannelCircuitBreakerConfig
	now   func() time.Time
	mu    sync.Mutex
	items map[string]*channelBreakerState
}

// NewChannelCircuitBreaker 创建熔断器，并对非法/缺省阈值做保守兜底。
func NewChannelCircuitBreaker(cfg ChannelCircuitBreakerConfig) *ChannelCircuitBreaker {
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

	return &ChannelCircuitBreaker{
		cfg:   cfg,
		now:   time.Now,
		items: make(map[string]*channelBreakerState),
	}
}

// Available 只读判断 channel 是否可进入 fallback plan，不推进熔断状态。
func (b *ChannelCircuitBreaker) Available(channelKey string) bool {
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

// Allow 实现 ChannelBreaker。
func (b *ChannelCircuitBreaker) Allow(channelKey string) bool {
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

// IsChannelFaultError 判断一个上游错误是否应归因于 channel 健康度。
//
//   - timeout / server_error / rate_limit：上游瞬时故障，应计入熔断。
//   - auth / permission：多为 channel 凭据/授权问题，持续出现应停用该 channel。
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
		adapter.UpstreamErrorAuth,
		adapter.UpstreamErrorPermission:
		return true
	default:
		return false
	}
}
