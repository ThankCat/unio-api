package quota

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 真实样例节选（sandbox/codex/wire/samples/upstream-wham-*.json）。
const sampleUsage = `{
  "plan_type": "plus",
  "rate_limit": {
    "allowed": true, "limit_reached": false,
    "primary_window": {"used_percent": 0, "limit_window_seconds": 18000, "reset_after_seconds": 18000, "reset_at": 1788708783},
    "secondary_window": {"used_percent": 71, "limit_window_seconds": 604800, "reset_after_seconds": 271601, "reset_at": 1788962383}
  },
  "additional_rate_limits": null,
  "rate_limit_reset_credits": {"available_count": 2, "applicable_available_count": 0}
}`

const sampleCredits = `{
  "credits": [
    {"id": "credit-b", "reset_type": "codex_rate_limits", "is_supported_by_plan": true, "status": "available",
     "granted_at": "2026-09-04T22:53:26.927598Z", "expires_at": "2026-10-04T22:53:26.927598Z", "title": "Full reset (Weekly + 5 hr)"},
    {"id": "credit-a", "reset_type": "codex_rate_limits", "is_supported_by_plan": true, "status": "available",
     "granted_at": "2026-09-04T02:31:51.152383Z", "expires_at": "2026-10-04T02:31:51.152383Z", "title": "Full reset (Weekly + 5 hr)"},
    {"id": "credit-used", "reset_type": "codex_rate_limits", "status": "redeemed", "expires_at": "2026-10-01T00:00:00Z"},
    {"id": "credit-other", "reset_type": "something_else", "status": "available", "expires_at": "2026-10-01T00:00:00Z"}
  ],
  "available_count": 2,
  "total_earned_count": 0
}`

// accounts/check 与 me 的真实样例节选（脱敏）。
const sampleAccountsCheck = `{
  "accounts": {
    "acct-1": {
      "account": {
        "account_id": "acct-1", "plan_type": "plus", "plan_display_name": "Plus", "structure": "personal",
        "workspace_type": null, "is_deactivated": false, "eligible_for_reactivation": true,
        "has_previously_paid_subscription": true, "created_time": "2026-09-02T11:14:50.035328Z",
        "account_compute_residency": "no_constraint"
      },
      "features": ["aura_available", "beta_features"],
      "entitlement": {
        "subscription_id": "sub-1", "has_active_subscription": true, "is_active_subscription_gratis": false,
        "subscription_plan": "chatgptplusplan", "expires_at": "2026-10-02T17:53:17+00:00",
        "renews_at": "2026-10-02T11:53:17+00:00", "cancels_at": null, "billing_period": "monthly",
        "billing_currency": "VND", "is_delinquent": false, "grace_period_end_timestamp": null,
        "discount": {"discount_type": "percentage", "amount": 100.0, "discount_expires_at": "2026-10-02T11:53:17+00:00", "promo_campaign_id": "plus-1-month-free"}
      }
    }
  }
}`

const sampleMe = `{
  "object": "user", "id": "user-x", "email": "acct@example.com", "name": "Nancy", "created": 1788347687,
  "phone_number": "+81...", "mfa_flag_enabled": true, "email_domain_type": "social", "country": "JP", "region": "Tokyo",
  "orgs": {"object": "list", "data": [{"object": "organization", "id": "org-x", "title": "Personal", "personal": true, "is_default": true, "role": "owner", "banned": null}]}
}`

type capturedRequest struct {
	method  string
	path    string
	headers http.Header
	body    string
}

func newUpstream(t *testing.T, consumeStatus int, consumeBody string) (*Client, *[]capturedRequest) {
	t.Helper()
	var captured []capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 0)
		if r.Body != nil {
			buf := make([]byte, 4096)
			n, _ := r.Body.Read(buf)
			raw = buf[:n]
		}
		captured = append(captured, capturedRequest{method: r.Method, path: r.URL.Path, headers: r.Header.Clone(), body: string(raw)})
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			_, _ = w.Write([]byte(sampleUsage))
		case "/backend-api/wham/rate-limit-reset-credits":
			_, _ = w.Write([]byte(sampleCredits))
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			w.WriteHeader(consumeStatus)
			_, _ = w.Write([]byte(consumeBody))
		case "/backend-api/accounts/check/v4-2023-04-27":
			if r.Header.Get("Origin") != "https://chatgpt.com" || r.Header.Get("Referer") != "https://chatgpt.com/" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(sampleAccountsCheck))
		case "/backend-api/me":
			_, _ = w.Write([]byte(sampleMe))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client := NewClient(nil, func() string { return "0.160.0" }).WithBaseURL(server.URL)
	return client, &captured
}

