package adapter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultStreamIdleTimeout 是流式上游「相邻两次活动之间」最大静默时长的默认值（运行期未配置时的兜底）。
//
// 取 10 分钟是有意从宽：上游存在合法的长静默阶段（如慢速图像生成会先回 200 再静默数分钟才吐事件），
// idle 看门狗只用于兜底「半开/挂死连接」这种永不推进的异常，绝不能误杀正常长任务流。
const DefaultStreamIdleTimeout = 10 * time.Minute

// streamIdleTimeoutNanos 是运行期可配置的流式 idle 超时（纳秒）；0 表示回退 DefaultStreamIdleTimeout。
//
// 由进程启动期 SetStreamIdleTimeout 设置一次并由 settings applier 热更新。
// 用 atomic 仅为读写竞态安全；预期 serve 前设置、serve 中只读。
var streamIdleTimeoutNanos atomic.Int64

// SetStreamIdleTimeout 设置全局流式 idle 超时。d<=0 时回退内置默认值。
func SetStreamIdleTimeout(d time.Duration) {
	if d <= 0 {
		streamIdleTimeoutNanos.Store(0)
		return
	}
	streamIdleTimeoutNanos.Store(int64(d))
}

// StreamIdleTimeout 返回当前生效的流式 idle 超时；未配置时返回 DefaultStreamIdleTimeout。
func StreamIdleTimeout() time.Duration {
	if n := streamIdleTimeoutNanos.Load(); n > 0 {
		return time.Duration(n)
	}
	return DefaultStreamIdleTimeout
}

// UpstreamTimeoutPhase 是稳定的超时阶段（§11.4）。只有超时失败时才有值。
//
// 错误码、Sticky 清绑原因、错误率样本和 Admin 展示都消费这一稳定阶段，
// 禁止从错误文本猜测「到底卡在哪一步」。
type UpstreamTimeoutPhase string

const (
	// TimeoutPhaseResponseHeader 上游未在 response_timeout_ms 内返回 HTTP 响应头。
	TimeoutPhaseResponseHeader UpstreamTimeoutPhase = "response_header"
	// TimeoutPhaseFirstToken 流式：响应头已到，但未在 first_token_timeout_ms 内产生首个有效生成 Token。
	TimeoutPhaseFirstToken UpstreamTimeoutPhase = "first_token"
	// TimeoutPhaseStreamIdle 流式：首个有效生成 Token 之后连接静默超过 stream_idle_timeout_ms。
	TimeoutPhaseStreamIdle UpstreamTimeoutPhase = "stream_idle"
	// TimeoutPhaseResponseBody 非流式：响应头已到，但完整响应体未在 response_timeout_ms 内读完并解析。
	TimeoutPhaseResponseBody UpstreamTimeoutPhase = "response_body"
)

// ErrStreamIdleTimeout 表示流式上游在 idle 超时窗口内未推进任何字节（疑似半开/挂死连接）。
//
// 它沿 context cause 暴露：idle 看门狗触发后会 cancelCause(ErrStreamIdleTimeout) 取消流 context，
// 在途的 body 读取随之失败。stream adapter 据此把读流错误归类为「上游超时」而非通用读失败。
var ErrStreamIdleTimeout = errors.New("adapter: upstream stream idle timeout")

// ErrFirstTokenTimeout 表示流式上游已返回响应头，但未在首字预算内产生任何有效生成 Token。
//
// 它与 idle 超时是不同的故障：idle 说明「曾经推进过、后来卡住」，首字超时说明「从未真正开始」。
// 两者在 Sticky 清绑、错误率样本和 Admin 展示上都必须可区分。
var ErrFirstTokenTimeout = errors.New("adapter: upstream first token timeout")

// StreamTimeoutState 暴露一次流式调用最终卡在哪个阶段，供调用方落 upstream_timeout_phase。
type StreamTimeoutState struct {
	headersDone atomic.Bool
	firstToken  atomic.Bool
	phase       atomic.Pointer[UpstreamTimeoutPhase]
}

