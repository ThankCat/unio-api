package adapter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultStreamIdleTimeout 是流式上游「相邻两次数据之间」最大静默时长的默认值（运行期未配置时的兜底）。
//
// 与 Sub2API 的 stream_data_interval_timeout 默认值一致（180s）：它从响应头到达起生效，任何上游数据
// （协议事件、SSE 注释/空行）都重置它，只兜底「半开/挂死连接」；模型思考期间上游会持续推 reasoning
// 事件，不会被它误杀。
const DefaultStreamIdleTimeout = 180 * time.Second

// streamIdleTimeoutNanos 是运行期配置的流式 idle 超时（纳秒）；streamIdleTimeoutSet 为 false 时回退默认。
// 值 0 表示显式关闭 idle 看门狗（与 Sub2API 一致：0 = 禁用）。
//
// 由进程启动期 SetStreamIdleTimeout 设置一次并由 settings applier 热更新。
var (
	streamIdleTimeoutNanos atomic.Int64
	streamIdleTimeoutSet   atomic.Bool
)

// SetStreamIdleTimeout 设置全局流式 idle 超时：d<0 回退内置默认值；d==0 关闭看门狗；d>0 生效。
func SetStreamIdleTimeout(d time.Duration) {
	if d < 0 {
		streamIdleTimeoutSet.Store(false)
		streamIdleTimeoutNanos.Store(0)
		return
	}
	streamIdleTimeoutNanos.Store(int64(d))
	streamIdleTimeoutSet.Store(true)
}

// StreamIdleTimeout 返回当前生效的流式 idle 超时；未配置时返回 DefaultStreamIdleTimeout，0 表示关闭。
func StreamIdleTimeout() time.Duration {
	if streamIdleTimeoutSet.Load() {
		return time.Duration(streamIdleTimeoutNanos.Load())
	}
	return DefaultStreamIdleTimeout
}

// TTFTMode 是首字（TTFT）统计口径，对齐 Sub2API 的 openai_ttft_mode。
//
// 它只决定「哪一个事件记为首字」（上游 TTFT 样本、Gateway TTFT、首字超时的解除点），
// 不影响计费：partial settlement 始终只看是否已向客户交付可见生成内容。
type TTFTMode string

const (
	// TTFTModeSemantic 以跳过协议前导后的首个结构性事件为首字（Sub2API 默认）。
	// reasoning 模型思考阶段推送的 reasoning item 事件即算，TTFT 反映「模型开始工作」。
	TTFTModeSemantic TTFTMode = "semantic"
	// TTFTModeVisible 以首个携带客户可用内容（文本 / 推理摘要 / 工具参数）的事件为首字，
	// TTFT 反映「客户看到第一个字」。
	TTFTModeVisible TTFTMode = "visible"
)

// DefaultTTFTMode 与 Sub2API 默认一致。
const DefaultTTFTMode = TTFTModeSemantic

var ttftMode atomic.Value

// SetTTFTMode 设置全局 TTFT 口径；非法值回退默认。由 settings applier 热更新。
func SetTTFTMode(mode TTFTMode) {
	ttftMode.Store(NormalizeTTFTMode(string(mode)))
}

// CurrentTTFTMode 返回当前生效的 TTFT 口径。
func CurrentTTFTMode() TTFTMode {
	if v, ok := ttftMode.Load().(TTFTMode); ok && v != "" {
		return v
	}
	return DefaultTTFTMode
}

// NormalizeTTFTMode 把配置值归一为合法口径；未识别一律 semantic。
func NormalizeTTFTMode(raw string) TTFTMode {
	if TTFTMode(raw) == TTFTModeVisible {
		return TTFTModeVisible
	}
	return TTFTModeSemantic
}

// TTFTEligible 判定一个协议事件在当前口径下是否记为首字：progress 是结构性进展，visible 是可见生成内容。
func TTFTEligible(progress, visible bool) bool {
	if CurrentTTFTMode() == TTFTModeVisible {
		return visible
	}
	return progress || visible
}

// UpstreamTimeoutPhase 是稳定的超时阶段（§11.4）。只有超时失败时才有值。
//
// 错误码、Sticky 清绑原因、错误率样本和 Admin 展示都消费这一稳定阶段，
// 禁止从错误文本猜测「到底卡在哪一步」。
type UpstreamTimeoutPhase string

const (
	// TimeoutPhaseResponseHeader 上游未在响应头预算内返回 HTTP 响应头。
	TimeoutPhaseResponseHeader UpstreamTimeoutPhase = "response_header"
	// TimeoutPhaseFirstToken 流式：未在首字预算内出现任何上游进展（结构性事件或有效生成 Token）。
	// 首字预算从 transport start 起算，因此它可能在响应头到达前先于更宽松的响应头预算触发。
	TimeoutPhaseFirstToken UpstreamTimeoutPhase = "first_token"
	// TimeoutPhaseStreamIdle 流式：响应头到达后，相邻两次上游数据之间静默超过 idle 超时。
	TimeoutPhaseStreamIdle UpstreamTimeoutPhase = "stream_idle"
	// TimeoutPhaseResponseBody 非流式：响应头已到，但完整响应体未在 response_timeout_ms 内读完并解析。
	TimeoutPhaseResponseBody UpstreamTimeoutPhase = "response_body"
)

