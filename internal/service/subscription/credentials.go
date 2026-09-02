// Package subscription 编排订阅账号的凭据生命周期（第六节）：
// OAuth PKCE 导入、批量文件导入、令牌后台保活与请求时兜底刷新、出站凭据解析。
//
// 凭据存储沿用渠道凭据的明文口径（边界 22）；credentials 列是本包唯一读写的凭据文档。
package subscription

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// Credentials 是 subscription_accounts.credentials 列的规范文档形态。
//
// 导入来源（OAuth 回调 / sub2api 文件）各有杂字段，落库前一律归一到本形态；
// 读侧只认这些字段，未知字段在再编码时丢弃（单一 schema，避免文档漂移）。
type Credentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	// ExpiresAt 是 access token 的过期时刻（RFC3339）。与订阅到期是两回事。
	ExpiresAt time.Time `json:"expires_at"`
	// ClientID 是签发该令牌的 OAuth client；刷新时必须回传同一个。
	ClientID string `json:"client_id,omitempty"`
	Email    string `json:"email,omitempty"`
}

// DecodeCredentials 解析凭据文档；access_token 缺失视为凭据损坏。
func DecodeCredentials(raw []byte) (Credentials, error) {
	var creds Credentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return Credentials{}, failure.Wrap(
			failure.CodeConfigInvalid, err,
			failure.WithMessage("decode subscription account credentials"),
		)
	}
	if strings.TrimSpace(creds.AccessToken) == "" {
		return Credentials{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("subscription account credentials missing access_token"),
		)
	}
	return creds, nil
}

// Encode 序列化为规范文档。
func (c Credentials) Encode() ([]byte, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeConfigInvalid, err,
			failure.WithMessage("encode subscription account credentials"),
		)
	}
	return raw, nil
}

// FreshFor 报告 access token 在 skew 之后是否仍然有效。
// ExpiresAt 缺失（零值）时保守视为不新鲜，逼出一次刷新或明确的失败。
func (c Credentials) FreshFor(skew time.Duration, now time.Time) bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return now.Add(skew).Before(c.ExpiresAt)
}

// MergeRefreshed 应用一次刷新结果：新 refresh token 非空才覆盖旧值（第六节硬规则——
// 上游可能不回 refresh token，用空值冲掉会让账号从此无法续命）。
func (c Credentials) MergeRefreshed(result RefreshResult, now time.Time) Credentials {
	next := c
	next.AccessToken = result.AccessToken
	if strings.TrimSpace(result.RefreshToken) != "" {
		next.RefreshToken = result.RefreshToken
	}
	if strings.TrimSpace(result.IDToken) != "" {
		next.IDToken = result.IDToken
	}
	switch {
	case result.ExpiresAt != nil:
		next.ExpiresAt = *result.ExpiresAt
	case result.ExpiresInSeconds > 0:
		next.ExpiresAt = now.Add(time.Duration(result.ExpiresInSeconds) * time.Second)
	default:
		// 上游没给有效期：从 JWT exp 声明兜底（Codex access token 是 RS256 JWT）。
		if exp, ok := jwtExpiry(result.AccessToken); ok {
			next.ExpiresAt = exp
		}
	}
	return next
}

// jwtExpiry 从 JWT 的 exp 声明解出过期时刻（不校验签名——我们是令牌持有方，不是验证方）。
func jwtExpiry(token string) (time.Time, bool) {
	claims, ok := JWTClaims(token)
	if !ok {
		return time.Time{}, false
	}
	exp, ok := claims["exp"].(float64)
	if !ok || exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(exp), 0).UTC(), true
}

// JWTClaims 解出 JWT payload 声明（不校验签名）。非 JWT 返回 ok=false。
func JWTClaims(token string) (map[string]any, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, false
	}
	return claims, true
}

// IdentityClaims 是从 id_token / access token 声明解出的账号身份（导入与重授权共用）。
type IdentityClaims struct {
	Email             string
	ChatGPTAccountID  string
	PlanType          string
	SubscriptionUntil time.Time
}

// ParseIdentity 依次从 id_token 与 access token 的 https://api.openai.com/auth 声明解身份。
func ParseIdentity(creds Credentials) IdentityClaims {
	identity := IdentityClaims{Email: creds.Email}
	for _, token := range []string{creds.IDToken, creds.AccessToken} {
		claims, ok := JWTClaims(token)
		if !ok {
			continue
		}
		if email, ok := claims["email"].(string); ok && identity.Email == "" {
			identity.Email = email
		}
		auth, ok := claims["https://api.openai.com/auth"].(map[string]any)
		if !ok {
			continue
		}
		if v, ok := auth["chatgpt_account_id"].(string); ok && identity.ChatGPTAccountID == "" {
			identity.ChatGPTAccountID = v
		}
		if v, ok := auth["chatgpt_plan_type"].(string); ok && identity.PlanType == "" {
			identity.PlanType = v
		}
		if v, ok := auth["chatgpt_subscription_active_until"].(string); ok && identity.SubscriptionUntil.IsZero() {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				identity.SubscriptionUntil = t
			}
		}
	}
	return identity
}
