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
// idle 看门狗只用于兜底「半开/挂死连接」这种永不推进的异常，绝不能误杀正常长任务流。运维需要更激进或
// 更宽松的检测时由 GATEWAY_STREAM_IDLE_TIMEOUT 调整。
const DefaultStreamIdleTimeout = 10 * time.Minute

// streamIdleTimeoutNanos 是运行期可配置的流式 idle 超时（纳秒）；0 表示回退 DefaultStreamIdleTimeout。
//
// 由进程启动期 SetStreamIdleTimeout 设置一次（gateway server bootstrap 读 GATEWAY_STREAM_IDLE_TIMEOUT）。
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

// ErrStreamIdleTimeout 表示流式上游在 idle 超时窗口内未推进任何字节（疑似半开/挂死连接）。
//
// 它沿 context cause 暴露：idle 看门狗触发后会 cancelCause(ErrStreamIdleTimeout) 取消流 context，
// 在途的 body 读取随之失败。stream adapter 据此把读流错误归类为「上游超时」而非通用读失败。
var ErrStreamIdleTimeout = errors.New("adapter: upstream stream idle timeout")

// StreamTimeoutContext 为流式上游调用派生 context，提供两段相互独立的超时保护：
//
//  1. headerTimeout：约束「上游开始响应(建连 + 返回响应头/首字节)」的等待时长（即渠道 timeout）。
//     拿到响应头后由 headersReceived() 解除——绝不能用它约束流本体：长补全 / 图像生成会合法地流式数分钟，
//     若把渠道 timeout 当成整段读流的绝对截止时间，会在 timeout 处被 deadline exceeded 掐断。
//  2. idleTimeout：拿到响应头后启用的「相邻两次流活动之间」最大静默时长看门狗（防半开 / 挂死连接）。
//     调用方须在每次读到流活动后调用 resetIdle()；静默超过 idleTimeout 即以 ErrStreamIdleTimeout 取消 context。
//     idleTimeout<=0 时不启用，resetIdle 为空操作。注意 idleTimeout 必须显著大于上游合法的最长静默阶段，
//     否则会误杀正常长任务流。
//
// 用法：
//
//	ctx, headersReceived, resetIdle, cancel := adapter.StreamTimeoutContext(ctx, ch.Timeout, idle)
//	defer cancel()
//	resp, err := client.Do(req.WithContext(ctx))
//	headersReceived()              // 解除 header 超时；idleTimeout>0 时起 idle 看门狗
//	reader := sse.NewReader(resp.Body, sse.Config{OnActivity: resetIdle, ...})
//	for reader.Next() { ... }      // reader 每读到一行即 resetIdle
//
// 返回的 cancel 必须 defer 调用以停止计时器并释放资源。
func StreamTimeoutContext(parent context.Context, headerTimeout, idleTimeout time.Duration) (ctx context.Context, headersReceived func(), resetIdle func(), cancel context.CancelFunc) {
	ctx, cancelCause := context.WithCancelCause(parent)

	var (
		mu          sync.Mutex
		idleTimer   *time.Timer
		headerTimer *time.Timer
	)

	if headerTimeout > 0 {
		headerTimer = time.AfterFunc(headerTimeout, func() {
			cancelCause(context.DeadlineExceeded)
		})
	}

	headersReceived = func() {
		mu.Lock()
		defer mu.Unlock()
		if headerTimer != nil {
			headerTimer.Stop()
		}
		if idleTimeout > 0 && idleTimer == nil {
			idleTimer = time.AfterFunc(idleTimeout, func() {
				cancelCause(ErrStreamIdleTimeout)
			})
		}
	}

	resetIdle = func() {
		if idleTimeout <= 0 {
			return
		}
		mu.Lock()
		if idleTimer != nil {
			idleTimer.Reset(idleTimeout)
		}
		mu.Unlock()
	}

	cancel = func() {
		mu.Lock()
		if headerTimer != nil {
			headerTimer.Stop()
		}
		if idleTimer != nil {
			idleTimer.Stop()
		}
		mu.Unlock()
		cancelCause(context.Canceled)
	}

	return ctx, headersReceived, resetIdle, cancel
}
