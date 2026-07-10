package ratelimit

import (
	"strconv"
	"sync"
)

// ConcurrencyLimiter 是进程内「在途并发」限制器（DEC-029）。
//
// 与 Guard（Redis 滑动窗口，按时间的 RPM/TPM/RPD）正交：本限制器数的是「同时进行中」的
// 请求数（含整段流式传输），专门防「慢上游 + 客户端重试风暴」把长耗时请求堆在同一主体上。
// 采用进程内计数（与熔断器/冷却注册表同取舍）：每个 gateway 实例独立保护自己，
// 多实例部署时按实例分摊；释放由 release 闭包保证，进程崩溃即全部归零，无泄漏残留。
//
// 两级主体：
//   - 线路+用户（ru:<routeID>:<userID>）：上限取全局默认 key_limit（运行时热改）；
//     同一用户在该线路下所有 Key 共享一个在途池（对齐 DEC-027 的限流主体口径）。
//   - 渠道（chan:<channelID>）：上限取渠道行覆盖值（nil=继承全局默认 channel_limit，
//     0=显式不限，>0=具体上限），与 channels.rpm_limit 语义一致。
//
// 上限 <=0 时该主体不限并发，但仍然计数：保证运行期把上限从 0 调成 N 时计数是准的。
type ConcurrencyLimiter struct {
	mu       sync.Mutex
	inflight map[string]int64

	defaultKeyLimit     int64
	defaultChannelLimit int64
}

// NewConcurrencyLimiter 创建在途并发限制器。keyLimit/channelLimit 为全局默认上限（0=不限）。
func NewConcurrencyLimiter(keyLimit, channelLimit int64) *ConcurrencyLimiter {
	return &ConcurrencyLimiter{
		inflight:            make(map[string]int64),
		defaultKeyLimit:     keyLimit,
		defaultChannelLimit: channelLimit,
	}
}

// SetDefaults 原子替换全局默认上限（运行时热改入口）。只影响之后的判定，不动在途计数。
func (l *ConcurrencyLimiter) SetDefaults(keyLimit, channelLimit int64) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.defaultKeyLimit = keyLimit
	l.defaultChannelLimit = channelLimit
	l.mu.Unlock()
}

// AcquireRouteUser 尝试占用「线路+用户」的一个在途名额。
//
// ok=false 表示已达上限（调用方应立即以 429 拒绝，不发起任何上游调用）。
// ok=true 时必须在请求完全结束（含流式传输完成/中断）后调用 release 释放名额；
// release 幂等，重复调用只释放一次。nil receiver 恒放行（未启用并发限制）。
func (l *ConcurrencyLimiter) AcquireRouteUser(routeID, userID int64) (release func(), ok bool) {
	if l == nil {
		return func() {}, true
	}
	subject := routeUserInflightSubject(routeID, userID)
	l.mu.Lock()
	limit := l.defaultKeyLimit
	ok = l.acquireLocked(subject, limit)
	l.mu.Unlock()
	if !ok {
		return nil, false
	}
	return l.releaseFunc(subject), true
}

// AcquireChannel 尝试占用渠道的一个在途名额。
//
// override 是渠道行上的 concurrency_limit：nil=继承全局默认，0=显式不限，>0=具体上限。
// 语义同 AcquireRouteUser；ok=false 时调用方应跳过该候选 fallback 到下一渠道。
func (l *ConcurrencyLimiter) AcquireChannel(channelID int64, override *int64) (release func(), ok bool) {
	if l == nil {
		return func() {}, true
	}
	subject := channelInflightSubject(channelID)
	l.mu.Lock()
	limit := l.defaultChannelLimit
	if override != nil {
		limit = *override
	}
	ok = l.acquireLocked(subject, limit)
	l.mu.Unlock()
	if !ok {
		return nil, false
	}
	return l.releaseFunc(subject), true
}

// Inflight 返回某主体当前在途计数（观测/测试用）。
func (l *ConcurrencyLimiter) Inflight(subject string) int64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inflight[subject]
}

// acquireLocked 在持锁状态下判定并占用。limit<=0 表示不限：仍计数但不拒绝。
func (l *ConcurrencyLimiter) acquireLocked(subject string, limit int64) bool {
	if limit > 0 && l.inflight[subject] >= limit {
		return false
	}
	l.inflight[subject]++
	return true
}

// releaseFunc 构造幂等的释放闭包；计数归零时删除条目，避免 map 无限增长。
func (l *ConcurrencyLimiter) releaseFunc(subject string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			if n := l.inflight[subject]; n <= 1 {
				delete(l.inflight, subject)
			} else {
				l.inflight[subject] = n - 1
			}
			l.mu.Unlock()
		})
	}
}

// RouteUserInflightSubject 构造「线路+用户」在途计数主体（观测/测试用）。
func RouteUserInflightSubject(routeID, userID int64) string {
	return routeUserInflightSubject(routeID, userID)
}

// ChannelInflightSubject 构造渠道在途计数主体（观测/测试用）。
func ChannelInflightSubject(channelID int64) string {
	return channelInflightSubject(channelID)
}

func routeUserInflightSubject(routeID, userID int64) string {
	return string(ScopeRouteUser) + ":" + strconv.FormatInt(routeID, 10) + ":" + strconv.FormatInt(userID, 10) + ":inflight"
}

func channelInflightSubject(channelID int64) string {
	return string(ScopeChannel) + ":" + strconv.FormatInt(channelID, 10) + ":inflight"
}
