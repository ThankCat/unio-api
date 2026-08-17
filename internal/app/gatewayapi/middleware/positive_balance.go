package middleware

import (
	"context"
	"net/http"

	"go.uber.org/zap"

	gatewayanthropic "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/anthropic"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/ledger"
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
	GetBalanceEligibility(ctx context.Context, userID int64) (ledger.BalanceEligibility, error)
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

// PositiveBalanceGate 在计费 Handler 解码请求体前拒绝所有币种账面余额都已耗尽的已认证用户。
// 暂时冻结必须放行，由目标计费币种明确后的原子授权准确区分 402 与 429。
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

			eligibility, err := checker.GetBalanceEligibility(r.Context(), principal.UserID)
			if err != nil {
				logPositiveBalanceFailure(r.Context(), opts.Logger, r, principal, err)
				writePositiveBalanceUnavailable(w, r, opts.Protocol)
				return
			}

			switch eligibility {
			case ledger.BalanceEligibilityPositiveAvailable,
				ledger.BalanceEligibilityTemporarilyReserved:
				next.ServeHTTP(w, r)
				return
			case ledger.BalanceEligibilityInsufficient:
				if opts.Metrics != nil {
					opts.Metrics.IncRequestRejected(string(opts.Protocol), "insufficient_balance")
				}
				logPositiveBalanceRejection(r.Context(), opts.Logger, r, principal, failure.CodeLedgerInsufficientBalance)
				writePositiveBalanceRejected(w, r, opts.Protocol)
				return
			default:
				err := failure.New(
					failure.CodeLedgerStoreFailed,
					failure.WithMessage("unknown balance eligibility"),
					failure.WithField("balance_eligibility", string(eligibility)),
				)
				logPositiveBalanceFailure(r.Context(), opts.Logger, r, principal, err)
				writePositiveBalanceUnavailable(w, r, opts.Protocol)
				return
			}
		})
	}
}

func logPositiveBalanceRejection(ctx context.Context, logger *zap.Logger, r *http.Request, principal *auth.APIKeyPrincipal, code failure.Code) {
	logfields.SetCompletion(ctx, "warning", string(code))
	logging.Warn(logger, "billing", "balance_precheck", "billing balance precheck rejected",
		zap.String("trace_id", httpx.RequestID(ctx)),
		zap.Int64("user_id", principal.UserID),
		zap.Int64("api_key_id", principal.APIKeyID),
		zap.String("method", r.Method),
		zap.String("path", r.URL.Path),
		zap.String("error_code", string(code)),
	)
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
