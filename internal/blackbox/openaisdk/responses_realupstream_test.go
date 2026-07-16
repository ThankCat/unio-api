//go:build blackbox

package openaisdk_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/blackbox/sdkfixture"
)

// responses_realupstream_test.go 是 Responses → 真实 DeepSeek 的端到端 smoke（TASK-11.15）。
//
// gate：DEEPSEEK_BLACKBOX=1 + DEEPSEEK_API_KEY（sdkfixture.UpstreamReal 缺失即 t.Skip）。
// 这是「Codex 把 base_url 指到 Unio 即用 DeepSeek」在 HTTP 层的硬证据；真实 Codex CLI 端到端
// 手测步骤见 PLAN TASK-11.15。

// OAI-RESP-Real-01：非流式 /v1/responses 打真实 DeepSeek，响应与账务终态正确。
func TestResponsesRealNonStream(t *testing.T) {
	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:          sdkfixture.UpstreamReal,
		ModelID:       "deepseek-v4-flash",
		UpstreamModel: "deepseek-v4-flash",
	})

	resp := doResponses(t, http.MethodPost, f.BaseURL+"/responses", f.APIKey,
		`{"model":"`+f.ModelID+`","input":"Reply with the single word: ok","max_output_tokens":16,"stream":false}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(raw))
	}

	var rb responsesBody
	if err := json.NewDecoder(resp.Body).Decode(&rb); err != nil {
		t.Fatalf("decode responses body: %v", err)
	}
	if rb.Object != "response" {
		t.Errorf("object = %q, want response", rb.Object)
	}
	if len(rb.Output) == 0 || len(rb.Output[0].Content) == 0 || strings.TrimSpace(rb.Output[0].Content[0].Text) == "" {
		t.Fatalf("expected non-empty output text, got %+v", rb.Output)
	}
	if rb.Usage == nil || rb.Usage.InputTokens <= 0 || rb.Usage.OutputTokens <= 0 {
		t.Fatalf("unexpected usage: %+v", rb.Usage)
	}

	time.Sleep(200 * time.Millisecond)
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dbCancel()
	var rrStatus, rrOperation string
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT status, operation FROM request_records WHERE user_id = $1 ORDER BY id DESC LIMIT 1
	`, f.UserID).Scan(&rrStatus, &rrOperation); err != nil {
		t.Fatalf("query final status: %v", err)
	}
	if rrStatus != "succeeded" {
		t.Errorf("request_records.status = %q, want succeeded", rrStatus)
	}
	if rrOperation != "responses" {
		t.Errorf("request_records.operation = %q, want responses", rrOperation)
	}
}

// OAI-RESP-Real-02：流式 /v1/responses 打真实 DeepSeek，事件序列与 usage 正确。
func TestResponsesRealStream(t *testing.T) {
	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:          sdkfixture.UpstreamReal,
		ModelID:       "deepseek-v4-flash",
		UpstreamModel: "deepseek-v4-flash",
	})

	resp := doResponses(t, http.MethodPost, f.BaseURL+"/responses", f.APIKey,
		`{"model":"`+f.ModelID+`","input":"Reply with the single word: ok","max_output_tokens":16,"stream":true}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(raw))
	}

	events := readResponsesSSE(t, resp.Body)
	assertResponsesSSEInvariants(t, events)

	var text strings.Builder
	for _, ev := range events {
		if ev.Type == "response.output_text.delta" {
			text.WriteString(ev.Delta)
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		t.Errorf("expected non-empty streamed text")
	}

	completed := events[len(events)-1]
	if completed.Response == nil || completed.Response.Usage == nil || completed.Response.Usage.TotalTokens <= 0 {
		t.Errorf("expected usage in response.completed, got %+v", completed)
	}
}