func (s *StreamTimeoutState) markPhase(phase UpstreamTimeoutPhase) {
	// 只记录第一个触发的阶段：后续取消都是它的连带结果。
	s.phase.CompareAndSwap(nil, &phase)
}

// TimeoutPhase 返回已观测到的超时阶段；空字符串表示本次调用没有因超时失败。
func (s *StreamTimeoutState) TimeoutPhase() UpstreamTimeoutPhase {
	if s == nil {
		return ""
	}
	if phase := s.phase.Load(); phase != nil {
		return *phase
	}
	return ""
}

// StreamTimeoutHandles 是流式超时上下文的控制句柄。
type StreamTimeoutHandles struct {
	// HeadersReceived 在拿到上游 HTTP 响应头后调用：停掉响应头计时器。
	// 它不停首字计时器——首字预算与响应头预算共享同一起点（§11.2）。
	HeadersReceived func()
	// FirstToken 在首个包含有效生成 Token 的协议事件到达时调用：停掉首字计时器并启动 idle
	// 看门狗。HTTP 响应头、协议前导事件、SSE 空行、注释和纯心跳都不得调用它。
	FirstToken func()
	// ResetIdle 在首个有效生成 Token 之后的每次流活动（协议事件或上游合法 SSE 心跳）时调用。
	// 首个有效生成 Token 之前调用是无效的：心跳只能证明连接存活，不能停止首字计时。
	ResetIdle func()
	// State 暴露最终超时阶段。
	State *StreamTimeoutState
	// Cancel 必须 defer 调用以停止全部计时器并释放资源。
	Cancel context.CancelFunc
}

type streamTimeoutStartContextKey struct{}

func startStreamTimeout(ctx context.Context) {
	if ctx == nil {
		return
	}
	if start, ok := ctx.Value(streamTimeoutStartContextKey{}).(func()); ok && start != nil {
		start()
	}
}

// StreamTimeoutConfig 是流式上游调用的三段超时预算（§11.2）。
type StreamTimeoutConfig struct {
	// ResponseHeader 限制「建连 + 拿到 HTTP 响应头」。<=0 表示不设该保护。
	ResponseHeader time.Duration
	// FirstToken 限制「从发起调用到首个有效生成 Token」，与 ResponseHeader 同起点。<=0 表示不设。
	FirstToken time.Duration
	// Idle 是首个有效生成 Token 之后的静默看门狗。<=0 表示不启用。
	Idle time.Duration
}

