package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/platform/ratelimit"
)

const (
	HeaderRateLimitLimit     = "X-RateLimit-Limit"
	HeaderRateLimitRemaining = "X-RateLimit-Remaining"
	HeaderRateLimitReset     = "X-RateLimit-Reset"
)

// RateLimitMetricsRecorder 定义限流中间件记录判定结果的能力。
// 由 internal/platform/observability/metrics 提供实现，这里只声明消费契约。
type RateLimitMetricsRecorder interface {
	IncRateLimitDecision(decision metrics.RateLimitDecision)
}

// RateLimitOptions 保存 RateLimit middleware 的运行参数。
// fail_open/fail_closed 策略已下沉到 Guard（按 RATE_LIMIT_FAILURE_POLICY 构造）：
// 故障 fail_open 时 Guard 返回放行且不报错；fail_closed 时返回错误，中间件据此回 500。
type RateLimitOptions struct {
	Logger  *slog.Logger
	Metrics RateLimitMetricsRecorder
}

// KeyRateLimiter 定义 middleware 在 ingress 执行 API Key 级请求限流（RPM/RPD）的能力。
type KeyRateLimiter interface {
	AllowKeyRequest(ctx context.Context, apiKeyID int64, limits ratelimit.Limits) (ratelimit.Decision, error)
}

// RateLimit 在 ingress 用认证身份对请求做 API Key 级 RPM/RPD 限流（P2-8）。
// 每把 Key 的上限取其自身配置，未配置则继承全局默认（由 Guard 解析）。TPM 与渠道级限流在调用上游前于 lifecycle 执行。
func RateLimit(limiter KeyRateLimiter, opts RateLimitOptions) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := auth.APIKeyPrincipalFromContext(r.Context())
			if !ok {
				_ = httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "missing api key principal")
				return
			}

			limits := ratelimit.Limits{
				RPM: principal.RPMLimit,
				TPM: principal.TPMLimit,
				RPD: principal.RPDLimit,
			}
			decision, err := limiter.AllowKeyRequest(r.Context(), principal.APIKeyID, limits)
			if err != nil {
				logRateLimitFailure(opts.Logger, principal.APIKeyID, principal.KeyPrefix, err)
				recordRateLimitDecision(opts.Metrics, metrics.RateLimitDecisionFailClosed)
				_ = httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "rate limit failed")
				return
			}

			writeRateLimitHeaders(w, decision)

			if !decision.Allowed {
				recordRateLimitDecision(opts.Metrics, metrics.RateLimitDecisionLimited)
				_ = httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded")
				return
			}

			recordRateLimitDecision(opts.Metrics, metrics.RateLimitDecisionAllowed)
			next.ServeHTTP(w, r)
		})
	}
}

// writeRateLimitHeaders 写入标准限流响应头；不限（Limit<=0）时不写，避免误导客户端。
func writeRateLimitHeaders(w http.ResponseWriter, decision ratelimit.Decision) {
	if decision.Limit <= 0 {
		return
	}
	w.Header().Set(HeaderRateLimitLimit, strconv.FormatInt(decision.Limit, 10))
	w.Header().Set(HeaderRateLimitRemaining, strconv.FormatInt(decision.Remaining, 10))
	w.Header().Set(HeaderRateLimitReset, strconv.FormatInt(decision.ResetAt.Unix(), 10))
}

// recordRateLimitDecision 在配置了 metrics recorder 时记录一次限流判定。
func recordRateLimitDecision(recorder RateLimitMetricsRecorder, decision metrics.RateLimitDecision) {
	if recorder == nil {
		return
	}

	recorder.IncRateLimitDecision(decision)
}

// logRateLimitFailure 记录限流计数后端故障；只记录 key prefix，不记录完整 API key。
func logRateLimitFailure(logger *slog.Logger, apiKeyID int64, keyPrefix string, err error) {
	if logger == nil {
		return
	}

	args := []any{
		"api_key_id", apiKeyID,
		"api_key_prefix", keyPrefix,
	}
	args = append(args, failure.LogArgs(err)...)

	logger.Warn("rate limit failed", args...)
}
