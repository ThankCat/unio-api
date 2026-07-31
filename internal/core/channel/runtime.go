package channel

import "time"

// Runtime 表示一次 adapter 调用使用的运行时渠道参数。
type Runtime struct {
	ID     int64
	Name   string
	Origin string
	APIKey string

	// ResponseTimeout 限制「拿到上游 HTTP 响应头」（流式）或「完整响应体 + adapter 解析完成」（非流式）。
	// 从 upstream_started_at 起算，恒为正数——0/负数会关闭保护并产生无法结束的请求（§11.3）。
	ResponseTimeout time.Duration

	// FirstTokenTimeout 只用于流式：从同一个 upstream_started_at 起算，限制首个有效生成 Token（§11.2）。
	// 两个预算共享起点，不能等响应头到达后再重新给一份完整首字预算。
	FirstTokenTimeout time.Duration

	// ProviderSlug 是业务 provider 标识（providers.slug），供 adapter 选择 stream translator；由 routing 注入。
	ProviderSlug string
}
