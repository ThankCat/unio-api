package httpmw

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
)

// RequireBearer 用固定 token 保护运维端点（/metrics 等）：Authorization 必须为 `Bearer <token>`，
// 常数时间比较，失败回 401 且不泄露期望长度以外的任何信息。
//
// token 为空时原样返回 next（不加防护），由调用方决定是否允许未配置 token 的部署直接暴露端点；
// 这是纵深防御——第一道仍是反代对 /metrics 的屏蔽（deploy/nginx），这里只保证容器端口
// 被直接暴露或反代漏配时指标不会对外可读。
func RequireBearer(token string, next http.Handler) http.Handler {
	if token == "" || next == nil {
		return next
	}
	expected := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if provided == r.Header.Get("Authorization") || len(provided) != len(expected) ||
			subtle.ConstantTimeCompare([]byte(provided), expected) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="internal"`)
			_ = httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid internal token")
			return
		}
		next.ServeHTTP(w, r)
	})
}
