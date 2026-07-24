//go:build blackbox

// Package starapi_test runs explicitly gated, full-Gateway smoke tests against a
// StarAPI-compatible OpenAI and Anthropic upstream. No upstream credential is read
// unless STARAPI_BLACKBOX=1, and sdkfixture keeps those credentials out of test code.
package starapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/openai/openai-go"
	openaioption "github.com/openai/openai-go/option"
	openairesponses "github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"

	"github.com/ThankCat/unio-gateway/internal/blackbox/sdkfixture"
)

const realUpstreamTimeout = 60 * time.Second

func TestStarAPIOpenAIChatNonStream(t *testing.T) {
	f := setupStarAPIOpenAI(t)
	client := newOpenAIClient(f)
	ctx, cancel := context.WithTimeout(context.Background(), realUpstreamTimeout)
	defer cancel()

	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(f.ModelID),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Reply with exactly: ok"),
		},
	})
	if err != nil {
		t.Fatalf("StarAPI OpenAI Chat non-stream request failed: %v", err)
	}
	if len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Message.Content) == "" {
		t.Fatal("StarAPI OpenAI Chat non-stream response has no content")
	}
	if resp.Usage.PromptTokens <= 0 || resp.Usage.CompletionTokens <= 0 || resp.Usage.TotalTokens <= 0 {
		t.Fatalf("StarAPI OpenAI Chat non-stream response has invalid usage: %+v", resp.Usage)
	}

	f.AssertLatestRequestFacts(t, sdkfixture.RequestFactsExpectation{
		IngressProtocol: "openai",
		Endpoint:       "chat_completions",
		Stream:          false,
	})
}

func TestStarAPIOpenAIChatStream(t *testing.T) {
	f := setupStarAPIOpenAI(t)
	client := newOpenAIClient(f)
	ctx, cancel := context.WithTimeout(context.Background(), realUpstreamTimeout)
	defer cancel()

	stream := client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(f.ModelID),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Reply with exactly: ok"),
		},
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	})

	var (
		content  strings.Builder
		gotUsage bool
		frames   int
	)
	for stream.Next() {
		chunk := stream.Current()
		frames++
		if len(chunk.Choices) > 0 {
			content.WriteString(chunk.Choices[0].Delta.Content)
		}
		if chunk.Usage.TotalTokens > 0 {
			gotUsage = chunk.Usage.PromptTokens > 0 && chunk.Usage.CompletionTokens > 0
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("StarAPI OpenAI Chat stream failed: %v", err)
	}
	if frames < 2 || strings.TrimSpace(content.String()) == "" {
		t.Fatalf("StarAPI OpenAI Chat stream is incomplete: frames=%d content_empty=%v", frames, content.Len() == 0)
	}
	if !gotUsage {
		t.Fatal("StarAPI OpenAI Chat stream has no valid tail usage")
	}

	f.AssertLatestRequestFacts(t, sdkfixture.RequestFactsExpectation{
		IngressProtocol: "openai",
		Endpoint:       "chat_completions",
		Stream:          true,
	})
}

func TestStarAPIOpenAIResponsesNonStream(t *testing.T) {
	f := setupStarAPIOpenAI(t)
	client := newOpenAIClient(f)
	ctx, cancel := context.WithTimeout(context.Background(), realUpstreamTimeout)
	defer cancel()

	resp, err := client.Responses.New(ctx, newResponsesParams(f.ModelID))
	if err != nil {
		t.Fatalf("StarAPI OpenAI Responses non-stream request failed: %v", err)
	}
	if strings.TrimSpace(resp.OutputText()) == "" {
		t.Fatal("StarAPI OpenAI Responses non-stream response has no output text")
	}
	if resp.Usage.InputTokens <= 0 || resp.Usage.OutputTokens <= 0 || resp.Usage.TotalTokens <= 0 {
		t.Fatalf("StarAPI OpenAI Responses non-stream response has invalid usage: %+v", resp.Usage)
	}

	f.AssertLatestRequestFacts(t, sdkfixture.RequestFactsExpectation{
		IngressProtocol: "openai",
		Endpoint:       "responses",
		Stream:          false,
	})
}

