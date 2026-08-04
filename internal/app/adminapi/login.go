package adminapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/middleware"
	"github.com/ThankCat/unio-gateway/internal/core/adminauth"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
)

// CredentialAuthenticator 定义登录入口校验用户名口令所需的最小能力。
type CredentialAuthenticator interface {
	AuthenticateCredentials(ctx context.Context, username, password string) (*adminauth.Principal, error)
}

// SessionIssuer 定义登录签发与登出吊销会话所需的最小能力。
type SessionIssuer interface {
	Issue(ctx context.Context) (string, error)
	Revoke(ctx context.Context, token string) error
}

// loginRequest 是登录入口接受的请求体。
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginResponse 返回后续请求使用的会话 token 及其有效秒数。
type loginResponse struct {
	Token     string `json:"token"`
	ExpiresIn int64  `json:"expires_in"`
}

// handleLogin 校验固定用户名与口令，通过后现场签发随机会话 token。
//
// 这是 admin 表面唯一不需要 token 的端点：没有它就无法取得 token。除此之外它不放宽任何东西——
// 凭据校验失败一律回 401 与同一句文案，不区分用户名错、口令错或缺字段，避免枚举出有效用户名。
//
// 会话存储故障与凭据错误分开渲染：前者是依赖故障（503），把它伪装成 401 会让管理员
// 反复尝试登录而看不到真正原因。
func handleLogin(authenticator CredentialAuthenticator, sessions SessionIssuer, ttlSeconds int64) http.HandlerFunc {
	const invalidMessage = "用户名或密码错误"

	return func(w http.ResponseWriter, r *http.Request) {
		var body loginRequest
		if err := httpx.DecodeJSON(w, r, &body); err != nil {
			adminhttp.WriteServiceError(w, err)
			return
		}

		username := strings.TrimSpace(body.Username)
		if _, err := authenticator.AuthenticateCredentials(r.Context(), username, body.Password); err != nil {
			_ = httpx.WriteError(w, http.StatusUnauthorized, "adminauth_invalid_credentials", invalidMessage)
			return
		}

		token, err := sessions.Issue(r.Context())
		if err != nil {
			adminhttp.WriteServiceError(w, err)
			return
		}

		_ = httpx.WriteJSON(w, http.StatusOK, loginResponse{Token: token, ExpiresIn: ttlSeconds})
	}
}

// handleLogout 立即吊销当前会话。
//
// 挂在受保护分组内，因此到达这里时 token 必然有效；吊销后同一 token 不可再用。
// 重复登出按幂等处理：会话已不存在也回 204。
func handleLogout(sessions SessionIssuer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := sessions.Revoke(r.Context(), middleware.BearerToken(r.Header.Get("Authorization"))); err != nil {
			adminhttp.WriteServiceError(w, err)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
