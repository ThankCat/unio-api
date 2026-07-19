package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// scrape 调用 /metrics handler 并返回响应文本。
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected metrics status 200, got %d", rec.Code)
	}

	return rec.Body.String()
}

// TestMetricsExposesRecordedSeries 验证各类记录方法都会在 /metrics 输出对应时间序列。
func TestMetricsExposesRecordedSeries(t *testing.T) {
	m := New()

	m.ObserveHTTPRequest(http.MethodPost, "/v1/chat/completions", http.StatusOK, 120*time.Millisecond)
	m.IncChatRequest(false, ChatOutcomeSuccess)
	m.IncChatRequest(true, ChatOutcomeCanceled)
	m.IncRoutingSelected("9123", "123", "openai/gpt-4.1")
	m.ObserveUpstream("9123", "123", true, "", 800*time.Millisecond)
	m.ObserveUpstream("9123", "123", false, "rate_limit", 50*time.Millisecond)
	m.IncSettlement(SettlementOutcomeSuccess)
	m.IncStreamEvent(StreamEventCompleted)
	m.IncRateLimitDecision(RateLimitDecisionLimited)
	m.ObserveRoutingBalance("balanced", "planned", 3, 2, 0.25)
	m.IncRoutingBalanceSelected("42", "123")
	m.IncRoutingBalanceFallback("42", "upstream_timeout")
	m.IncRoutingCapacityRead("success")
	m.IncRoutingMarginGuard("runtime_rejected")
	m.IncRoutingTraceWrite("success")

	body := scrape(t, m)

	wants := []string{
		`unio_http_requests_total{method="POST",route="/v1/chat/completions",status="200"} 1`,
		`unio_gateway_chat_requests_total{outcome="success",stream="false"} 1`,
		`unio_gateway_chat_requests_total{outcome="canceled",stream="true"} 1`,
		`unio_gateway_routing_selected_total{channel="123",model="openai/gpt-4.1",provider="9123"} 1`,
		`unio_gateway_upstream_requests_total{channel="123",error_category="none",outcome="success",provider="9123"} 1`,
		`unio_gateway_upstream_requests_total{channel="123",error_category="rate_limit",outcome="error",provider="9123"} 1`,
		`unio_gateway_settlement_total{outcome="success"} 1`,
		`unio_gateway_stream_events_total{event="completed"} 1`,
		`unio_ratelimit_decisions_total{decision="limited"} 1`,
		`unio_gateway_routing_balance_total{mode="balanced",result="planned"} 1`,
		`unio_gateway_routing_balance_candidate_count_count{mode="balanced"} 1`,
		`unio_gateway_routing_balance_pool_size_count{mode="balanced"} 1`,
		`unio_gateway_routing_balance_selected_total{channel="123",route="42"} 1`,
		`unio_gateway_routing_balance_fallback_total{reason="upstream_timeout",route="42"} 1`,
		`unio_gateway_routing_balance_load_skew_count 1`,
		`unio_gateway_routing_balance_capacity_read_total{result="success"} 1`,
		`unio_gateway_routing_margin_guard_total{result="runtime_rejected"} 1`,
		`unio_gateway_routing_trace_write_total{result="success"} 1`,
	}

	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing series:\n%s", want)
		}
	}
}

// TestMetricsIncludesRuntimeCollectors 验证 registry 同时暴露 Go runtime / process 基础指标。
func TestMetricsIncludesRuntimeCollectors(t *testing.T) {
	m := New()
	body := scrape(t, m)

	for _, want := range []string{"go_goroutines", "process_"} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing runtime series prefix %q", want)
		}
	}
}

// TestObserveUpstreamSuccessForcesNoneCategory 验证成功调用的 error_category 固定为 none。
func TestObserveUpstreamSuccessForcesNoneCategory(t *testing.T) {
	m := New()
	// 即使误传了非空 errorCategory，成功调用也必须归一化为 none，避免污染成功序列。
	m.ObserveUpstream("9123", "123", true, "server_error", time.Second)

	body := scrape(t, m)
	if !strings.Contains(body, `error_category="none",outcome="success"`) {
		t.Errorf("expected successful upstream to force none category, got:\n%s", body)
	}
	if strings.Contains(body, `error_category="server_error",outcome="success"`) {
		t.Error("successful upstream must not record a non-none error category")
	}
}