func TestStarAPIOpenAIResponsesStream(t *testing.T) {
	f := setupStarAPIOpenAI(t)
	client := newOpenAIClient(f)
	ctx, cancel := context.WithTimeout(context.Background(), realUpstreamTimeout)
	defer cancel()

	stream := client.Responses.NewStreaming(ctx, newResponsesParams(f.ModelID))
	var (
		content   strings.Builder
		completed *openairesponses.Response
		frames    int
	)
	for stream.Next() {
		event := stream.Current()
		frames++
		switch event.Type {
		case "response.output_text.delta":
			content.WriteString(event.AsResponseOutputTextDelta().Delta)
		case "response.completed":
			response := event.AsResponseCompleted().Response
			completed = &response
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("StarAPI OpenAI Responses stream failed: %v", err)
	}
	if frames < 3 || strings.TrimSpace(content.String()) == "" {
		t.Fatalf("StarAPI OpenAI Responses stream is incomplete: frames=%d content_empty=%v", frames, content.Len() == 0)
	}
	if completed == nil || completed.Usage.InputTokens <= 0 || completed.Usage.OutputTokens <= 0 || completed.Usage.TotalTokens <= 0 {
		t.Fatal("StarAPI OpenAI Responses stream has no valid completed usage")
	}

	f.AssertLatestRequestFacts(t, sdkfixture.RequestFactsExpectation{
		IngressProtocol: "openai",
		Endpoint:       "responses",
		Stream:          true,
	})
}

func TestStarAPIOpenAIResponsesCompactNative(t *testing.T) {
	requireCompactRealUpstream(t)
	f := setupStarAPIOpenAI(t)

	status, body := requestCompact(t, f)
	if status != http.StatusOK {
		t.Fatalf("StarAPI native compact status=%d body=%s", status, compactBodySnippet(body))
	}
	assertCompactOutput(t, body)

	f.AssertLatestRequestFacts(t, sdkfixture.RequestFactsExpectation{
		IngressProtocol: "openai",
		Endpoint:       "responses",
		Stream:          false,
	})
	assertLatestCompactAttempts(t, f, []compactAttemptWant{
		{endpoint: "responses_compact", status: "succeeded", statusCode: http.StatusOK},
	})
}

func TestStarAPIOpenAIResponsesCompactSyntheticDirect(t *testing.T) {
	requireCompactRealUpstream(t)
	f := setupStarAPIOpenAIWithAdapter(t, "deepseek")

	status, body := requestCompact(t, f)
	if status != http.StatusOK {
		t.Fatalf("StarAPI direct synthetic compact status=%d body=%s", status, compactBodySnippet(body))
	}
	assertCompactOutput(t, body)

	f.AssertLatestRequestFacts(t, sdkfixture.RequestFactsExpectation{
		IngressProtocol: "openai",
		Endpoint:       "responses",
		Stream:          false,
	})
	assertLatestCompactAttempts(t, f, []compactAttemptWant{
		{endpoint: "chat_completions", status: "succeeded", statusCode: http.StatusOK},
	})
}

func TestStarAPIOpenAIResponsesCompactProxyFallback(t *testing.T) {
	requireCompactRealUpstream(t)
	baseURL, apiKey, model := starAPIOpenAIEnv(t)
	proxy := newStarAPIProxy(t, baseURL, true)
	t.Cleanup(proxy.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: proxy.URL,
		UpstreamAPIKey:  apiKey,
		Protocol:        "openai",
		AdapterKey:      "openai",
		ModelID:         starAPIBlackboxModelID("openai-compact-proxy"),
		UpstreamModel:   model,
	})

	status, body := requestCompact(t, f)
	if status != http.StatusOK {
		t.Fatalf("StarAPI proxy fallback compact status=%d body=%s", status, compactBodySnippet(body))
	}
	assertCompactOutput(t, body)

	f.AssertLatestRequestFacts(t, sdkfixture.RequestFactsExpectation{
		IngressProtocol: "openai",
		Endpoint:       "responses",
		Stream:          false,
	})
	assertLatestCompactAttempts(t, f, []compactAttemptWant{
		{endpoint: "responses_compact", status: "failed", statusCode: http.StatusNotFound},
		{endpoint: "chat_completions", status: "succeeded", statusCode: http.StatusOK},
	})
}

