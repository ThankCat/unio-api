package adapter

import (
	"net/http"
	"strconv"
	"time"
)

// maxParsedRetryAfter 是解析阶段对 Retry-After 的硬上限（24h），防御 provider 返回异常大值；
// 业务侧（gateway cooldown）仍会按自身配置再做一次更小的 cap。
const maxParsedRetryAfter = 24 * time.Hour

// ParseRetryAfterHeader 解析 HTTP Retry-After 头，返回建议的冷却时长（P2-7）。
//
// 支持 RFC 7231 两种形式：delta-seconds（非负整数秒）与 HTTP-date（按相对 now 的剩余时间）。
// 缺失、格式非法、零或负值统一返回 0（表示「无可用建议」）。结果按 maxParsedRetryAfter 截断。
func ParseRetryAfterHeader(h http.Header) time.Duration {
	if h == nil {
		return 0
	}
	return parseRetryAfterValue(h.Get("Retry-After"), time.Now())
}

// parseRetryAfterValue 是可注入 now 的纯函数实现，便于单测覆盖 delta-seconds 与 HTTP-date 两路。
func parseRetryAfterValue(value string, now time.Time) time.Duration {
	if value == "" {
		return 0
	}

	// delta-seconds：非负整数秒。
	if secs, err := strconv.Atoi(value); err == nil {
		if secs <= 0 {
			return 0
		}
		return clampRetryAfter(time.Duration(secs) * time.Second)
	}

	// HTTP-date：取相对 now 的剩余时间。
	if t, err := http.ParseTime(value); err == nil {
		d := t.Sub(now)
		if d <= 0 {
			return 0
		}
		return clampRetryAfter(d)
	}

	return 0
}

func clampRetryAfter(d time.Duration) time.Duration {
	if d > maxParsedRetryAfter {
		return maxParsedRetryAfter
	}
	return d
}
