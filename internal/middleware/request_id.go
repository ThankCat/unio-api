package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/ThankCat/unio-api/internal/httpx"
)

// RequestID 为每个请求补充请求 ID，并写入响应 header。
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get(httpx.HeaderRequestID)
		if requestID == "" {
			requestID = newRequestID()
		}

		w.Header().Set(httpx.HeaderRequestID, requestID)

		ctx := httpx.ContextWithRequestID(r.Context(), requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newRequestID() string {
	var b [16]byte

	if _, err := rand.Read(b[:16]); err != nil {
		return "unknown"
	}

	return hex.EncodeToString(b[:])
}
