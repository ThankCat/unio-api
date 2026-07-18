package httpmw

import (
	"net/http"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
)

// Recoverer 捕获 handler 中的 panic，记录错误日志，并返回统一的 500 JSON 响应。
func Recoverer(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				err := recover()
				if err == nil {
					return
				}

				logger.Error(
					"panic recovered",
					zap.Any("error", err),
					zap.String("method", r.Method),
					zap.String("path", r.URL.Path),
					zap.String("request_id", httpx.RequestID(r.Context())),
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
