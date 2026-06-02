//go:build blackbox

package openaisdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"

	"github.com/ThankCat/unio-api/internal/blackbox/sdkfixture"
)

// OAI-SDK-Mock-05：tools 多轮 function calling。
//
// 第 1 轮：SDK 发 tools；mock 上游返回 tool_calls；SDK 解析。
// 第 2 轮：SDK 把 tool_calls + tool 角色的 result message 一起发回；mock 上游返回最终回复。
//
// 核心证明：openai-go ChatCompletionMessageToolCall typed 解析在 unio gateway 端到端可用，
// tool_calls / tool_call_id / function.name / function.arguments 都能正确穿越。
func TestOAISDKMockToolsMultiTurn(t *testing.T) {
	turn := 0
	mock := newMockUpstream(t, func(t *testing.T, w http.ResponseWriter, _ *http.Request, body []byte) {
		turn++
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("upstream body parse: %v", err)
		}

		switch turn {
		case 1:
			tools, _ := req["tools"].([]any)
			if len(tools) == 0 {
				t.Fatalf("expected upstream to receive tools, got body=%s", string(body))
			}
			writeMockChatCompletionWithMessage(w, "chatcmpl-tool-1", map[string]any{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]any{{
					"id":   "call_1",
					"type": "function",
					"function": map[string]any{
						"name":      "get_weather",
						"arguments": `{"city":"Tokyo"}`,
					},
				}},
			}, "tool_calls", 8, 5)
		case 2:
			msgs, _ := req["messages"].([]any)
			if len(msgs) < 3 {
				t.Fatalf("turn 2 expected >= 3 messages (user+assistant+tool), got %d: %s", len(msgs), string(body))
			}
			last, _ := msgs[len(msgs)-1].(map[string]any)
			if role, _ := last["role"].(string); role != "tool" {
				t.Fatalf("turn 2 expected last message role 'tool', got %q", role)
			}
			if toolCallID, _ := last["tool_call_id"].(string); toolCallID != "call_1" {
				t.Fatalf("turn 2 expected tool_call_id 'call_1', got %q", toolCallID)
			}
			writeMockChatCompletion(w, "chatcmpl-tool-2", "Tokyo is sunny, 22°C.", 20, 7)
		default:
			t.Fatalf("unexpected upstream turn %d", turn)
		}
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL + "/v1",
	})

	client := openai.NewClient(
		option.WithBaseURL(f.BaseURL),
		option.WithAPIKey(f.APIKey),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	params := openai.ChatCompletionNewParams{
		Model: openai.ChatModel(f.ModelID),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("what's the weather in Tokyo?"),
		},
		Tools: []openai.ChatCompletionToolParam{{
			Function: shared.FunctionDefinitionParam{
				Name:        "get_weather",
				Description: openai.String("Get current weather of a city"),
				Parameters: shared.FunctionParameters{
					"type": "object",
					"properties": map[string]any{
						"city": map[string]any{"type": "string"},
					},
					"required": []string{"city"},
				},
			},
		}},
	}

	resp1, err := client.Chat.Completions.New(ctx, params)
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if len(resp1.Choices[0].Message.ToolCalls) == 0 {
		t.Fatalf("turn 1 expected tool_calls, got message=%+v", resp1.Choices[0].Message)
	}
	tc := resp1.Choices[0].Message.ToolCalls[0]
	if tc.Function.Name != "get_weather" {
		t.Fatalf("turn 1 expected function name 'get_weather', got %q", tc.Function.Name)
	}

	// 第 2 轮：把 assistant message 与 tool result 加入历史。
	params.Messages = append(params.Messages, resp1.Choices[0].Message.ToParam())
	params.Messages = append(params.Messages, openai.ToolMessage(`{"temp_c":22,"condition":"sunny"}`, tc.ID))

	resp2, err := client.Chat.Completions.New(ctx, params)
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if got := resp2.Choices[0].Message.Content; got != "Tokyo is sunny, 22°C." {
		t.Fatalf("turn 2 unexpected content: %q", got)
	}
}