var testIdentity = Identity{AccessToken: "token-1", UpstreamAccountID: "acct-1"}

func TestClientFetchUsageSendsCodexIdentityAndDecodesWindows(t *testing.T) {
	client, captured := newUpstream(t, http.StatusOK, `{}`)

	usage, err := client.FetchUsage(context.Background(), testIdentity)
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if usage.PlanType != "plus" || usage.RateLimit == nil || usage.RateLimit.SecondaryWindow == nil {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.RateLimit.SecondaryWindow.UsedPercent != 71 || usage.RateLimit.SecondaryWindow.ResetAt != 1788962383 {
		t.Fatalf("secondary window = %+v", usage.RateLimit.SecondaryWindow)
	}
	if usage.ResetCreditCounts == nil || usage.ResetCreditCounts.AvailableCount != 2 {
		t.Fatalf("reset credit counts = %+v", usage.ResetCreditCounts)
	}

	req := (*captured)[0]
	if req.method != http.MethodGet || req.path != "/backend-api/wham/usage" {
		t.Fatalf("request = %s %s", req.method, req.path)
	}
	if got := req.headers.Get("Authorization"); got != "Bearer token-1" {
		t.Fatalf("authorization = %q", got)
	}
	if got := req.headers.Get("chatgpt-account-id"); got != "acct-1" {
		t.Fatalf("chatgpt-account-id = %q", got)
	}
	if got := req.headers.Get("originator"); got != "codex-tui" {
		t.Fatalf("originator = %q", got)
	}
	if got := req.headers.Get("version"); got != "0.160.0" {
		t.Fatalf("version = %q", got)
	}
	if ua := req.headers.Get("User-Agent"); !strings.HasPrefix(ua, "codex-tui/0.160.0 ") {
		t.Fatalf("user-agent = %q", ua)
	}
}

func TestClientFetchResetCreditsFiltersAndSortsUsableCredits(t *testing.T) {
	client, _ := newUpstream(t, http.StatusOK, `{}`)

	credits, err := client.FetchResetCredits(context.Background(), testIdentity)
	if err != nil {
		t.Fatalf("FetchResetCredits: %v", err)
	}
	if credits.AvailableCount != 2 || len(credits.Credits) != 4 {
		t.Fatalf("credits = %+v", credits)
	}
	usable := credits.UsableCredits()
	if len(usable) != 2 {
		t.Fatalf("usable = %+v", usable)
	}
	// 最早到期的排前面（credit-a 10-04 02:31 早于 credit-b 10-04 22:53）。
	if usable[0].ID != "credit-a" || usable[1].ID != "credit-b" {
		t.Fatalf("usable order = %s, %s", usable[0].ID, usable[1].ID)
	}
	if usable[0].ExpiresAt.IsZero() || usable[0].GrantedAt.IsZero() {
		t.Fatalf("timestamps must parse: %+v", usable[0])
	}
}

func TestClientConsumeResetCreditPostsIdempotencyKeyAndTargetsCredit(t *testing.T) {
	client, captured := newUpstream(t, http.StatusOK, `{"code":"success","windows_reset":2,"credit":{"reset_type":"codex_rate_limits","status":"redeemed","redeemed_at":"2026-09-06T10:00:00Z"}}`)

	result, err := client.ConsumeResetCredit(context.Background(), testIdentity, "credit-a", "redeem-1")
	if err != nil {
		t.Fatalf("ConsumeResetCredit: %v", err)
	}
	if result.Code != "success" || result.WindowsReset != 2 || result.Credit == nil || result.Credit.Status != "redeemed" {
		t.Fatalf("result = %+v", result)
	}
	req := (*captured)[0]
	if req.method != http.MethodPost || req.path != "/backend-api/wham/rate-limit-reset-credits/consume" {
		t.Fatalf("request = %s %s", req.method, req.path)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(req.body), &body); err != nil {
		t.Fatalf("body must be JSON: %v (%s)", err, req.body)
	}
	if body["redeem_request_id"] != "redeem-1" || body["credit_id"] != "credit-a" {
		t.Fatalf("body = %v", body)
	}
	if got := req.headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q", got)
	}

	// 不定向时不带 credit_id；空 redeem id 直接拒绝。
	if _, err := client.ConsumeResetCredit(context.Background(), testIdentity, "", "redeem-2"); err != nil {
		t.Fatalf("untargeted consume: %v", err)
	}
	body = map[string]string{}
	_ = json.Unmarshal([]byte((*captured)[1].body), &body)
	if _, has := body["credit_id"]; has {
		t.Fatalf("untargeted consume must not send credit_id: %v", body)
	}
	if _, err := client.ConsumeResetCredit(context.Background(), testIdentity, "", ""); err == nil {
		t.Fatal("empty redeem id must be rejected")
	}
}

