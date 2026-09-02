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
		snippet := string(body)
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return RefreshResult{}, &RefreshRejectedError{StatusCode: resp.StatusCode, Body: snippet}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
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