// ErrStreamIdleTimeout 表示流式上游在 idle 超时窗口内未推进任何字节（疑似半开/挂死连接）。
//
// 它沿 context cause 暴露：idle 看门狗触发后会 cancelCause(ErrStreamIdleTimeout) 取消流 context，
// 在途的 body 读取随之失败。stream adapter 据此把读流错误归类为「上游超时」而非通用读失败。
var ErrStreamIdleTimeout = errors.New("adapter: upstream stream idle timeout")

// ErrFirstTokenTimeout 表示流式上游未在首字预算内出现任何进展。
// 首字预算从 transport start 起算，因此该错误也可能在响应头到达前触发。
//
// 它与 idle 超时是不同的故障：idle 说明「曾经有数据、后来卡住」，首字超时说明「从未真正开始」。
// 两者在错误率样本和 Admin 展示上都必须可区分。
var ErrFirstTokenTimeout = errors.New("adapter: upstream first token timeout")

// StreamTimeoutState 暴露一次流式调用最终卡在哪个阶段，供调用方落 upstream_timeout_phase。
type StreamTimeoutState struct {
	headersDone atomic.Bool
	progress    atomic.Bool
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
	// HeadersReceived 在拿到上游 HTTP 响应头后调用：停掉响应头计时器，并启动 idle 看门狗
	// （数据间隔超时从此刻起生效）。它不停首字计时器——首字预算与响应头预算共享同一起点（§11.2）。
	HeadersReceived func()
	// Progress 在首个「上游进展」事件到达时调用：停掉首字计时器。进展 = 跳过协议前导
	// （response.created / in_progress 等）后的首个结构性事件，与 Sub2API 的 client output started
	// 同口径；SSE 空行、注释和纯心跳不算进展。
	Progress func()
	// FirstToken 在首个携带有效生成 Token 的协议事件到达时调用；有效 Token 必然也是进展，
	// 因此等价于 Progress，保留供只认可见内容的协议（chat / Anthropic）沿用。
	FirstToken func()
	// ResetIdle 在响应头之后的每次上游活动（协议事件、SSE 空行/注释、心跳）时调用，重置数据间隔看门狗。
	// 它不会影响首字计时：心跳只能证明连接存活，不能证明模型开始工作。
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

// StreamTimeoutConfig 是流式上游调用的三段超时预算（§11.2，2026-09-05 对齐 Sub2API 语义）。
// 三项都以 0 表示「不限制」——与 Sub2API 一致，默认全部为 0 / idle 180s，由运行时配置决定。
type StreamTimeoutConfig struct {
	// ResponseHeader 限制「建连 + 拿到 HTTP 响应头」。<=0 表示不设该保护。
	ResponseHeader time.Duration
	// FirstToken 限制「从发起调用到首个上游进展」，与 ResponseHeader 同起点。<=0 表示不设。
	FirstToken time.Duration
	// Idle 是响应头到达后相邻两次上游数据之间的最大静默（数据间隔看门狗）。<=0 表示不启用。
	Idle time.Duration
}

// StreamTimeoutContext 为流式上游调用派生 context，提供三段相互独立的超时保护。
// 计时器在 MarkTransportStarted 被调用时才启动，使预算起点与 upstream_started_at 完全一致：
//
//  1. ResponseHeader：约束「上游开始响应（建连 + 返回响应头）」。拿到响应头后由 HeadersReceived 解除。
//     绝不能用它约束流本体：长补全 / 图像生成会合法地流式数分钟。
//  2. FirstToken：与 ResponseHeader 从同一时刻起算，约束「首个上游进展」（Progress / FirstToken 解除）。
//     如果等响应头完成后才启动首字计时，一个「秒回 200 然后静默」的上游会先耗尽响应头预算之外的
//     完整首字预算，用户实际等待翻倍。
//  3. Idle：响应头到达后即启动的数据间隔看门狗（防半开 / 挂死连接），任何上游活动重置。
//     它与首字预算独立：心跳能重置 idle，但不能解除首字计时。
//
// 用法：
//
//	ctx, h := adapter.StreamTimeoutContext(ctx, adapter.StreamTimeoutConfig{...})
//	defer h.Cancel()
//	MarkTransportStarted(ctx)
//	resp, err := client.Do(req.WithContext(ctx))
//	h.HeadersReceived()
//	reader := sse.NewReader(resp.Body, sse.Config{OnActivity: h.ResetIdle, ...})
//	for reader.Next() { /* 首个进展事件时 h.Progress()，有效生成 Token 时 h.FirstToken() */ }
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

	armIdle := func() {
		if config.Idle > 0 && idleTimer == nil && started && !canceled && state.headersDone.Load() {
			idleTimer = time.AfterFunc(config.Idle, func() {
				state.markPhase(TimeoutPhaseStreamIdle)
				cancelCause(ErrStreamIdleTimeout)
			})
		}
	}

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
		if config.FirstToken > 0 && !state.progress.Load() {
			firstTokenTimer = time.AfterFunc(config.FirstToken, func() {
				state.markPhase(TimeoutPhaseFirstToken)
				cancelCause(ErrFirstTokenTimeout)
			})
		}
		armIdle()
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
		// 数据间隔看门狗从响应头到达起生效：「响应头已到、之后一个字节都不来」的连接由它兜底，
		// 与首字预算是否配置无关。
		armIdle()
	}

	handles.Progress = func() {
		mu.Lock()
		defer mu.Unlock()
		if state.progress.Swap(true) {
			return
		}
		if firstTokenTimer != nil {
			firstTokenTimer.Stop()
		}
	}
	handles.FirstToken = handles.Progress

	handles.ResetIdle = func() {
		if config.Idle <= 0 {
			return
		}
		mu.Lock()
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