func TestStarAPIAnthropicMessagesNonStream(t *testing.T) {
	f := setupStarAPIAnthropic(t)
	client := newAnthropicClient(f)
	ctx, cancel := context.WithTimeout(context.Background(), realUpstreamTimeout)
	defer cancel()

	msg, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(f.ModelID),
		MaxTokens: 256,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Reply with exactly: ok")),
		},
	})
	if err != nil {
		t.Fatalf("StarAPI Anthropic Messages non-stream request failed: %v", err)
	}
	if !anthropicMessageHasContent(msg) {
		t.Fatal("StarAPI Anthropic Messages non-stream response has no text or thinking content")
	}
	if msg.Usage.InputTokens <= 0 || msg.Usage.OutputTokens <= 0 {
		t.Fatalf("StarAPI Anthropic Messages non-stream response has invalid usage: %+v", msg.Usage)
	}

	f.AssertLatestRequestFacts(t, sdkfixture.RequestFactsExpectation{
		IngressProtocol: "anthropic",
		Endpoint:       "messages",
		Stream:          false,
	})
}

func TestStarAPIAnthropicMessagesStream(t *testing.T) {
	f := setupStarAPIAnthropic(t)
	client := newAnthropicClient(f)
	ctx, cancel := context.WithTimeout(context.Background(), realUpstreamTimeout)
	defer cancel()

	stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(f.ModelID),
		MaxTokens: 256,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Reply with exactly: ok")),
		},
	})
	var (
		acc     anthropic.Message
		frames  int
		sawStop bool
	)
	for stream.Next() {
		event := stream.Current()
		frames++
		if err := acc.Accumulate(event); err != nil {
			t.Fatalf("accumulate StarAPI Anthropic stream event %q: %v", event.Type, err)
		}
		if event.Type == "message_stop" {
			sawStop = true
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("StarAPI Anthropic Messages stream failed: %v", err)
	}
	if frames < 3 || !sawStop || !anthropicMessageHasContent(&acc) {
		t.Fatalf("StarAPI Anthropic Messages stream is incomplete: frames=%d saw_stop=%v content_empty=%v",
			frames, sawStop, !anthropicMessageHasContent(&acc))
	}
	if acc.Usage.InputTokens <= 0 || acc.Usage.OutputTokens <= 0 {
		t.Fatalf("StarAPI Anthropic Messages stream has invalid usage: %+v", acc.Usage)
	}

	f.AssertLatestRequestFacts(t, sdkfixture.RequestFactsExpectation{
		IngressProtocol: "anthropic",
		Endpoint:       "messages",
		Stream:          true,
	})
}

func setupStarAPIOpenAI(t *testing.T) *sdkfixture.Fixture {
	t.Helper()
	return setupStarAPIOpenAIWithAdapter(t, "openai")
}

func setupStarAPIOpenAIWithAdapter(t *testing.T, adapterKey string) *sdkfixture.Fixture {
	t.Helper()
	baseURL, apiKey, model := starAPIOpenAIEnv(t)
	proxy := newStarAPIProxy(t, baseURL, false)
	t.Cleanup(proxy.Close)
	return sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: proxy.URL,
		UpstreamAPIKey:  apiKey,
		Protocol:        "openai",
		AdapterKey:      adapterKey,
		ModelID:         starAPIBlackboxModelID("openai"),
		UpstreamModel:   model,
	})
}

func setupStarAPIAnthropic(t *testing.T) *sdkfixture.Fixture {
	t.Helper()
	baseURL, apiKey, model := starAPIAnthropicEnv(t)
	proxy := newStarAPIProxy(t, baseURL, false)
	t.Cleanup(proxy.Close)
	return sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: proxy.URL,
		UpstreamAPIKey:  apiKey,
		Protocol:        "anthropic",
		AdapterKey:      "anthropic",
		ModelID:         starAPIBlackboxModelID("anthropic"),
		UpstreamModel:   model,
	})
}

