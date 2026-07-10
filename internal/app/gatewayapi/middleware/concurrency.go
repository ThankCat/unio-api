package middleware

import (
	"net/http"

	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
)

// KeyConcurrencyLimiter 定义 ingress 对「线路+用户」在途并发数设限的能力（DEC-029）。
//
// ok=false 表示该主体在途请求已达全局默认 key_limit（立即 429，不发起任何上游调用/预授权）；
// ok=true 时返回的 release 必须在整个请求结束（含流式传输完成/中断）后调用，release 幂等。
type KeyConcurrencyLimiter interface {
	AcquireRouteUser(routeID, userID int64) (release func(), ok bool)
}

// ConcurrencyLimit 在 ingress 用认证身份对「线路+用户」做在途并发限制（DEC-029）。
//
// 与 RateLimit（RPM/RPD，按时间的请求速率）正交：本中间件数的是「同时进行中」的请求数
// （含整段流式传输），专门吸收「慢上游 + 客户端自动重试」的堆积——多余的并发重试立即 429
// 快速失败，不打上游、不冻结余额、不被上游计费。limiter 为 nil 或全局默认 key_limit=0 时恒放行。
func ConcurrencyLimit(limiter KeyConcurrencyLimiter) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limiter == nil {
				next.ServeHTTP(w, r)
				return
			}

			principal, ok := auth.APIKeyPrincipalFromContext(r.Context())
			if !ok {
				_ = httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "missing api key principal")
				return
			}

			// 线路必填（DB NOT NULL + 认证期 JOIN routes 保证），理论不可达的 nil 分支与
			// RateLimit 中间件一致地放行，避免对不可能状态硬失败。
			if principal.RouteID == nil {
				next.ServeHTTP(w, r)
				return
			}

			release, allowed := limiter.AcquireRouteUser(*principal.RouteID, principal.UserID)
			if !allowed {
				_ = httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many concurrent requests")
				return
			}
			defer release()

			next.ServeHTTP(w, r)
		})
	}
}