// StreamTimeoutContext 为流式上游调用派生 context，提供三段相互独立的超时保护。
// 计时器在 MarkTransportStarted 被调用时才启动，使预算起点与 upstream_started_at 完全一致：
//
//  1. ResponseHeader：约束「上游开始响应（建连 + 返回响应头）」。拿到响应头后由 HeadersReceived 解除。
//     绝不能用它约束流本体：长补全 / 图像生成会合法地流式数分钟。
//  2. FirstToken：与 ResponseHeader 从同一时刻起算，约束「首个有效生成 Token」。
//     这是关键约束——如果等响应头完成后才启动首字计时，一个「秒回 200 然后静默」的上游
//     会先耗尽响应头预算之外的完整首字预算，用户实际等待翻倍。
//  3. Idle：首个有效生成 Token 之后的静默看门狗（防半开 / 挂死连接）。必须显著大于上游合法的最长静默阶段。
//
// 用法：
//
//	ctx, h := adapter.StreamTimeoutContext(ctx, adapter.StreamTimeoutConfig{...})
//	defer h.Cancel()
//	MarkTransportStarted(ctx)
//	resp, err := client.Do(req.WithContext(ctx))
//	h.HeadersReceived()
//	reader := sse.NewReader(resp.Body, sse.Config{OnActivity: h.ResetIdle, ...})
//	for reader.Next() { /* 首个有效生成 Token 时 h.FirstToken() */ }
func StreamTimeoutContext(
	parent context.Context, config StreamTimeoutConfig,
) (context.Context, StreamTimeoutHandles) {
	ctx, cancelCause := context.WithCancelCause(parent)
	state := &StreamTimeoutState{}

	var (
		mu              sync.Mutex
		headerTimer     *time.Timer
		firstTokenTimer *time.Timer
		idleTimer       *time.Timer
		started         bool
		canceled        bool
	)

	start := func() {
		mu.Lock()
		defer mu.Unlock()
		if started || canceled {
			return
		}
		started = true
		if config.ResponseHeader > 0 && !state.headersDone.Load() {
			headerTimer = time.AfterFunc(config.ResponseHeader, func() {
				state.markPhase(TimeoutPhaseResponseHeader)
				cancelCause(context.DeadlineExceeded)
			})
		}
		// 首字计时器与响应头计时器在同一 transport start 启动，共享 upstream_started_at。
		if config.FirstToken > 0 && !state.firstToken.Load() {
			firstTokenTimer = time.AfterFunc(config.FirstToken, func() {
				state.markPhase(TimeoutPhaseFirstToken)
				cancelCause(ErrFirstTokenTimeout)
			})
		}
		if config.Idle > 0 && idleTimer == nil &&
			(state.firstToken.Load() || (state.headersDone.Load() && config.FirstToken <= 0)) {
			idleTimer = time.AfterFunc(config.Idle, func() {
				state.markPhase(TimeoutPhaseStreamIdle)
				cancelCause(ErrStreamIdleTimeout)
			})
		}
	}
	ctx = context.WithValue(ctx, streamTimeoutStartContextKey{}, start)

	handles := StreamTimeoutHandles{State: state}

	handles.HeadersReceived = func() {
		mu.Lock()
		defer mu.Unlock()
		if state.headersDone.Swap(true) {
			return
		}
		if headerTimer != nil {
			headerTimer.Stop()
		}
		// 有首字预算时故意不动 firstTokenTimer 也不起 idle：首个有效生成 Token 之前 idle 无意义，
		// 否则一个只发心跳的上游会靠 idle 重置无限拖住首字。
		//
		// 没有配置首字预算时必须在这里就起 idle：否则「响应头已到、永不产生有效事件」的连接
		// 完全没有看门狗，请求会挂到客户端断开为止。
		if started && config.FirstToken <= 0 && config.Idle > 0 && idleTimer == nil {
			idleTimer = time.AfterFunc(config.Idle, func() {
				state.markPhase(TimeoutPhaseStreamIdle)
				cancelCause(ErrStreamIdleTimeout)
			})
		}
	}

	handles.FirstToken = func() {
		mu.Lock()
		defer mu.Unlock()
		if state.firstToken.Swap(true) {
			return
		}
		if firstTokenTimer != nil {
			firstTokenTimer.Stop()
		}
		if started && config.Idle > 0 && idleTimer == nil {
			idleTimer = time.AfterFunc(config.Idle, func() {
				state.markPhase(TimeoutPhaseStreamIdle)
				cancelCause(ErrStreamIdleTimeout)
			})
		}
	}

	handles.ResetIdle = func() {
		if config.Idle <= 0 {
			return
		}
		mu.Lock()
		// 有首字预算时，首个有效生成 Token 之前 idleTimer 尚未创建，因此心跳自然无法重置任何东西——
		// 这正是「心跳不能停止首字计时」的实现方式（§11.2）。
		if idleTimer != nil {
			idleTimer.Reset(config.Idle)
		}
		mu.Unlock()
	}

	handles.Cancel = func() {
		mu.Lock()
		canceled = true
		for _, timer := range []*time.Timer{headerTimer, firstTokenTimer, idleTimer} {
			if timer != nil {
				timer.Stop()
			}
		}
		mu.Unlock()
		cancelCause(context.Canceled)
	}

	return ctx, handles
}

// NonStreamTimeoutPhaseOf 判定非流式超时卡在响应头还是响应体（§11.1）。
// headersReceived 由调用方在拿到 HTTP 响应头后置真。
func NonStreamTimeoutPhaseOf(headersReceived bool) UpstreamTimeoutPhase {
	if headersReceived {
		return TimeoutPhaseResponseBody
	}
	return TimeoutPhaseResponseHeader
}