func newOpenAIClient(f *sdkfixture.Fixture) openai.Client {
	return openai.NewClient(
		openaioption.WithBaseURL(f.BaseURL),
		openaioption.WithAPIKey(f.APIKey),
		openaioption.WithMaxRetries(0),
	)
}

func newResponsesParams(model string) openairesponses.ResponseNewParams {
	return openairesponses.ResponseNewParams{
		Model: shared.ResponsesModel(model),
		Input: openairesponses.ResponseNewParamsInputUnion{
			OfString: openai.String("Reply with exactly: ok"),
		},
		MaxOutputTokens: openai.Int(128),
	}
}

func newAnthropicClient(f *sdkfixture.Fixture) anthropic.Client {
	return anthropic.NewClient(
		anthropicoption.WithBaseURL(f.AnthropicBaseURL),
		anthropicoption.WithAPIKey(f.APIKey),
		anthropicoption.WithMaxRetries(0),
	)
}

func anthropicMessageHasContent(msg *anthropic.Message) bool {
	if msg == nil {
		return false
	}
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				return true
			}
		case "thinking":
			if strings.TrimSpace(block.AsThinking().Thinking) != "" {
				return true
			}
		}
	}
	return false
}

func requireCompactRealUpstream(t *testing.T) {
	t.Helper()
	if os.Getenv("STARAPI_COMPACT_BLACKBOX") != "1" {
		t.Skip("STARAPI_COMPACT_BLACKBOX is not set to 1")
	}
}

func starAPIOpenAIEnv(t *testing.T) (string, string, string) {
	t.Helper()
	return starAPIProtocolEnv(t, "STARAPI_OPENAI_API_KEY", "STARAPI_OPENAI_MODEL")
}

func starAPIAnthropicEnv(t *testing.T) (string, string, string) {
	t.Helper()
	return starAPIProtocolEnv(t, "STARAPI_ANTHROPIC_API_KEY", "STARAPI_ANTHROPIC_MODEL")
}

func starAPIProtocolEnv(t *testing.T, apiKeyEnv string, modelEnv string) (string, string, string) {
	t.Helper()
	if os.Getenv("STARAPI_BLACKBOX") != "1" {
		t.Skip("STARAPI_BLACKBOX is not set to 1")
	}
	baseURL := strings.TrimSpace(os.Getenv("STARAPI_BASE_URL"))
	if baseURL == "" {
		t.Skip("STARAPI_BASE_URL is not set")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		t.Fatalf("STARAPI_BASE_URL must be an http(s) API root without credentials, path, query, or fragment")
	}
	parsed.Path = ""
	apiKey := os.Getenv(apiKeyEnv)
	if apiKey == "" {
		t.Skipf("%s is not set", apiKeyEnv)
	}
	model := strings.TrimSpace(os.Getenv(modelEnv))
	if model == "" {
		t.Skipf("%s is not set", modelEnv)
	}
	return parsed.String(), apiKey, model
}

func starAPIBlackboxModelID(prefix string) string {
	return fmt.Sprintf("starapi-blackbox-%s-%d", prefix, time.Now().UnixNano())
}

func requestCompact(t *testing.T, f *sdkfixture.Fixture) (int, []byte) {
	t.Helper()
	body := fmt.Sprintf(
		`{"model":%q,"instructions":"compact the conversation into one concise paragraph","input":"Earlier context: the user approved P4 routing changes. Reply with a concise compaction summary."}`,
		f.ModelID,
	)
	req, err := http.NewRequest(http.MethodPost, f.BaseURL+"/responses/compact", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build compact request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: realUpstreamTimeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("send compact request: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read compact response: %v", err)
	}
	return resp.StatusCode, raw
}

