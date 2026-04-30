package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/ThankCat/unio-api/internal/auth"
	"github.com/ThankCat/unio-api/internal/httpx"
)

type APIKeyAuthenticator interface {
	AuthenticateAPIKey(rctx context.Context, plaintext string) (*auth.APIKeyPrincipal, error)
}

func APIKeyAuth(authenticator APIKeyAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r.Header.Get("Authorization"))
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
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func bearerToken(header string) string {
	const prefix = "Bearer "

	if !strings.HasPrefix(header, prefix) {
		return ""
	}

	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}
