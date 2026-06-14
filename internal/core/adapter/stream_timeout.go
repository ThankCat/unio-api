package adapter

import (
	"context"
	"time"
)

// HeaderTimeoutContext 为流式上游调用派生一个「只约束响应头阶段」的 context。
//
// 流式语义:渠道 timeout 只用于约束「上游开始响应(建连 + 返回响应头/首字节)」的等待时长,
// 绝不能约束流本体——长补全 / 图像生成会合法地持续流式数分钟。若把渠道 timeout 当成绝对截止时间
// 罩住整段读流(context.WithTimeout 包住整个 client.Do + body 读取循环),流会在 timeout 处被
// context deadline exceeded 掐断,客户端表现为 "stream disconnected"。流的总时长应由客户端断开
// (父 ctx 取消)兜底,而非渠道 timeout。
//
// 用法:
//
//	streamCtx, headersReceived, cancel := adapter.HeaderTimeoutContext(ctx, ch.Timeout)
//	defer cancel()
//	req, _ := http.NewRequestWithContext(streamCtx, ...)
//	resp, err := client.Do(req)
//	headersReceived() // 拿到响应头即解除 timeout;之后流体读取只由父 ctx(客户端断开)兜底
//
// timeout<=0 时不设超时,headersReceived 为空操作。返回的 cancel 必须 defer 调用以释放资源。
//
// 边界:若上游恰在 timeout 临界点返回响应头,timer 可能在 headersReceived() 之前已触发 cancel,
// 此时 body 读取会立即失败——这与「超过响应头等待时长」语义一致,可接受。
func HeaderTimeoutContext(parent context.Context, timeout time.Duration) (ctx context.Context, headersReceived func(), cancel context.CancelFunc) {
	ctx, cancel = context.WithCancel(parent)
	if timeout <= 0 {
		return ctx, func() {}, cancel
	}
	timer := time.AfterFunc(timeout, cancel)
	return ctx, func() { timer.Stop() }, cancel
}
