package middleware

import (
	"context"
	"net/http"
	"time"

	"go.uber.org/zap"

	gatewayanthropic "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/anthropic"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
)

const defaultRequestAdmissionFinishTimeout = 2 * time.Second

// RequestAdmissionProtocol selects the public error envelope for ingress admission failures.
type RequestAdmissionProtocol string

const (
	RequestAdmissionOpenAI    RequestAdmissionProtocol = "openai"
	RequestAdmissionAnthropic RequestAdmissionProtocol = "anthropic"
)

// RequestAdmissionAcquirer is the route wrapper's protocol-independent admission dependency.
type RequestAdmissionAcquirer interface {
	Acquire(context.Context, requestadmission.Identity) (requestadmission.AcquireResult, error)
}

// RequestAdmissionOptions fixes the canonical route scope and public error protocol.
type RequestAdmissionOptions struct {
	Scope         string
	Protocol      RequestAdmissionProtocol
	Logger        *zap.Logger
	FinishTimeout time.Duration
}

// RequestAdmission acquires one ingress token after API key authentication, exposes only the
// usage capability to services, and uniquely finalizes after the handler has finished writing.
func RequestAdmission(acquirer RequestAdmissionAcquirer, opts RequestAdmissionOptions) func(http.Handler) http.Handler {
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.FinishTimeout <= 0 {
		opts.FinishTimeout = defaultRequestAdmissionFinishTimeout
	}

	return func(next http.Handler) http.Handler {
		if acquirer == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := auth.APIKeyPrincipalFromContext(r.Context())
			if !ok || principal == nil || principal.RouteID == nil {
				writeRequestAdmissionUnavailable(w, r, opts.Protocol)
				return
			}

			result, err := acquirer.Acquire(r.Context(), requestadmission.Identity{
				RouteID:          *principal.RouteID,
				UserID:           principal.UserID,
				Scope:            r.Method + " " + opts.Scope,
				RPMLimitOverride: principal.RPMLimit,
				TPMLimitOverride: principal.TPMLimit,
				RPDLimitOverride: principal.RPDLimit,
			})
			if err != nil {
				logRequestAdmissionFailure(opts.Logger, "acquire", err)
				writeRequestAdmissionUnavailable(w, r, opts.Protocol)
				return
			}
			if result.Outcome != breakerstore.RequestAllowed {
				if result.Outcome == breakerstore.RequestLimited {
					writeRequestAdmissionLimited(w, r, opts.Protocol)
					return
				}
				writeRequestAdmissionUnavailable(w, r, opts.Protocol)
				return
			}
			if result.Session == nil {
				writeRequestAdmissionUnavailable(w, r, opts.Protocol)
				return
			}

			defer func() {
				result.Session.StopRenewer()
				finishCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), opts.FinishTimeout)
				defer cancel()
				if err := result.Session.Finalize(finishCtx); err != nil {
					logRequestAdmissionFailure(opts.Logger, "finish", err)
				}
			}()

			ctx := requestadmission.ContextWithUsageSession(r.Context(), result.Session.Usage())
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func writeRequestAdmissionLimited(w http.ResponseWriter, r *http.Request, protocol RequestAdmissionProtocol) {
	if protocol == RequestAdmissionAnthropic {
		_ = httpx.WriteJSON(w, http.StatusTooManyRequests, gatewayanthropic.NewErrorResponse(
			"rate_limit_error",
			"You have exceeded the rate limit. Please slow down and retry later.",
			httpx.RequestID(r.Context()),
		))
		return
	}
	_ = httpx.WriteOpenAIError(
		w,
		http.StatusTooManyRequests,
		"rate_limit_exceeded",
		"You have exceeded the rate limit. Please slow down and retry later.",
		"rate_limit_error",
		nil,
	)
}

func writeRequestAdmissionUnavailable(w http.ResponseWriter, r *http.Request, protocol RequestAdmissionProtocol) {
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

func logRequestAdmissionFailure(logger *zap.Logger, operation string, err error) {
	fields := []zap.Field{zap.String("operation", operation)}
	fields = append(fields, failure.LogFields(err)...)
	logger.Warn("request admission operation failed", fields...)
}