func TestClientSurfacesUpstreamStatusAndBody(t *testing.T) {
	client, _ := newUpstream(t, http.StatusConflict, `{"detail":"no applicable credit"}`)

	_, err := client.ConsumeResetCredit(context.Background(), testIdentity, "", "redeem-1")
	var upstream *UpstreamError
	if !asUpstreamError(err, &upstream) {
		t.Fatalf("expected *UpstreamError, got %T %v", err, err)
	}
	if upstream.StatusCode != http.StatusConflict || !strings.Contains(upstream.Body, "no applicable credit") || upstream.Operation != "consume reset credit" {
		t.Fatalf("upstream error = %+v", upstream)
	}
	if _, err := client.FetchUsage(context.Background(), Identity{}); err == nil {
		t.Fatal("empty access token must be rejected before calling upstream")
	}
}

func TestParseUpstreamTime(t *testing.T) {
	if got := parseUpstreamTime("2026-10-04T02:31:51.152383Z"); got.IsZero() || got.Location() != time.UTC {
		t.Fatalf("parse = %v", got)
	}
	if !parseUpstreamTime("").IsZero() || !parseUpstreamTime("garbage").IsZero() {
		t.Fatal("invalid input must yield zero time")
	}
}

func TestClientFetchAccountCheckPicksOwnAccountAndDecodesEntitlement(t *testing.T) {
	client, _ := newUpstream(t, http.StatusOK, `{}`)

	check, err := client.FetchAccountCheck(context.Background(), testIdentity)
	if err != nil {
		t.Fatalf("FetchAccountCheck: %v", err)
	}
	if check.AccountID != "acct-1" || check.PlanType != "plus" || check.PlanDisplayName != "Plus" || check.Structure != "personal" {
		t.Fatalf("check = %+v", check)
	}
	if check.IsDeactivated || !check.HasPreviouslyPaidSubscription || check.FeatureCount != 2 || check.CreatedTime.IsZero() {
		t.Fatalf("check flags = %+v", check)
	}
	e := check.Entitlement
	if e == nil || !e.HasActiveSubscription || e.SubscriptionPlan != "chatgptplusplan" || e.BillingPeriod != "monthly" {
		t.Fatalf("entitlement = %+v", e)
	}
	if e.ExpiresAt.Format(time.RFC3339) != "2026-10-02T17:53:17Z" || e.RenewsAt.IsZero() || !e.CancelsAt.IsZero() {
		t.Fatalf("entitlement timestamps = %+v", e)
	}
	if e.PromoCampaignID != "plus-1-month-free" || e.DiscountPercent != 100 || e.DiscountExpiresAt.IsZero() {
		t.Fatalf("entitlement discount = %+v", e)
	}

	// 账号 id 不在列表里但列表只有一条：回落到唯一的一条。
	other := Identity{AccessToken: "token-1", UpstreamAccountID: "acct-unknown"}
	fallback, err := client.FetchAccountCheck(context.Background(), other)
	if err != nil || fallback.AccountID != "acct-1" {
		t.Fatalf("single-account fallback = %+v err=%v", fallback, err)
	}
}

func TestClientFetchMeDecodesProfileWithoutSensitiveFields(t *testing.T) {
	client, _ := newUpstream(t, http.StatusOK, `{}`)

	me, err := client.FetchMe(context.Background(), testIdentity)
	if err != nil {
		t.Fatalf("FetchMe: %v", err)
	}
	if me.Email != "acct@example.com" || me.Name != "Nancy" || !me.MFAEnabled || me.Country != "JP" || me.Region != "Tokyo" {
		t.Fatalf("me = %+v", me)
	}
	if me.Created.Unix() != 1788347687 || me.EmailDomainType != "social" {
		t.Fatalf("me created/domain = %+v", me)
	}
	if len(me.Orgs) != 1 || me.Orgs[0].Title != "Personal" || !me.Orgs[0].Personal || me.Orgs[0].Role != "owner" || me.Orgs[0].Banned {
		t.Fatalf("orgs = %+v", me.Orgs)
	}
}
