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

func TestP4MetricsExposeBoundedRuntimeFacts(t *testing.T) {
	m := New()
	ttft := 250 * time.Millisecond

	m.SetBreakerState("channel", "17", "open")
	m.IncBreakerTransition("channel", "closed", "open", "consecutive_failure")
	m.IncBreakerSkip("origin", "open")
	m.ObserveBreakerStoreOperation("acquire_attempt", "allowed", 10*time.Millisecond)
	m.SetBreakerStoreHealth(true, false)
	m.SetRuntimeStateIntegrity("ready")
	m.IncRuntimeStateLossRecovery("committed")
	m.IncRequestAdmissionOperation("acquire", "allowed")
	m.AddRequestAdmissionActive(1)
	m.IncBreakerPermitOperation("finish", "applied")
	m.AddBreakerPermitActive(1)
	m.IncBreakerIgnoredResult("origin", "stale_revision")
	m.IncChannelConfigRevisionMismatch("finish")
	m.IncChannelCredentialRotationVerification("passed")
	m.SetOriginBaseURLRevisionFence("23", "pending", 3*time.Second)
	m.SetOriginStatusRevisionFence("23", "active", 0)
	m.IncOriginStatusRevisionMismatch("acquire")
	m.IncRuntimeControlOperation("circuit_breaker", "commit", "success")
	m.SetRuntimeControlPending("circuit_breaker", true, 2*time.Second)
	m.IncRuntimeControlRevisionMismatch("circuit_breaker", "acquire")
	m.IncRuntimeControlRecovery("circuit_breaker", "committed")
	m.IncOriginFailure("23", "http_500")
	m.IncChannelFailure("17", "server")
	m.ObserveUpstreamTiming("11", "23", "17", "openai", "responses", "stream", time.Second, &ttft)
	m.ObserveUpstreamTiming("11", "23", "17", "openai", "responses", "non_stream", 2*time.Second, nil)
	m.SetBalancedFinalWeight("31", "17", 0.75)

	body := scrape(t, m)
	wants := []string{
		`unio_gateway_breaker_state{id="17",scope="channel",state="open"} 1`,
		`unio_gateway_breaker_transition_total{from="closed",reason="consecutive_failure",scope="channel",to="open"} 1`,
		`unio_gateway_breaker_skip_total{reason="open",scope="origin"} 1`,
		`unio_gateway_breaker_store_operation_total{operation="acquire_attempt",result="allowed"} 1`,
		`unio_gateway_breaker_store_latency_seconds_count{operation="acquire_attempt"} 1`,
		`unio_gateway_breaker_store_ready 1`,
		`unio_gateway_breaker_store_unavailable 0`,
		`unio_gateway_runtime_state_integrity{state="ready"} 1`,
		`unio_gateway_runtime_state_loss_recovery_total{result="committed"} 1`,
		`unio_gateway_request_admission_operation_total{operation="acquire",result="allowed"} 1`,
		`unio_gateway_request_admission_active 1`,
		`unio_gateway_breaker_permit_operation_total{operation="finish",result="applied"} 1`,
		`unio_gateway_breaker_permit_active 1`,
		`unio_gateway_breaker_ignored_result_total{reason="stale_revision",scope="origin"} 1`,
		`unio_gateway_channel_config_revision_mismatch_total{operation="finish"} 1`,
		`unio_gateway_channel_credential_rotation_verification_total{state="passed"} 1`,
		`unio_gateway_origin_base_url_revision_fence{origin_id="23",state="pending"} 1`,
		`unio_gateway_origin_base_url_revision_pending_seconds{origin_id="23"} 3`,
		`unio_gateway_origin_status_revision_fence{origin_id="23",state="active"} 1`,
		`unio_gateway_origin_status_revision_mismatch_total{operation="acquire"} 1`,
		`unio_gateway_runtime_control_operation_total{operation="commit",result="success",target="circuit_breaker"} 1`,
		`unio_gateway_runtime_control_pending{target="circuit_breaker"} 1`,
		`unio_gateway_runtime_control_pending_seconds{target="circuit_breaker"} 2`,
		`unio_gateway_runtime_control_revision_mismatch_total{operation="acquire",target="circuit_breaker"} 1`,
		`unio_gateway_runtime_control_recovery_total{result="committed",target="circuit_breaker"} 1`,
		`unio_gateway_origin_failure_total{category="http_500",origin_id="23"} 1`,
		`unio_gateway_channel_failure_total{category="server",channel_id="17"} 1`,
		`unio_gateway_upstream_ttft_seconds_count{channel_id="17",endpoint="responses",origin_id="23",protocol="openai",provider_id="11",sample_source="stream_only"} 1`,
		`unio_gateway_upstream_total_duration_seconds_count{channel_id="17",endpoint="responses",mode="stream",origin_id="23",protocol="openai",provider_id="11"} 1`,
		`unio_gateway_upstream_total_duration_seconds_count{channel_id="17",endpoint="responses",mode="non_stream",origin_id="23",protocol="openai",provider_id="11"} 1`,
		`unio_gateway_balanced_final_weight{channel_id="17",route_id="31"} 0.75`,
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing series:\n%s", want)
		}
	}
}
