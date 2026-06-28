package adapter

import (
	"io"
	"sync/atomic"
)

// DefaultMaxUpstreamResponseBytes 是非流式上游响应体的默认字节上限（运行期未配置时的兜底值）。
//
// 这是防 OOM 的安全阀：异常/恶意上游可能对一次非流式请求返回任意大的 body，若整体读入内存会撑爆进程。
// 默认 8 MiB 足以覆盖正常 chat/messages 响应（含长上下文回显与 tool_calls）。
const DefaultMaxUpstreamResponseBytes int64 = 8 << 20

// maxUpstreamResponseBytes 是运行期可配置的非流式上游响应体上限（字节）；0 表示回退 DefaultMaxUpstreamResponseBytes。
//
// 由进程启动期 SetMaxUpstreamResponseBytes 设置一次（gateway server bootstrap 读 GATEWAY_MAX_UPSTREAM_RESPONSE_MB）。
// 用 atomic 仅为读写竞态安全；预期 serve 前设置、serve 中只读。
var maxUpstreamResponseBytes atomic.Int64

// SetMaxUpstreamResponseBytes 设置全局非流式上游响应体上限（字节）。n<=0 时回退内置默认值。
func SetMaxUpstreamResponseBytes(n int64) {
	if n <= 0 {
		maxUpstreamResponseBytes.Store(0)
		return
	}
	maxUpstreamResponseBytes.Store(n)
}

// MaxUpstreamResponseBytes 返回当前生效的非流式上游响应体上限（字节）；未配置时返回 DefaultMaxUpstreamResponseBytes。
func MaxUpstreamResponseBytes() int64 {
	if n := maxUpstreamResponseBytes.Load(); n > 0 {
		return n
	}
	return DefaultMaxUpstreamResponseBytes
}

// ReadUpstreamBodyLimited 读取非流式上游响应体，但最多读 MaxUpstreamResponseBytes()+1 字节，并报告是否超限。
//
// 设计为「读到上限+1 即可判定超限」：避免把任意大的 body 整体读入内存。exceeded=true 时返回的 data 已截断到
// limit，调用方应据此返回 CodeAdapterResponseTooLarge 而非把截断 JSON 当作 decode 失败。本函数不依赖 failure
// 包（保持 core/adapter 主体无 failure 依赖），错误分类由各 adapter 收口。
func ReadUpstreamBodyLimited(r io.Reader) (data []byte, exceeded bool, err error) {
	limit := MaxUpstreamResponseBytes()
	data, err = io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}
