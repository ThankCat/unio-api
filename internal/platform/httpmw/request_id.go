package httpmw

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/logfields"
)

const maxRequestIDLength = 128

var fallbackRequestIDSequence atomic.Uint64

// RequestID 为每个请求补充请求 ID，并写入响应 header。
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get(httpx.HeaderRequestID)
		if !isSafeRequestID(requestID) {
			requestID = newRequestID()
		}

		w.Header().Set(httpx.HeaderRequestID, requestID)

		ctx := httpx.ContextWithRequestID(r.Context(), requestID)
		// 在请求最外层安装结构化日志字段集合，trace_id 即采用并回传的 X-Request-ID；
		// 下游认证和 gateway 会向同一个 *Fields 填充 user/project/api_key/request_id/route。
		ctx, _ = logfields.NewContext(ctx, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// newRequestID 生成 16 字节随机请求 ID；随机源失败时仍生成进程内不重复的安全兜底值。
func newRequestID() string {
	var b [16]byte

	if _, err := rand.Read(b[:16]); err != nil {
		return fmt.Sprintf("fallback-%x-%x", time.Now().UnixNano(), fallbackRequestIDSequence.Add(1))
	}

	return hex.EncodeToString(b[:])
}

// isSafeRequestID 判断客户端传入的 trace id 是否适合进入响应头、日志和 context。
func isSafeRequestID(requestID string) bool {
	if requestID == "" || len(requestID) > maxRequestIDLength {
		return false
	}

	for _, c := range requestID {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-' || c == ':':
		default:
			return false
		}
	}

	return true
}
