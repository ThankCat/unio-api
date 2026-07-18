package channel

import "time"

// Runtime 表示一次 adapter 调用使用的运行时渠道参数。
type Runtime struct {
	ID      int64
	Name    string
	BaseURL string
	APIKey  string
	Timeout time.Duration

	// ProviderSlug 是业务 provider 标识（providers.slug），供 adapter 选择 stream translator；由 routing 注入。
	ProviderSlug string
}
