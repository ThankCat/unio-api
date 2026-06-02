package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/platform/observability/logfields"
)

// APIKeyAuthenticator 定义 middleware 调用认证服务所需的最小能力。
type APIKeyAuthenticator interface {
	AuthenticateAPIKey(rctx context.Context, plaintext string) (*auth.APIKeyPrincipal, error)
}

// APIKeyAuth 校验 Bearer API Key，并把认证身份写入请求 context。
func APIKeyAuth(authenticator APIKeyAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := apiKeyToken(r)
			if token == "" {
				_ = httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing api key")
				return
			}

			principal, err := authenticator.AuthenticateAPIKey(r.Context(), token)
			if err != nil {
				status := http.StatusUnauthorized
				code := "unauthorized"
				message := "invalid api key"

				if errors.Is(err, auth.ErrAPIKeyDisabled) || errors.Is(err, auth.ErrAPIKeyRevoked) {
					message = "api key disabled"
				}

				if errors.Is(err, auth.ErrAPIKeyExpired) {
					message = "api key expired"
				}

				_ = httpx.WriteError(w, status, code, message)
				return
			}

			ctx := auth.ContextWithAPIKeyPrincipal(r.Context(), principal)
			logfields.SetIdentity(ctx, principal.UserID, principal.ProjectID, principal.APIKeyID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// apiKeyToken 从 Anthropic x-api-key 或 OpenAI Bearer Authorization 提取客户 API key。
func apiKeyToken(r *http.Request) string {
	if token := strings.TrimSpace(r.Header.Get("x-api-key")); token != "" {
		return token
	}

	return bearerToken(r.Header.Get("Authorization"))
}

// bearerToken 从 Authorization header 中提取 Bearer token；格式不匹配时返回空字符串。
func bearerToken(header string) string {
	const prefix = "Bearer "

	if !strings.HasPrefix(header, prefix) {
		return ""
	}

	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}
