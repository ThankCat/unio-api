package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/core/adminauth"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
)

// AdminAuthenticator 定义 middleware 调用 admin 认证所需的最小能力。
type AdminAuthenticator interface {
	AuthenticateAdmin(ctx context.Context, token string) (*adminauth.Principal, error)
}

// AdminAuth 校验 Bearer 会话 token，并把认证身份写入请求 context。
//
// 不向客户端透传内部 failure 细节，但区分两类结果：缺 token 与会话失效都是 401（客户端应重新登录），
// 会话存储故障是 503（依赖问题，重登也没用）。把后者伪装成 401 会让 Redis 抖动表现为
// 管理员被反复登出，掩盖真正的故障。
func AdminAuth(authenticator AdminAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := BearerToken(r.Header.Get("Authorization"))
			if token == "" {
				_ = httpx.WriteError(w, http.StatusUnauthorized, "adminauth_missing_token", "missing admin session token")
				return
			}

			principal, err := authenticator.AuthenticateAdmin(r.Context(), token)
			if err != nil {
				if failure.CodeOf(err) == failure.CodeAdminSessionStoreFailed {
					_ = httpx.WriteError(
						w, http.StatusServiceUnavailable,
						string(failure.CodeAdminSessionStoreFailed),
						"admin session store is unavailable",
					)
					return
				}

				_ = httpx.WriteError(w, http.StatusUnauthorized, "adminauth_session_expired", "admin session expired")
				return
			}

			ctx := adminauth.ContextWithPrincipal(r.Context(), principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// BearerToken 从 Authorization header 提取 Bearer token；格式不匹配时返回空字符串。
func BearerToken(header string) string {
	const prefix = "Bearer "

	if !strings.HasPrefix(header, prefix) {
		return ""
	}

	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}
