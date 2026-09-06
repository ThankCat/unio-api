package middleware

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
)

type clientIPContextKey struct{}

// ClientIPResolver 仅从可信代理链中提取客户端地址；实现由 httpx.TrustedClientIPResolver 提供，
// admin 登录限速与 console 审计共用同一套回溯规则。
type ClientIPResolver = httpx.TrustedClientIPResolver

// NewClientIPResolver 解析配置的可信代理 CIDR。
func NewClientIPResolver(cidrs []string) (*ClientIPResolver, error) {
	return httpx.NewTrustedClientIPResolver(cidrs)
}

// ClientIP 将解析后的客户端地址写入请求上下文。
func ClientIP(resolver *ClientIPResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), clientIPContextKey{}, resolver.Resolve(r))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClientIPFromContext 返回可信客户端地址；无法识别时返回 "unknown"。
func ClientIPFromContext(ctx context.Context) string {
	if value, ok := ctx.Value(clientIPContextKey{}).(string); ok && value != "" {
		return value
	}
	return "unknown"
}
