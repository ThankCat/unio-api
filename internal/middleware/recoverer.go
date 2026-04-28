package middleware

import (
	"log/slog"
	"net/http"

	"github.com/ThankCat/unio-api/internal/httpx"
)

func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				err := recover()
				if err == nil {
					return
				}

				logger.ErrorContext(
					r.Context(),
					"panic recovered",
					"error", err,
					"method", r.Method,
					"path", r.URL.Path,
					"request_id", httpx.RequestID(r.Context()),
				)

				_ = httpx.WriteError(
					w,
					http.StatusInternalServerError,
					"internal_error",
					"internal server error",
				)
			}()
			h.ServeHTTP(w, r)
		})
	}
}
