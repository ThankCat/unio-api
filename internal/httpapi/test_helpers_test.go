package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/ThankCat/unio-api/internal/auth"
)

var logger = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeAPIKeyAuthenticator 是测试用认证器，用来替代真实 API Key 认证服务。
type fakeAPIKeyAuthenticator struct {
	principal *auth.APIKeyPrincipal
	err       error
	token     string
}

// AuthenticateAPIKey 记录收到的明文 token，并返回测试预设的认证结果。
func (a *fakeAPIKeyAuthenticator) AuthenticateAPIKey(ctx context.Context, plaintext string) (*auth.APIKeyPrincipal, error) {
	a.token = plaintext
	return a.principal, a.err
}

// newSuccessfulAuthenticator 创建默认认证成功的 fake authenticator。
func newSuccessfulAuthenticator() *fakeAPIKeyAuthenticator {
	return &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
}

// newTestRouter 创建测试用 router，避免每个测试重复组装 RouterDeps。
func newTestRouter(authenticator *fakeAPIKeyAuthenticator) http.Handler {
	return NewRouter(RouterDeps{
		Logger:              logger,
		APIKeyAuthenticator: authenticator,
	})
}
