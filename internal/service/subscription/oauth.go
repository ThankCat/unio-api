package subscription

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/url"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// OAuth PKCE 导入（第六节）：生成授权链接 → 管理员在浏览器完成登录 → 回填 code →
// 常量时间校验 state → 换令牌（走账号绑定代理）→ 解析 id_token 得邮箱/账号 ID/套餐 →
// 落库 disabled，显式启用。不做指纹伪装类 enrichment。

const (
	// authorizeEndpoint 是 OpenAI OAuth 授权端点（与 Codex CLI 同一 client）。
	authorizeEndpoint = "https://auth.openai.com/oauth/authorize"
	// DefaultRedirectURI 是 Codex CLI 注册的回调地址。code 由管理员从浏览器地址栏复制回填，
	// 不要求该端口真的有监听——这与 Sub2API 的导入向导同一交互形态。
	DefaultRedirectURI = "http://localhost:1455/auth/callback"
	oauthScopes        = "openid profile email offline_access"
)

// PKCEChallenge 是一次授权会话的临时秘密。Verifier/State 只在服务端会话里保存，
// 不落库、不写日志。
type PKCEChallenge struct {
	Verifier string
	State    string
}

// NewPKCEChallenge 生成 PKCE verifier 与 state（各 32 字节随机、URL-safe）。
func NewPKCEChallenge() (PKCEChallenge, error) {
	verifier, err := randomToken(32)
	if err != nil {
		return PKCEChallenge{}, err
	}
	state, err := randomToken(32)
	if err != nil {
		return PKCEChallenge{}, err
	}
	return PKCEChallenge{Verifier: verifier, State: state}, nil
}

func randomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", failure.Wrap(failure.CodeConfigInvalid, err, failure.WithMessage("generate pkce token"))
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// AuthorizationURL 构造授权链接（S256 code challenge，Codex CLI 同款参数）。
func (p PKCEChallenge) AuthorizationURL(redirectURI string) string {
	if redirectURI == "" {
		redirectURI = DefaultRedirectURI
	}
	sum := sha256.Sum256([]byte(p.Verifier))
	query := url.Values{
		"response_type":              {"code"},
		"client_id":                  {defaultClientID},
		"redirect_uri":               {redirectURI},
		"scope":                      {oauthScopes},
		"state":                      {p.State},
		"code_challenge":             {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
	}
	return authorizeEndpoint + "?" + query.Encode()
}

// VerifyState 常量时间比较回调 state，防时序侧信道与 CSRF。
func (p PKCEChallenge) VerifyState(state string) bool {
	return subtle.ConstantTimeCompare([]byte(p.State), []byte(state)) == 1
}

// CompleteAuthorization 用回填的 code 换令牌并解析账号身份，产出可直接落库的导入条目。
// proxyURL 是该账号将绑定的出口：换码就从这个出口发（三条路径统一走账号代理）。
func CompleteAuthorization(
	ctx context.Context,
	tokens *TokenClient,
	challenge PKCEChallenge,
	code, state, redirectURI, proxyURL string,
) (ImportAccount, error) {
	if !challenge.VerifyState(state) {
		return ImportAccount{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("oauth state mismatch"),
		)
	}
	if redirectURI == "" {
		redirectURI = DefaultRedirectURI
	}
	result, err := tokens.ExchangeCode(ctx, code, challenge.Verifier, redirectURI, proxyURL)
	if err != nil {
		return ImportAccount{}, err
	}
	creds := Credentials{}.MergeRefreshed(result, time.Now())
	creds.ClientID = defaultClientID
	identity := ParseIdentity(creds)
	if identity.ChatGPTAccountID == "" {
		return ImportAccount{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("oauth tokens carry no chatgpt_account_id claim"),
		)
	}
	creds.Email = identity.Email
	display := identity.Email
	if display == "" {
		display = identity.ChatGPTAccountID
	}
	return ImportAccount{
		Platform:          "openai",
		UpstreamAccountID: identity.ChatGPTAccountID,
		DisplayName:       display,
		PlanType:          identity.PlanType,
		Credentials:       creds,
		ProxyURL:          proxyURL,
		Priority:          50,
		SubscriptionUntil: identity.SubscriptionUntil,
	}, nil
}
