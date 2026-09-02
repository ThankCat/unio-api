package subscription

import (
	"strings"
	"testing"
	"time"
)

// 样例与上传的真实导出文件同构（令牌换成短占位符，claims 结构一致的假 JWT）。
func sub2apiSample(t *testing.T) []byte {
	t.Helper()
	return []byte(`{
  "type": "sub2api-data",
  "version": 1,
  "proxies": [{"key": "p1", "url": "http://127.0.0.1:7890"}],
  "accounts": [
    {
      "name": "user@example.com",
      "type": "oauth",
      "platform": "openai",
      "priority": 50,
      "concurrency": 3,
      "proxy_key": "p1",
      "credentials": {
        "email": "user@example.com",
        "access_token": "at-opaque",
        "refresh_token": "rt.1.abc",
        "client_id": "app_EMoamEEZ73f0CkXaXp7hrann",
        "plan_type": "plus",
        "account_id": "3581a2ce-8aa3-4841-8b18-a71b456e9033",
        "expires_at": "2026-09-12T02:57:14Z",
        "subscription_expires_at": "2026-10-02T02:44:59Z"
      }
    }
  ]
}`)
}

func TestParseSub2APIDataNormalizesAccounts(t *testing.T) {
	accounts, err := ParseSub2APIData(sub2apiSample(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(accounts))
	}
	account := accounts[0]
	if account.UpstreamAccountID != "3581a2ce-8aa3-4841-8b18-a71b456e9033" {
		t.Fatalf("upstream id = %q", account.UpstreamAccountID)
	}
	if account.PlanType != "plus" || account.DisplayName != "user@example.com" {
		t.Fatalf("plan/display = %q/%q", account.PlanType, account.DisplayName)
	}
	if account.ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("proxy url = %q (proxy_key must map through proxies[])", account.ProxyURL)
	}
	if account.Concurrency == nil || *account.Concurrency != 3 {
		t.Fatalf("concurrency = %v, want 3", account.Concurrency)
	}
	if account.Credentials.RefreshToken != "rt.1.abc" || account.Credentials.ExpiresAt.IsZero() {
		t.Fatalf("credentials not normalized: %+v", account.Credentials)
	}
	if account.SubscriptionUntil.Format(time.RFC3339) != "2026-10-02T02:44:59Z" {
		t.Fatalf("subscription until = %v", account.SubscriptionUntil)
	}
}

func TestParseSub2APIDataRejectsUnknownFormat(t *testing.T) {
	if _, err := ParseSub2APIData([]byte(`{"type":"other","version":2,"accounts":[]}`)); err == nil {
		t.Fatal("unknown format must be rejected")
	}
}

// 新 refresh token 非空才覆盖旧值：上游不回 refresh token 时用空值冲掉会让账号从此无法续命。
func TestMergeRefreshedKeepsOldRefreshTokenWhenAbsent(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	old := Credentials{AccessToken: "old-at", RefreshToken: "old-rt", ExpiresAt: now}

	merged := old.MergeRefreshed(RefreshResult{AccessToken: "new-at", ExpiresInSeconds: 3600}, now)
	if merged.AccessToken != "new-at" {
		t.Fatalf("access token = %q", merged.AccessToken)
	}
	if merged.RefreshToken != "old-rt" {
		t.Fatalf("empty refresh token must not overwrite the old one, got %q", merged.RefreshToken)
	}
	if !merged.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("expires at = %v", merged.ExpiresAt)
	}

	rotated := old.MergeRefreshed(RefreshResult{AccessToken: "new-at", RefreshToken: "new-rt"}, now)
	if rotated.RefreshToken != "new-rt" {
		t.Fatalf("non-empty refresh token must overwrite, got %q", rotated.RefreshToken)
	}
}

func TestFreshForTreatsMissingExpiryAsStale(t *testing.T) {
	now := time.Now()
	if (Credentials{AccessToken: "at"}).FreshFor(time.Minute, now) {
		t.Fatal("missing expires_at must be treated as stale")
	}
	fresh := Credentials{AccessToken: "at", ExpiresAt: now.Add(time.Hour)}
	if !fresh.FreshFor(5*time.Minute, now) {
		t.Fatal("token valid for an hour must be fresh with a 5m skew")
	}
	if fresh.FreshFor(2*time.Hour, now) {
		t.Fatal("skew larger than validity must report stale")
	}
}

func TestAuthorizationURLCarriesPKCE(t *testing.T) {
	challenge, err := NewPKCEChallenge()
	if err != nil {
		t.Fatalf("pkce: %v", err)
	}
	authURL := challenge.AuthorizationURL("")
	for _, want := range []string{
		"https://auth.openai.com/oauth/authorize?",
		"code_challenge_method=S256",
		"client_id=app_EMoamEEZ73f0CkXaXp7hrann",
		"state=" + challenge.State,
	} {
		if !strings.Contains(authURL, want) {
			t.Fatalf("authorization url missing %q: %s", want, authURL)
		}
	}
	if strings.Contains(authURL, challenge.Verifier) {
		t.Fatal("verifier must never appear in the authorization url")
	}
	if !challenge.VerifyState(challenge.State) || challenge.VerifyState("other") {
		t.Fatal("state verification is broken")
	}
}
