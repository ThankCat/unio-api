package middleware

import (
	"context"
	"net/http"

	"go.uber.org/zap"

	gatewayanthropic "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/anthropic"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/logfields"
)

const (
	openAIInsufficientBalanceMessage    = "You exceeded your current quota. Please check your balance or billing details."
	anthropicInsufficientBalanceMessage = "Your credit balance is too low to access the API. Please go to Plans & Billing to upgrade or purchase credits."
)

// PositiveBalanceChecker 定义计费 Handler 前置余额门禁所需的最小能力。
type PositiveBalanceChecker interface {
	HasPositiveAvailableBalance(ctx context.Context, userID int64) (bool, error)
}

// PositiveBalanceMetrics 记录未进入持久请求生命周期的余额资格拒绝。
type PositiveBalanceMetrics interface {
	IncRequestRejected(protocol string, reason string)
}

// PositiveBalanceOptions 配置正余额门禁的公开错误协议和日志。
type PositiveBalanceOptions struct {
	Protocol RequestAdmissionProtocol
	Logger   *zap.Logger
	Metrics  PositiveBalanceMetrics
}

// PositiveBalanceGate 在计费 Handler 解码请求体前拒绝没有正可用余额的已认证用户。
func PositiveBalanceGate(checker PositiveBalanceChecker, opts PositiveBalanceOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if checker == nil {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := auth.APIKeyPrincipalFromContext(r.Context())
			if !ok || principal == nil {
				err := failure.New(
					failure.CodeAuthMissingAPIKey,
					failure.WithMessage("positive balance gate requires authenticated principal"),
				)
				logPositiveBalanceFailure(r.Context(), opts.Logger, r, nil, err)
				writePositiveBalanceUnavailable(w, r, opts.Protocol)
				return
			}

			positive, err := checker.HasPositiveAvailableBalance(r.Context(), principal.UserID)
			if err != nil {
				logPositiveBalanceFailure(r.Context(), opts.Logger, r, principal, err)
				writePositiveBalanceUnavailable(w, r, opts.Protocol)
				return
			}
			if !positive {
				if opts.Metrics != nil {
					opts.Metrics.IncRequestRejected(string(opts.Protocol), "insufficient_balance")
				}
				logfields.SetCompletion(r.Context(), "warning", string(failure.CodeLedgerInsufficientBalance))
				logging.Warn(opts.Logger, "billing", "balance_precheck", "billing balance precheck rejected",
					zap.String("trace_id", httpx.RequestID(r.Context())),
					zap.Int64("user_id", principal.UserID),
					zap.Int64("api_key_id", principal.APIKeyID),
					zap.String("method", r.Method),
					zap.String("path", r.URL.Path),
					zap.String("error_code", string(failure.CodeLedgerInsufficientBalance)),
				)
				writePositiveBalanceRejected(w, r, opts.Protocol)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func logPositiveBalanceFailure(ctx context.Context, logger *zap.Logger, r *http.Request, principal *auth.APIKeyPrincipal, err error) {
	code := failure.CodeOf(err)
	if code == "" {
		code = failure.CodeLedgerStoreFailed
	}
	logfields.SetCompletion(ctx, "error", string(code))
	fields := []zap.Field{
		zap.String("trace_id", httpx.RequestID(ctx)),
		zap.String("method", r.Method),
		zap.String("path", r.URL.Path),
	}
	if principal != nil {
		fields = append(fields,
			zap.Int64("user_id", principal.UserID),
			zap.Int64("api_key_id", principal.APIKeyID),
		)
	}
	fields = append(fields, failure.LogFields(err)...)
	logging.Error(logger, "billing", "balance_precheck", "billing balance precheck failed", fields...)
}

func writePositiveBalanceRejected(w http.ResponseWriter, r *http.Request, protocol RequestAdmissionProtocol) {
	if protocol == RequestAdmissionAnthropic {
		_ = httpx.WriteJSON(w, http.StatusPaymentRequired, gatewayanthropic.NewErrorResponse(
			"invalid_request_error",
			anthropicInsufficientBalanceMessage,
			httpx.RequestID(r.Context()),
		))
		return
	}

	_ = httpx.WriteOpenAIError(
		w,
		http.StatusPaymentRequired,
		"insufficient_quota",
		openAIInsufficientBalanceMessage,
		"insufficient_quota",
		nil,
	)
}

func writePositiveBalanceUnavailable(w http.ResponseWriter, r *http.Request, protocol RequestAdmissionProtocol) {
	if protocol == RequestAdmissionAnthropic {
		_ = httpx.WriteJSON(w, http.StatusServiceUnavailable, gatewayanthropic.NewErrorResponse(
			"api_error",
			"The service is temporarily unavailable.",
			httpx.RequestID(r.Context()),
		))
		return
	}

	_ = httpx.WriteOpenAIError(
		w,
		http.StatusServiceUnavailable,
		"service_unavailable",
		"The service is temporarily unavailable.",
		"api_error",
		nil,
	)
}
