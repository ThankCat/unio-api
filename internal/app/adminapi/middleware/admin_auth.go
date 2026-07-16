package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/core/adminauth"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
)

// AdminAuthenticator 定义 middleware 调用 admin 认证所需的最小能力。
type AdminAuthenticator interface {
	AuthenticateAdmin(ctx context.Context, token string) (*adminauth.Principal, error)
}

// AdminAuth 校验 Bearer admin token，并把认证身份写入请求 context。
//
// 不向客户端透传内部 failure 细节：缺 token 与 token 不匹配都映射为 401。
func AdminAuth(authenticator AdminAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r.Header.Get("Authorization"))
			if token == "" {
				_ = httpx.WriteError(w, http.StatusUnauthorized, "adminauth_missing_token", "missing admin token")
				return
			}

			principal, err := authenticator.AuthenticateAdmin(r.Context(), token)
			if err != nil {
				_ = httpx.WriteError(w, http.StatusUnauthorized, "adminauth_invalid_token", "invalid admin token")
				return
			}

			ctx := adminauth.ContextWithPrincipal(r.Context(), principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// bearerToken 从 Authorization header 提取 Bearer token；格式不匹配时返回空字符串。
func bearerToken(header string) string {
	const prefix = "Bearer "

	if !strings.HasPrefix(header, prefix) {
		return ""
	}

	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}
