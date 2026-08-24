package responses

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
)

// TestCustomToolBridgeAgainstRealUpstream 用真实 chat-only 上游验证 custom 工具降级链路：
// 出站降级后的 function 定义能否让上游产出合法 patch，以及响应能否还原回 custom_tool_call。
//
// 需要显式开关与凭据，默认跳过：
//
//	UNIO_DEEPSEEK_E2E=1 DEEPSEEK_API_KEY=sk-... go test ./internal/service/gateway/openai/responses/ -run RealUpstream -v
//
// 只读调用上游，不触碰数据库、Redis 或任何本地业务数据。
func TestCustomToolBridgeAgainstRealUpstream(t *testing.T) {
	if os.Getenv("UNIO_DEEPSEEK_E2E") != "1" {
		t.Skip("set UNIO_DEEPSEEK_E2E=1 to run against the real upstream")
	}
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		t.Skip("DEEPSEEK_API_KEY is required for the real upstream test")
	}

	upstreamModel := os.Getenv("DEEPSEEK_MODEL")
	if upstreamModel == "" {
		upstreamModel = "deepseek-v4-flash"
	}

	req := decodeRequest(t, `{
		"model": "gpt-5.4-mini",
		"instructions": "You are Codex, a coding agent. Always use the apply_patch tool for file edits.",
		"input": [{"type":"message","role":"user","content":[{"type":"input_text","text":"The file calc.py contains:\n\ndef add(a, b):\n    return a + b\n\nAdd a multiply(a, b) function returning a * b. Use apply_patch."}]}],
		"tools": [`+applyPatchToolJSON+`]
	}`)

	// 出站：Responses → Chat（custom 工具在此降级为单参数 function）。
	chatReq, _ := mapResponsesRequestToChat(req, upstreamModel)
	if len(chatReq.Tools) != 1 || chatReq.Tools[0].Function.Name != "apply_patch" {
		t.Fatalf("unexpected outbound tools: %+v", chatReq.Tools)
	}

	// ChatMessage 是内部契约类型，线上由 adapter 的 wire 层编码；此处只验证桥接产物的语义，
	// 故按上游线格式手工投影，避免依赖 adapter 的 channel/凭据装配。
	messages := make([]map[string]any, 0, len(chatReq.Messages))
	for _, m := range chatReq.Messages {
		messages = append(messages, map[string]any{"role": m.Role, "content": m.ContentString()})
	}

	body, err := json.Marshal(map[string]any{
		"model":      chatReq.Model,
		"messages":   messages,
		"tools":      chatReq.Tools,
		"max_tokens": 600,
	})
	if err != nil {
		t.Fatalf("marshal upstream body: %v", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, "https://api.deepseek.com/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build upstream request: %v", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		t.Fatalf("call upstream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(resp.Body)
		t.Fatalf("upstream returned %d: %s\nrequest body: %s", resp.StatusCode, detail, body)
	}

	var upstream struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&upstream); err != nil {
		t.Fatalf("decode upstream: %v", err)
	}
	if len(upstream.Choices) == 0 || len(upstream.Choices[0].Message.ToolCalls) == 0 {
		t.Fatalf("upstream did not call the tool")
	}

	// 入站：Chat → Responses（降级 function 调用还原回 custom_tool_call）。
	call := upstream.Choices[0].Message.ToolCalls[0]
	out := mapChatResponseToResponses(req, chatcompletionsadapter.ChatResponse{
		ToolCalls: []chatcompletionsadapter.ChatToolCall{{
			ID: call.ID,
			Function: chatcompletionsadapter.ChatToolCallFunction{
				Name: call.Function.Name, Arguments: call.Function.Arguments,
			},
		}},
	})

	if len(out.Output) != 1 {
		t.Fatalf("expected one output item, got %d", len(out.Output))
	}
	item := out.Output[0]
	if item.Type != "custom_tool_call" {
		t.Fatalf("expected custom_tool_call, got %q", item.Type)
	}
	if item.Status != "completed" {
		t.Errorf("upstream violated the degraded schema: status=%q input=%q", item.Status, item.Input)
	}
	if !strings.HasPrefix(item.Input, "*** Begin Patch") || !strings.Contains(item.Input, "*** End Patch") {
		t.Errorf("restored patch is not a well-formed apply_patch document:\n%s", item.Input)
	}
	t.Logf("restored patch document:\n%s", item.Input)
}