func assertCompactOutput(t *testing.T, body []byte) {
	t.Helper()
	var parsed struct {
		Output []json.RawMessage `json:"output"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode compact response: %v body=%s", err, compactBodySnippet(body))
	}
	if parsed.Error != nil {
		t.Fatalf("compact returned error code=%q message=%q", parsed.Error.Code, parsed.Error.Message)
	}
	if len(parsed.Output) == 0 {
		t.Fatalf("compact response has no output: %s", compactBodySnippet(body))
	}
}

type compactAttemptWant struct {
	endpoint  string
	status     string
	statusCode int32
}

func assertLatestCompactAttempts(t *testing.T, f *sdkfixture.Fixture, want []compactAttemptWant) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var requestID int64
	var requestStarted pgtype.Timestamptz
	if err := f.Pool.QueryRow(ctx, `
		SELECT id, response_started_at
		FROM request_records
		WHERE user_id = $1
		ORDER BY id DESC
		LIMIT 1
	`, f.UserID).Scan(&requestID, &requestStarted); err != nil {
		t.Fatalf("load latest compact request: %v", err)
	}
	if requestStarted.Valid {
		t.Fatal("non-stream compact request response_started_at must be NULL")
	}

	rows, err := f.Pool.Query(ctx, `
		SELECT attempt_index, status, upstream_status_code, upstream_endpoint,
		       response_started_at, upstream_first_token_at
		FROM request_attempts
		WHERE request_record_id = $1
		ORDER BY attempt_index ASC
	`, requestID)
	if err != nil {
		t.Fatalf("load compact attempts: %v", err)
	}
	defer rows.Close()

	var got []struct {
		index             int32
		status            string
		statusCode        pgtype.Int4
		endpoint         string
		responseStartedAt pgtype.Timestamptz
		firstTokenAt      pgtype.Timestamptz
	}
	for rows.Next() {
		var item struct {
			index             int32
			status            string
			statusCode        pgtype.Int4
			endpoint         string
			responseStartedAt pgtype.Timestamptz
			firstTokenAt      pgtype.Timestamptz
		}
		if err := rows.Scan(
			&item.index, &item.status, &item.statusCode, &item.endpoint,
			&item.responseStartedAt, &item.firstTokenAt,
		); err != nil {
			t.Fatalf("scan compact attempt: %v", err)
		}
		got = append(got, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate compact attempts: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("compact attempt count=%d want=%d attempts=%+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].index != int32(i) {
			t.Errorf("compact attempt[%d] index=%d want %d", i, got[i].index, i)
		}
		if got[i].endpoint != want[i].endpoint || got[i].status != want[i].status {
			t.Errorf("compact attempt[%d] endpoint/status=%q/%q want %q/%q",
				i, got[i].endpoint, got[i].status, want[i].endpoint, want[i].status)
		}
		if !got[i].statusCode.Valid || got[i].statusCode.Int32 != want[i].statusCode {
			t.Errorf("compact attempt[%d] status_code=%v want %d", i, got[i].statusCode, want[i].statusCode)
		}
		if got[i].responseStartedAt.Valid || got[i].firstTokenAt.Valid {
			t.Errorf("compact attempt[%d] non-stream timing must not have response_started/first_token", i)
		}
	}
}

func newStarAPIProxy(t *testing.T, upstreamRoot string, compactUnsupported bool) *httptest.Server {
	t.Helper()
	upstream, err := url.Parse(upstreamRoot)
	if err != nil {
		t.Fatalf("parse upstream root: %v", err)
	}
	client := &http.Client{Timeout: realUpstreamTimeout}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if compactUnsupported && r.URL.Path == "/v1/responses/compact" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("x-request-id", "starapi-compact-proxy-unsupported")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"compact unsupported by proxy"}}`))
			return
		}

		target := *upstream
		target.Path = strings.TrimRight(upstream.Path, "/") + r.URL.Path
		target.RawQuery = r.URL.RawQuery
		req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
		if err != nil {
			http.Error(w, "build proxy request", http.StatusBadGateway)
			return
		}
		req.Header = r.Header.Clone()
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "proxy request failed", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
}

func compactBodySnippet(body []byte) string {
	const max = 1_000
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) > max {
		return trimmed[:max] + "..."
	}
	return trimmed
}
