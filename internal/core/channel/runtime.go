package channel

import "time"

// SupplyForm 是渠道的供给形态：凭据从哪里来，容量与健康按哪一层核算。
type SupplyForm string

const (
	// SupplyFormCredential 是存量形态：渠道自身持一份 API Key，容量与健康都在渠道层。
	SupplyFormCredential SupplyForm = "credential"
	// SupplyFormPool 表示渠道下挂一池订阅账号：渠道不持凭据，凭据、并发、健康与用量都在账号上。
	// 协议、adapter、模型绑定、定价与超时仍在渠道层，故 routing 的候选单位不变。
	SupplyFormPool SupplyForm = "pool"
)

// IsPool 让调用方不必逐处比较字符串常量；未知取值一律按 credential 处理，
// 使新增形态在未适配的代码路径上退化为存量行为而不是崩溃。
func (f SupplyForm) IsPool() bool {
	return f == SupplyFormPool
}

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
