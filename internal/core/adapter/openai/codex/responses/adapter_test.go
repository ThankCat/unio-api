package codexresponses

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	openairesponses "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/servicetier"
)

// TestFinalizeCodexFactsOutboundTierAuthoritative 冻结边界 15 的结算例外：
// Codex 响应档位不可信，结算档位以出站请求档位为准；响应原始值保留供审计。
func TestFinalizeCodexFactsOutboundTierAuthoritative(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		responseRaw string
		wantTier    servicetier.Tier
	}{
		// 上游对 priority 请求仍回 default——按出站定 Fast，这正是本例外要修的现象。
		{name: "outbound priority beats response default", body: `{"model":"gpt-5.5","service_tier":"priority"}`, responseRaw: "default", wantTier: servicetier.TierFast},
		{name: "outbound fast alias", body: `{"service_tier":"fast"}`, responseRaw: "auto", wantTier: servicetier.TierFast},
		{name: "outbound absent is standard", body: `{"model":"gpt-5.5"}`, responseRaw: "default", wantTier: servicetier.TierStandard},
		{name: "outbound default is standard", body: `{"service_tier":"default"}`, responseRaw: "", wantTier: servicetier.TierStandard},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := adapter.ResponseFacts{
				ServiceTier: servicetier.Response{
					Actual: servicetier.TierStandard, Settled: servicetier.TierStandard,
					UpstreamRaw: tc.responseRaw, Resolution: servicetier.ResolutionUpstreamResponse,
				},
			}
			finalizeCodexFacts(openairesponses.Request{Body: json.RawMessage(tc.body)}, &facts)

			if facts.ServiceTier.Actual != tc.wantTier || facts.ServiceTier.Settled != tc.wantTier {
				t.Fatalf("tier = actual %q settled %q, want %q", facts.ServiceTier.Actual, facts.ServiceTier.Settled, tc.wantTier)
			}
			if facts.ServiceTier.Resolution != servicetier.ResolutionWireOutboundAuthoritative {
				t.Fatalf("resolution = %q, want wire_outbound_authoritative", facts.ServiceTier.Resolution)
			}
			if facts.ServiceTier.UpstreamRaw != tc.responseRaw {
				t.Fatalf("upstream raw = %q, want preserved %q", facts.ServiceTier.UpstreamRaw, tc.responseRaw)
			}
		})
	}
}

// TestFinalizeCodexFactsUnparsableBodyKeepsResponseTier 解析不了的 body 不猜：保留响应侧判定。
func TestFinalizeCodexFactsUnparsableBodyKeepsResponseTier(t *testing.T) {
	facts := adapter.ResponseFacts{
		ServiceTier: servicetier.Response{
			Actual: servicetier.TierFast, Settled: servicetier.TierFast,
			UpstreamRaw: "priority", Resolution: servicetier.ResolutionUpstreamResponse,
		},
	}
	finalizeCodexFacts(openairesponses.Request{Body: json.RawMessage(`not-json`)}, &facts)
	if facts.ServiceTier.Resolution != servicetier.ResolutionUpstreamResponse {
		t.Fatalf("unparsable body must not override facts, got resolution %q", facts.ServiceTier.Resolution)
	}
}

// TestCodexRetryAfterFromBody 冻结 429 错误体的重置时刻兜底解析（x-codex 头缺失时）。
func TestCodexRetryAfterFromBody(t *testing.T) {
	if d := codexRetryAfterFromBody(429, `{"error":{"type":"usage_limit_reached","resets_in_seconds":120}}`); d != 120*time.Second {
		t.Fatalf("resets_in_seconds = %v, want 120s", d)
	}
	future := time.Now().Add(10 * time.Minute).Unix()
	if d := codexRetryAfterFromBody(429, fmt.Sprintf(`{"error":{"type":"rate_limit_exceeded","resets_at":%d}}`, future)); d < 9*time.Minute || d > 10*time.Minute {
		t.Fatalf("resets_at = %v, want ~10m", d)
	}
	for _, tc := range []struct {
		name    string
		status  int
		snippet string
	}{
		{"非 429 不解析", 401, `{"error":{"type":"usage_limit_reached","resets_in_seconds":120}}`},
		{"非限流类型不解析", 429, `{"error":{"type":"invalid_request_error","resets_in_seconds":120}}`},
		{"坏 JSON 返回 0", 429, `not-json`},
		{"过去的 resets_at 返回 0", 429, `{"error":{"type":"usage_limit_reached","resets_at":1000}}`},
	} {
		if d := codexRetryAfterFromBody(tc.status, tc.snippet); d != 0 {
			t.Fatalf("%s: got %v, want 0", tc.name, d)
		}
	}
}
