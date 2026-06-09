package adminauth

import "context"

// principalContextKey 是存放 admin 认证身份的私有 context key，与客户认证 context key 隔离。
type principalContextKey struct{}

// ContextWithPrincipal 返回带 admin 认证身份的新 context。
func ContextWithPrincipal(ctx context.Context, principal *Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFromContext 从 context 读取 admin 认证身份。
func PrincipalFromContext(ctx context.Context) (*Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(*Principal)
	return principal, ok
}
