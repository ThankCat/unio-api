package middleware

import (
	"net/http"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
)

// Recoverer converts panics into the Console-specific error envelope.
func Recoverer(logger *zap.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				panicValue := recover()
				if panicValue == nil {
					return
				}
				logger.Error(
					"console request panic recovered",
					zap.Any("error", panicValue),
					zap.String("method", r.Method),
					zap.String("path", r.URL.Path),
					zap.String("request_id", httpx.RequestID(r.Context())),
				)
					_ = httpx.WriteConsoleError(
						w,
						http.StatusInternalServerError,
						"request_failed",
						"The request could not be completed.",
					nil,
				)
			}()
			next.ServeHTTP(w, r)
		})
	}
}
