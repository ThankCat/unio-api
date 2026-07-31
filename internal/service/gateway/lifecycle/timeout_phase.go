package lifecycle

import (
	"context"
	"errors"
	"net"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
)

// TimeoutPhaseOf 判定一次 attempt 是否因超时失败，以及卡在哪个稳定阶段（§11.4）。
//
// 返回空字符串表示「不是超时失败」。错误码、Sticky 清绑原因、错误率样本和 Admin 展示都消费
// 这个稳定阶段，禁止从错误文本猜测——所以阶段只从两类硬事实推导：
//   - 专用哨兵错误（首字超时 / 流 idle 超时）本身就唯一确定阶段；
//   - 其余超时按已观测到的 transport 进度（是否拿到响应头、是否收到首个有效生成 Token）判定。
func TimeoutPhaseOf(err error, stream bool, timing AttemptTimingFacts) adapter.UpstreamTimeoutPhase {
	if err == nil {
		return ""
	}
	// 哨兵错误是无歧义的：不必再看 transport 进度。
	if errors.Is(err, adapter.ErrFirstTokenTimeout) {
		return adapter.TimeoutPhaseFirstToken
	}
	if errors.Is(err, adapter.ErrStreamIdleTimeout) {
		return adapter.TimeoutPhaseStreamIdle
	}
	if !isTimeoutFailure(err) {
		return ""
	}
	if !timing.ResponseHeadersSeen {
		// 连响应头都没拿到：无论流式与否都是响应头阶段。
		return adapter.TimeoutPhaseResponseHeader
	}
	if !stream {
		return adapter.NonStreamTimeoutPhaseOf(true)
	}
	if timing.UpstreamFirstTokenAt == nil {
		// 响应头已到但从未产生有效生成 Token。
		return adapter.TimeoutPhaseFirstToken
	}
	return adapter.TimeoutPhaseStreamIdle
}

// isTimeoutFailure 只承认可证明的超时：context deadline、net 超时错误，
// 以及 adapter 归类为 timeout 的上游错误。客户端取消不是超时。
func isTimeoutFailure(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if category, ok := adapter.UpstreamCategoryOf(err); ok && category == adapter.UpstreamErrorTimeout {
		return true
	}
	return false
}
