package auth

import (
	"context"
)

// principalContextKey 是存放 API Key 认证身份的私有 context key。
type principalContextKey struct{}

// ContextWithAPIKeyPrincipal 返回带认证身份的新 context。
func ContextWithAPIKeyPrincipal(ctx context.Context, principal *APIKeyPrincipal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// APIKeyPrincipalFromContext 从 context 读取认证身份。
func APIKeyPrincipalFromContext(ctx context.Context) (*APIKeyPrincipal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(*APIKeyPrincipal)
	return principal, ok
}
