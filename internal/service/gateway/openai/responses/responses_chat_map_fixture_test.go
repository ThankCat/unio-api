package responses

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
)

// codexFixtureGlob 指向真实 Codex /v1/responses 抓包（开发期手工抓包落盘）。
const codexFixtureGlob = "../../../../blackbox/fixtures/codex/*_POST_v1_responses.json"

// TestMapRealCodexFixtureToChat 用真实 v0.130 抓包端到端验证 TASK-11.05 翻译：
// 真实请求体经 ingress decode → mapResponsesRequestToChat → 产出可喂给 openai.ChatAdapter 的 ChatRequest。
func TestMapRealCodexFixtureToChat(t *testing.T) {
	matches, err := filepath.Glob(codexFixtureGlob)
	if err != nil {
		t.Fatalf("glob codex fixtures: %v", err)
	}
	if len(matches) == 0 {
		t.Skip("no real Codex fixture captured under internal/blackbox/fixtures/codex/")
	}

	for _, path := range matches {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var req gatewayapi.ResponsesRequest
			if err := json.Unmarshal(data, &req); err != nil {
				t.Fatalf("decode fixture: %v", err)
			}

			chat, tr := mapResponsesRequestToChat(req, "deepseek-chat")

			if chat.Model != "deepseek-chat" {
				t.Errorf("expected upstream model, got %q", chat.Model)
			}
			if len(chat.Messages) == 0 {
				t.Fatal("expected non-empty messages")
			}
			// 抓包首条是 instructions→system；input 含 developer + user 消息。
			if chat.Messages[0].Role != "system" || chat.Messages[0].ContentString() == "" {
				t.Errorf("expected first message system instructions, got role=%q", chat.Messages[0].Role)
			}
			assertRoles(t, chat.Messages, "developer", "user")

			// 工具：function 扁平 + namespace 拍平；至少有一个拍平后的 MCP 工具名。
			if len(chat.Tools) == 0 {
				t.Fatal("expected function tools after flatten")
			}
			var sawExec, sawFlatMCP bool
			for _, tool := range chat.Tools {
				if tool.Type != "function" {
					t.Errorf("expected only function tools after flatten, got %q", tool.Type)
				}
				if tool.Function.Name == "exec_command" {
					sawExec = true
				}
				if strings.HasPrefix(tool.Function.Name, "mcp__") {
					sawFlatMCP = true
				}
			}
			if !sawExec {
				t.Error("expected exec_command function tool")
			}
			if !sawFlatMCP {
				t.Error("expected flattened mcp__* tool name")
			}

			// 内置工具与 Codex 专属字段进 Drop 审计。
			if !contains(tr.DroppedFields, "tools.web_search") || !contains(tr.DroppedFields, "tools.image_generation") {
				t.Errorf("expected builtin tools dropped, got %v", tr.DroppedFields)
			}
			if !contains(tr.DroppedFields, "client_metadata") {
				t.Errorf("expected client_metadata dropped, got %v", tr.DroppedFields)
			}
		})
	}
}

func assertRoles(t *testing.T, msgs []openai.ChatMessage, wantRoles ...string) {
	t.Helper()
	present := map[string]bool{}
	for _, m := range msgs {
		present[m.Role] = true
	}
	for _, role := range wantRoles {
		if !present[role] {
			t.Errorf("expected a %q message in translated messages", role)
		}
	}
}
