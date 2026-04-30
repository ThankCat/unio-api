这一课：**API Key HTTP Middleware**。

目标是把你刚写的 `auth.APIKeyAuthenticator` 接到 HTTP 链路里。

请求流程会变成：

```text
HTTP request
-> RequestID
-> Logger
-> Recoverer
-> APIKeyAuth middleware
-> handler
```

这节先不保护 `/healthz`，后面保护 `/v1/*`。

**目标**

新增一个 middleware：

```text
internal/middleware/api_key_auth.go
```

它负责：

```text
1. 读取 Authorization header
2. 解析 Bearer token
3. 调用 auth.AuthenticateAPIKey
4. 认证成功后把 principal 放进 context
5. 认证失败返回 JSON error
```

**第一步：先做 context helper**

建议放在：

```text
internal/auth/context.go
```

内容：

```go
package auth

import "context"

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
```

为什么放 `auth` 包？

因为 `APIKeyPrincipal` 是 auth 领域的概念。middleware 只负责把它放进 request context。

**第二步：定义 middleware 依赖接口**

`internal/middleware/api_key_auth.go`：

```go
package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ThankCat/unio-api/internal/auth"
	"github.com/ThankCat/unio-api/internal/httpx"
)

type APIKeyAuthenticator interface {
	AuthenticateAPIKey(rctx context.Context, plaintext string) (*auth.APIKeyPrincipal, error)
}
```

这里要 import `context`。

**第三步：写 Authorization 解析**

建议单独写一个小函数，方便测试：

```go
func bearerToken(header string) string {
	const prefix = "Bearer "

	if !strings.HasPrefix(header, prefix) {
		return ""
	}

	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}
```

先只支持标准写法：

```http
Authorization: Bearer unio_sk_xxx
```

注意：`Bearer` 大小写是否兼容后面再讨论，MVP 先严格点。

**第四步：写 middleware**

```go
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
```

**为什么 HTTP 层不暴露所有错误**

service 里有：

```text
ErrMissingAPIKey
ErrInvalidAPIKey
ErrAPIKeyRevoked
ErrAPIKeyDisabled
ErrAPIKeyExpired
```

但 HTTP 响应不一定要全部告诉用户。比如：

```text
invalid api key
revoked api key
不存在的 api key
```

都可以统一成 `401 unauthorized`，避免泄露太多认证状态。

**第五步：测试**

新增：

```text
internal/middleware/api_key_auth_test.go
```

测试 fake authenticator：

```go
type fakeAPIKeyAuthenticator struct {
	principal *auth.APIKeyPrincipal
	err       error
	token     string
}

func (a *fakeAPIKeyAuthenticator) AuthenticateAPIKey(ctx context.Context, plaintext string) (*auth.APIKeyPrincipal, error) {
	a.token = plaintext
	return a.principal, a.err
}
```

测试场景：

```text
missing Authorization -> 401
invalid Bearer format -> 401
authenticator error -> 401
valid token -> next handler 被调用
valid token -> principal 能从 context 取到
```

**注意**

这节先只写 middleware 和测试。不要急着接到 `NewRouter`，因为当前 `NewRouter(logger)` 还没有 auth service 依赖。接路由时我们要重构 router 构造函数，下一课再做。

你先写：

```text
internal/auth/context.go
internal/middleware/api_key_auth.go
internal/middleware/api_key_auth_test.go
```

跑：

```bash
go test ./internal/auth ./internal/middleware -v
go test ./...
```

写完后我帮你 review。