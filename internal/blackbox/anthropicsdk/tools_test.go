//go:build blackbox

package anthropicsdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/ThankCat/unio-api/internal/blackbox/sdkfixture"
)

// ANT-SDK-Mock-04：tool_use 多轮端到端。
//
// 流程：
//  1. 客户 SDK 发：user "What's the weather?" + tools=[get_weather]；
//  2. mock 上游 turn-1 返回：text("checking weather...") + tool_use(id=toolu_1, name=get_weather, input={location:"SF"})，
//     stop_reason=tool_use；
//  3. 客户 SDK 解析出 tool_use，并构造 user.tool_result(tool_use_id=toolu_1, content="sunny, 72F")
//     续接 turn-2；
//  4. mock 上游 turn-2 返回：text("It is sunny and 72F in SF.")，stop_reason=end_turn。
//
// 验证：
//   - 第一轮 stop_reason=tool_use，content 中包含 ToolUseBlock；
//   - 第二轮 mock 上游收到的 messages 中含 user role 且其 content 内有 tool_result block
//     且 tool_use_id 与 turn-1 返回值一致；
//   - 最终回答文本与 turn-2 mock 返回一致。
func TestANTSDKMockToolsMultiTurn(t *testing.T) {
	var turn1Body, turn2Body []byte

	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, body []byte) {
		switch {
		case turn1Body == nil:
			turn1Body = body
			writeMockMessageResponseRaw(w, map[string]any{
				"id":            "msg_tool_1",
				"type":          "message",
				"role":          "assistant",
				"model":         "deepseek-v4-flash",
				"stop_reason":   "tool_use",
				"stop_sequence": nil,
				"content": []map[string]any{
					{"type": "text", "text": "checking weather..."},
					{
						"type":  "tool_use",
						"id":    "toolu_1",
						"name":  "get_weather",
						"input": map[string]any{"location": "SF"},
					},
				},
				"usage": map[string]any{"input_tokens": 20, "output_tokens": 8},
			})
		default:
			turn2Body = body
			writeMockMessageResponseRaw(w, map[string]any{
				"id":            "msg_tool_2",
				"type":          "message",
				"role":          "assistant",
				"model":         "deepseek-v4-flash",
				"stop_reason":   "end_turn",
				"stop_sequence": nil,
				"content": []map[string]any{
					{"type": "text", "text": "It is sunny and 72F in SF."},
				},
				"usage": map[string]any{"input_tokens": 24, "output_tokens": 9},
			})
		}
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		Protocol:        "anthropic",
		AdapterKey:      "deepseek",
		UpstreamBaseURL: mock.URL,
	})

	client := anthropic.NewClient(
		option.WithBaseURL(f.AnthropicBaseURL),
		option.WithAPIKey(f.APIKey),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tools := []anthropic.ToolUnionParam{{
		OfTool: &anthropic.ToolParam{
			Name:        "get_weather",
			Description: anthropic.String("Get current weather"),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"location": map[string]any{"type": "string"},
				},
			},
		},
	}}

	// Turn 1.
	turn1, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(f.ModelID),
		MaxTokens: 32,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("What's the weather in SF?")),
		},
		Tools: tools,
	})
	if err != nil {
		t.Fatalf("turn1 call failed: %v", err)
	}
	if string(turn1.StopReason) != "tool_use" {
		t.Fatalf("turn1 stop_reason = %q, want tool_use", turn1.StopReason)
	}
	var toolUseID, toolUseName string
	for _, block := range turn1.Content {
		if block.Type == "tool_use" {
			tu := block.AsToolUse()
			toolUseID = tu.ID
			toolUseName = tu.Name
		}
	}
	if toolUseID != "toolu_1" || toolUseName != "get_weather" {
		t.Fatalf("turn1 tool_use id/name = %q/%q", toolUseID, toolUseName)
	}

	// Turn 2: send tool_result back.
	assistantBlocks := make([]anthropic.ContentBlockParamUnion, 0, len(turn1.Content))
	for _, block := range turn1.Content {
		assistantBlocks = append(assistantBlocks, block.ToParam())
	}
	turn2, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(f.ModelID),
		MaxTokens: 32,
		Tools:     tools,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("What's the weather in SF?")),
			{
				Role:    anthropic.MessageParamRoleAssistant,
				Content: assistantBlocks,
			},
			anthropic.NewUserMessage(
				anthropic.NewToolResultBlock(toolUseID, "sunny, 72F", false),
			),
		},
	})
	if err != nil {
		t.Fatalf("turn2 call failed: %v", err)
	}
	if len(turn2.Content) == 0 || turn2.Content[0].Text != "It is sunny and 72F in SF." {
		t.Fatalf("turn2 final text mismatch: %+v", turn2.Content)
	}

	// Assert turn2 upstream body actually contains tool_result block.
	if turn2Body == nil {
		t.Fatal("expected turn2 to hit upstream")
	}
	var turn2Decoded map[string]any
	if err := json.Unmarshal(turn2Body, &turn2Decoded); err != nil {
		t.Fatalf("turn2 body not valid json: %v", err)
	}
	messages, _ := turn2Decoded["messages"].([]any)
	foundToolResult := false
	for _, m := range messages {
		msg, _ := m.(map[string]any)
		if msg["role"] != "user" {
			continue
		}
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			blk, _ := b.(map[string]any)
			if blk["type"] == "tool_result" {
				if blk["tool_use_id"] == toolUseID {
					foundToolResult = true
				}
			}
		}
	}
	if !foundToolResult {
		t.Fatalf("turn2 upstream body missing tool_result with tool_use_id=%s; body=%s",
			toolUseID, string(turn2Body))
	}
}
