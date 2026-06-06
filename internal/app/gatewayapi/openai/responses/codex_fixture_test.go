package responses

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// codexFixtureGlob 指向 codexcapture 工具落盘的真实 Codex /v1/responses 抓包
// （internal/blackbox/fixtures/codex/*_POST_v1_responses.json）。
// 本测试确保 ingress 能解析并接受真实 Codex v0.130 请求体：缺抓包时跳过，不阻断 CI。
const codexFixtureGlob = "../../../../blackbox/fixtures/codex/*_POST_v1_responses.json"

// TestDecodeRealCodexResponsesFixture 用真实抓包验证 TASK-11.04 的 decode + validation：
//   - 顶层混合工具（function / namespace MCP / 内置 web_search / image_generation）不被 Reject；
//   - Codex 专属 client_metadata 落入 Extensions（DEC-012 decode 不丢字段）；
//   - reasoning:null → nil；input item 数组、instructions、prompt_cache_key 正常解析。
func TestDecodeRealCodexResponsesFixture(t *testing.T) {
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
				t.Fatalf("read fixture %s: %v", path, err)
			}

			var req ResponsesRequest
			if err := json.Unmarshal(data, &req); err != nil {
				t.Fatalf("decode real Codex fixture: %v", err)
			}
			if validationErr := validateResponsesRequest(req); validationErr != nil {
				t.Fatalf("validate real Codex fixture: param=%q message=%q", validationErr.param, validationErr.message)
			}

			if req.Model == "" {
				t.Error("expected non-empty model")
			}
			if req.Instructions == nil || *req.Instructions == "" {
				t.Error("expected Codex instructions to decode")
			}
			if len(req.Input.Items) == 0 {
				t.Fatalf("expected input items array, got text=%v", req.Input.Text)
			}
			if req.Reasoning != nil {
				t.Errorf("expected reasoning:null → nil, got %+v", req.Reasoning)
			}
			if !req.HasExtension("client_metadata") {
				t.Error("expected Codex-specific client_metadata preserved in Extensions")
			}

			assertCodexToolShapes(t, req.Tools)
		})
	}
}

// assertCodexToolShapes 校验真实抓包里出现过的工具形态都被 ingress 接受：
// 至少一个 function、至少一个 namespace（MCP 分组），且内置工具（web_search/image_generation）
// 这类无 name/parameters 的 type 不被 Reject。
func assertCodexToolShapes(t *testing.T, tools []ResponsesTool) {
	t.Helper()
	if len(tools) == 0 {
		t.Skip("fixture carries no tools")
	}

	var sawFunction, sawNamespace, sawBuiltin bool
	for _, tool := range tools {
		switch tool.Type {
		case toolTypeFunction:
			sawFunction = true
		case toolTypeNamespace:
			sawNamespace = true
			if len(tool.Tools) == 0 {
				t.Errorf("namespace tool %q decoded with no nested tools", tool.Name)
			}
		case "":
			t.Errorf("tool decoded with empty type: %+v", tool)
		default:
			// web_search / image_generation 等内置工具：无 name/parameters，仍须被接受。
			sawBuiltin = true
		}
	}

	if !sawFunction {
		t.Error("expected at least one function tool in Codex fixture")
	}
	if !sawNamespace {
		t.Error("expected at least one namespace (MCP) tool in Codex fixture")
	}
	if !sawBuiltin {
		t.Log("note: fixture carried no builtin tool types (web_search/image_generation)")
	}
}
