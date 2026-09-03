package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const (
	// tokenEndpoint 是 OpenAI OAuth 令牌端点（换码与刷新共用，与 Codex CLI 一致）。
	tokenEndpoint = "https://auth.openai.com/oauth/token"
	// refreshScopes 与 Codex CLI / 沙箱 token.py 一致。
	refreshScopes = "openid profile email"
	// defaultClientID 是 Codex CLI 的 OAuth client；凭据文档缺 client_id 时兜底
	//（access token 的 client_id 声明优先）。
	defaultClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// refreshUserAgent 与出站指纹同源（按账号收敛，边界 26）。
	refreshUserAgent = "codex_cli_rs/0.152.1 (Mac OS 15.2.0; arm64) unio"
)

// RefreshResult 是一次令牌刷新/换码的上游响应。
type RefreshResult struct {
	AccessToken      string     `json:"access_token"`
	RefreshToken     string     `json:"refresh_token"`
	IDToken          string     `json:"id_token"`
	ExpiresInSeconds int64      `json:"expires_in"`
	ExpiresAt        *time.Time `json:"-"`
}

// ErrRefreshRejected 表示上游明确拒绝刷新（400/401：refresh token 已吊销或失效）。
// 调用方据此走「确认吊销」路径（禁用账号），与网络故障（重试/退避）严格区分。
type RefreshRejectedError struct {
	StatusCode int
	Body       string
}

func (e *RefreshRejectedError) Error() string {
	return fmt.Sprintf("oauth token refresh rejected: status %d", e.StatusCode)
}

// TokenClient 执行 OAuth 令牌端点调用；HTTP client 按账号代理解析（三条路径统一走账号代理）。
type TokenClient struct {
	clientFor func(proxyURL string) *http.Client
}

// NewTokenClient 创建令牌客户端。clientFor 为 nil 时全部直连。
func NewTokenClient(clientFor func(proxyURL string) *http.Client) *TokenClient {
	if clientFor == nil {
		clientFor = func(string) *http.Client { return http.DefaultClient }
	}
	return &TokenClient{clientFor: clientFor}
}

// Refresh 用 refresh token 换新 access token（沙箱 token.py 已验证的流程）。
func (t *TokenClient) Refresh(ctx context.Context, creds Credentials, proxyURL string) (RefreshResult, error) {
	refreshToken := strings.TrimSpace(creds.RefreshToken)
	if refreshToken == "" {
		return RefreshResult{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("subscription account has no refresh token"),
		)
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {t.clientID(creds)},
		"scope":         {refreshScopes},
	}
	return t.postTokenForm(ctx, form, proxyURL)
}

// ExchangeCode 用授权码换令牌（PKCE 流程的最后一步；换码请求走账号绑定代理）。
func (t *TokenClient) ExchangeCode(ctx context.Context, code, verifier, redirectURI, proxyURL string) (RefreshResult, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
		"client_id":     {defaultClientID},
	}
	return t.postTokenForm(ctx, form, proxyURL)
}

func (t *TokenClient) clientID(creds Credentials) string {
	if claims, ok := JWTClaims(creds.AccessToken); ok {
		if v, ok := claims["client_id"].(string); ok && v != "" {
			return v
		}
	}
	if creds.ClientID != "" {
		return creds.ClientID
	}
	return defaultClientID
}

func (t *TokenClient) postTokenForm(ctx context.Context, form url.Values, proxyURL string) (RefreshResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return RefreshResult{}, failure.Wrap(failure.CodeConfigInvalid, err, failure.WithMessage("create oauth token request"))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", refreshUserAgent)
	req.Header.Set("originator", "codex_cli_rs")

	resp, err := t.clientFor(proxyURL).Do(req)
	if err != nil {
		return RefreshResult{}, failure.Wrap(
			failure.CodeAdapterSendRequestFailed, err,
			failure.WithMessage("oauth token endpoint unreachable"),
		)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// 明确拒绝：refresh token 吊销/失效。正文不含令牌，仅截前 300 字节供排障。
		return RefreshResult{}, &RefreshRejectedError{StatusCode: resp.StatusCode, Body: truncateRefreshBody(body)}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// 非常规状态码（5xx/3xx 等）但正文携带明确的终局错误码：同样按确认拒绝处理。
		// 与 sub2api 的不可重试清单对齐——只按状态码分类会把「上游 5xx 包着 invalid_grant」
		// 误判成网络问题，让保活对死令牌反复退避重试（白打上游 + 拖慢重授权提示）。
		if refreshBodyIsTerminal(body) {
			return RefreshResult{}, &RefreshRejectedError{StatusCode: resp.StatusCode, Body: truncateRefreshBody(body)}
		}
		return RefreshResult{}, failure.New(
			failure.CodeAdapterSendRequestFailed,
			failure.WithMessage(fmt.Sprintf("oauth token endpoint status %d", resp.StatusCode)),
		)
	}

	var result RefreshResult
	if err := json.Unmarshal(body, &result); err != nil || strings.TrimSpace(result.AccessToken) == "" {
		return RefreshResult{}, failure.New(
			failure.CodeAdapterSendRequestFailed,
			failure.WithMessage("oauth token endpoint returned no access_token"),
		)
	}
	return result, nil
}

// terminalRefreshErrorCodes 是 OAuth 刷新的终局错误码清单（与 sub2api 生产清单对齐，
// 含我们实测拿到过的 refresh_token_invalidated）。命中即「确认吊销/不可恢复」，
// 重试无意义，只能重新授权。
var terminalRefreshErrorCodes = []string{
	"invalid_grant",             // refresh_token 已失效
	"invalid_refresh_token",     // refresh_token 无效（team 工作区被删）
	"refresh_token_reused",      // rt 已被使用（旧副本在别处刷过，会话被顶）
	"refresh_token_invalidated", // 会话结束，rt 作废（实测样本）
	"token_expired",             // rt 本身过期
	"app_session_terminated",    // team 工作区被删
	"invalid_client",            // 客户端配置错误
	"unauthorized_client",       // 客户端未授权
	"access_denied",             // 访问被拒绝
}

// refreshBodyIsTerminal 判断令牌端点响应体是否携带终局错误码。
func refreshBodyIsTerminal(body []byte) bool {
	lowered := strings.ToLower(string(body))
	for _, code := range terminalRefreshErrorCodes {
		if strings.Contains(lowered, code) {
			return true
		}
	}
	return false
}

func truncateRefreshBody(body []byte) string {
	snippet := string(body)
	if len(snippet) > 300 {
		snippet = snippet[:300]
	}
	return snippet
}
