package auth

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	serviceauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"
)

type principalContextKey struct{}

// ContextWithPrincipal 将已认证主体写入请求上下文。
func ContextWithPrincipal(ctx context.Context, principal serviceauth.Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFromContext 读取 RequireAuth 写入的已认证主体。
func PrincipalFromContext(ctx context.Context) (serviceauth.Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(serviceauth.Principal)
	return principal, ok && principal.UserID > 0
}

// RequireAuth 要求浏览器携带有效的访问令牌 Cookie，并把内部用户主键放入上下文。
func RequireAuth(service Service, errorWriter transport.ErrorWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(accessCookieName)
			if err != nil || cookie.Value == "" {
				errorWriter.Write(w, &consoleservice.Error{
					Code:    serviceauth.CodeSessionInvalid,
					Message: "The current session is invalid.",
					Status:  http.StatusUnauthorized,
				})
				return
			}
			principal, authErr := service.AuthenticatePrincipal(r.Context(), cookie.Value)
			if authErr != nil {
				errorWriter.Write(w, authErr)
				return
			}
			next.ServeHTTP(w, r.WithContext(ContextWithPrincipal(r.Context(), principal)))
		})
	}
}
