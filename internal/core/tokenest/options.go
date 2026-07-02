// Package tokenest 提供与 new-api 对齐的输入 token 估算：对「提取出的文本内容」跑 tiktoken +
// 每消息/工具的框架开销 + 图片 tile/patch token 数学（绝不 tokenize base64），音频/文件/视频按固定值。
//
// 估算仅用于「限流预占」与「计费冻结」的保守上界；真实计费以 settlement 阶段上游 usage 为准。
// 之所以不数整包 wire JSON：JSON 结构字符 + base64 会把估算放大数倍甚至数十倍（DEC：见渠道限流复盘）。
package tokenest

import (
	"sync/atomic"
	"time"
)

// Options 控制媒体 token 估算行为，语义对齐 new-api 的 GetMediaToken / GetMediaTokenNotStream。
type Options struct {
	// CountMedia 关闭时，图片一律按固定保守值（3×base）估算，不解码 base64、不抓取 URL。
	CountMedia bool

	// FetchRemoteImages 打开时，对 http(s) URL 图片抓取以读取尺寸做精确 tile 估算；
	// 关闭时 URL 图片按固定保守值（3×base）。默认关闭：抓取任意客户 URL 在网关热路径上有
	// SSRF/延迟风险（new-api 同样把 URL 抓取藏在开关后）。内联 base64 图片始终本地解码，不受此开关影响。
	FetchRemoteImages bool

	// FetchTimeout / FetchMaxBytes 限制远程图片抓取的耗时与体积（仅 FetchRemoteImages 打开时生效）。
	FetchTimeout  time.Duration
	FetchMaxBytes int64
}

// defaultOptions 是媒体估算的内置默认（媒体计入、不抓 URL、抓取上限 3s / 8MiB）。
func defaultOptions() Options {
	return Options{
		CountMedia:        true,
		FetchRemoteImages: false,
		FetchTimeout:      3 * time.Second,
		FetchMaxBytes:     8 << 20,
	}
}

var configured atomic.Pointer[Options]

// Configure 在进程启动时注入媒体估算配置（bootstrap 调用）。零值字段回退内置默认。
func Configure(o Options) {
	def := defaultOptions()
	if o.FetchTimeout <= 0 {
		o.FetchTimeout = def.FetchTimeout
	}
	if o.FetchMaxBytes <= 0 {
		o.FetchMaxBytes = def.FetchMaxBytes
	}
	configured.Store(&o)
}

// activeOptions 返回当前生效的媒体估算配置；未 Configure 时返回内置默认。
func activeOptions() Options {
	if p := configured.Load(); p != nil {
		return *p
	}
	return defaultOptions()
}
