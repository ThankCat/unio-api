package httpmw

import (
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
)

// ClientIP 提取客户端来源 IP（X-Forwarded-For / X-Real-IP / RemoteAddr）并存入 context，
// 供下游请求记录落库（仅展示/审计，不用于安全决策）。放在信任代理链之后使用。
func ClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := httpx.ExtractClientIP(r)
		ctx := httpx.ContextWithClientIP(r.Context(), ip)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
