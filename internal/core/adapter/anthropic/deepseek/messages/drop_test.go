package messages

import (
	"encoding/json"
	"reflect"
	"testing"

	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
)

func userMessage(content string) messagesadapter.Message {
	return messagesadapter.Message{Role: "user", Content: json.RawMessage(content)}
}

// TestDropUnsupportedKeepsSupportedRequest 验证全部受支持的请求不产生任何 Drop。
func TestDropUnsupportedKeepsSupportedRequest(t *testing.T) {
	cases := []struct {
		name string
		req  messagesadapter.MessageRequest
	}{
		{"string shorthand", messagesadapter.MessageRequest{Messages: []messagesadapter.Message{userMessage(`"hi"`)}}},
		{"text block", messagesadapter.MessageRequest{Messages: []messagesadapter.Message{userMessage(`[{"type":"text","text":"hi"}]`)}}},
		{"thinking + tool_use", messagesadapter.MessageRequest{Messages: []messagesadapter.Message{
			userMessage(`[{"type":"thinking","thinking":"x"},{"type":"tool_use","id":"t1","name":"f","input":{}}]`),
		}}},
		{"custom tool", messagesadapter.MessageRequest{
			Messages: []messagesadapter.Message{userMessage(`"hi"`)},
			Tools:    json.RawMessage(`[{"name":"get_weather","input_schema":{"type":"object"}}]`),
		}},
		{"metadata user_id only", messagesadapter.MessageRequest{
			Messages: []messagesadapter.Message{userMessage(`"hi"`)},
			Metadata: json.RawMessage(`{"user_id":"u1"}`),
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, dropped := dropUnsupported(tc.req)
			if len(dropped) != 0 {
				t.Fatalf("unexpected dropped fields: %v", dropped)
			}
		})
	}
}

// TestDropUnsupportedRemovesUnsupportedContentBlocks 验证不支持的 content block 被剔除，支持的保留。
func TestDropUnsupportedRemovesUnsupportedContentBlocks(t *testing.T) {
	req := messagesadapter.MessageRequest{
		Messages: []messagesadapter.Message{
			userMessage(`[{"type":"text","text":"hi"},{"type":"image","source":{"type":"base64","data":"x"}}]`),
		},
	}

	cleaned, dropped := dropUnsupported(req)

	var blocks []map[string]any
	if err := json.Unmarshal(cleaned.Messages[0].Content, &blocks); err != nil {
		t.Fatalf("unmarshal cleaned content: %v", err)
	}
	if len(blocks) != 1 || blocks[0]["type"] != "text" {
		t.Fatalf("expected only text block kept: %+v", blocks)
	}

	assertDropped(t, dropped, "messages")
}

// TestDropUnsupportedRemovesServerTools 验证内置 server tool 被剔除，client custom tool 保留。
func TestDropUnsupportedRemovesServerTools(t *testing.T) {
	req := messagesadapter.MessageRequest{
		Messages: []messagesadapter.Message{userMessage(`"hi"`)},
		Tools:    json.RawMessage(`[{"name":"get_weather","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search"}]`),
	}

	cleaned, dropped := dropUnsupported(req)

	var tools []map[string]any
	if err := json.Unmarshal(cleaned.Tools, &tools); err != nil {
		t.Fatalf("unmarshal cleaned tools: %v", err)
	}
	if len(tools) != 1 || tools[0]["name"] != "get_weather" {
		t.Fatalf("expected only custom tool kept: %+v", tools)
	}

	assertDropped(t, dropped, "tools")
}

// TestDropUnsupportedKeepsOnlyUserIDMetadata 验证 metadata 仅保留 user_id。
func TestDropUnsupportedKeepsOnlyUserIDMetadata(t *testing.T) {
	req := messagesadapter.MessageRequest{
		Messages: []messagesadapter.Message{userMessage(`"hi"`)},
		Metadata: json.RawMessage(`{"user_id":"u1","session":"s1"}`),
	}

	cleaned, dropped := dropUnsupported(req)

	var meta map[string]any
	if err := json.Unmarshal(cleaned.Metadata, &meta); err != nil {
		t.Fatalf("unmarshal cleaned metadata: %v", err)
	}
	if len(meta) != 1 || meta["user_id"] != "u1" {
		t.Fatalf("expected only user_id kept: %+v", meta)
	}

	assertDropped(t, dropped, "metadata")
}

// TestDropUnsupportedRemovesIgnoredExtensions 验证 DeepSeek 忽略的顶层 extension 被 Drop。
func TestDropUnsupportedRemovesIgnoredExtensions(t *testing.T) {
	req := messagesadapter.MessageRequest{
		Messages: []messagesadapter.Message{userMessage(`"hi"`)},
		Extensions: map[string]json.RawMessage{
			"container":     json.RawMessage(`"c1"`),
			"service_tier":  json.RawMessage(`"auto"`),
			"inference_geo": json.RawMessage(`"us"`),
			"mcp_servers":   json.RawMessage(`[]`),
		},
	}

	cleaned, dropped := dropUnsupported(req)

	for _, key := range []string{"container", "service_tier", "inference_geo", "mcp_servers"} {
		if _, ok := cleaned.Extensions[key]; ok {
			t.Fatalf("expected extension %q dropped", key)
		}
	}

	assertDropped(t, dropped, "container", "inference_geo", "mcp_servers", "service_tier")
}

// TestDropUnsupportedKeepsOutputConfigEffort 验证 output_config 仅剔除 format、保留 effort。
func TestDropUnsupportedKeepsOutputConfigEffort(t *testing.T) {
	req := messagesadapter.MessageRequest{
		Messages: []messagesadapter.Message{userMessage(`"hi"`)},
		Extensions: map[string]json.RawMessage{
			"output_config": json.RawMessage(`{"effort":"high","format":{"type":"json_schema"}}`),
		},
	}

	cleaned, dropped := dropUnsupported(req)

	var cfg map[string]any
	if err := json.Unmarshal(cleaned.Extensions["output_config"], &cfg); err != nil {
		t.Fatalf("unmarshal output_config: %v", err)
	}
	if _, ok := cfg["format"]; ok {
		t.Fatal("expected output_config.format dropped")
	}
	if cfg["effort"] != "high" {
		t.Fatalf("expected effort kept: %+v", cfg)
	}

	assertDropped(t, dropped, "output_config.format")
}

// TestDropUnsupportedNormalizesOutputConfigEffort 验证 effort 显式归一为 DeepSeek 的 high/max（U5，Adapt）。
func TestDropUnsupportedNormalizesOutputConfigEffort(t *testing.T) {
	cases := []struct {
		name      string
		effort    string
		wantValue string
	}{
		{"minimal", "minimal", "high"},
		{"low", "low", "high"},
		{"medium", "medium", "high"},
		{"high", "high", "high"},
		{"xhigh", "xhigh", "max"},
		{"max", "max", "max"},
		{"uppercase", "HIGH", "high"},
		{"padded", " low ", "high"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]string{"effort": tc.effort})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			req := messagesadapter.MessageRequest{
				Messages:   []messagesadapter.Message{userMessage(`"hi"`)},
				Extensions: map[string]json.RawMessage{"output_config": body},
			}

			cleaned, dropped := dropUnsupported(req)

			for _, d := range dropped {
				if d == "output_config.effort" {
					t.Fatalf("effort 归一是 Adapt，不应计入 dropped: %v", dropped)
				}
			}

			var cfg map[string]any
			if err := json.Unmarshal(cleaned.Extensions["output_config"], &cfg); err != nil {
				t.Fatalf("unmarshal output_config: %v", err)
			}
			if cfg["effort"] != tc.wantValue {
				t.Fatalf("effort = %v, want %q", cfg["effort"], tc.wantValue)
			}
		})
	}
}

// TestDropUnsupportedDropsUnknownOutputConfigEffort 验证未知 effort 被 Drop（让上游回退默认）。
func TestDropUnsupportedDropsUnknownOutputConfigEffort(t *testing.T) {
	req := messagesadapter.MessageRequest{
		Messages: []messagesadapter.Message{userMessage(`"hi"`)},
		Extensions: map[string]json.RawMessage{
			"output_config": json.RawMessage(`{"effort":"turbo"}`),
		},
	}

	cleaned, dropped := dropUnsupported(req)

	if _, ok := cleaned.Extensions["output_config"]; ok {
		t.Fatalf("expected empty output_config removed, got %s", cleaned.Extensions["output_config"])
	}

	assertDropped(t, dropped, "output_config.effort")
}

// TestDropUnsupportedNormalizesEffortAndDropsFormat 验证 effort 归一与 format 剔除并存。
func TestDropUnsupportedNormalizesEffortAndDropsFormat(t *testing.T) {
	req := messagesadapter.MessageRequest{
		Messages: []messagesadapter.Message{userMessage(`"hi"`)},
		Extensions: map[string]json.RawMessage{
			"output_config": json.RawMessage(`{"effort":"low","format":{"type":"json_schema"}}`),
		},
	}

	cleaned, dropped := dropUnsupported(req)

	var cfg map[string]any
	if err := json.Unmarshal(cleaned.Extensions["output_config"], &cfg); err != nil {
		t.Fatalf("unmarshal output_config: %v", err)
	}
	if _, ok := cfg["format"]; ok {
		t.Fatal("expected format dropped")
	}
	if cfg["effort"] != "high" {
		t.Fatalf("effort = %v, want high", cfg["effort"])
	}

	assertDropped(t, dropped, "output_config.format")
}

// TestDropUnsupportedDropsTopK 验证 typed top_k 被 Drop。
func TestDropUnsupportedDropsTopK(t *testing.T) {
	k := 5
	req := messagesadapter.MessageRequest{
		Messages: []messagesadapter.Message{userMessage(`"hi"`)},
		TopK:     &k,
	}

	cleaned, dropped := dropUnsupported(req)

	if cleaned.TopK != nil {
		t.Fatal("expected top_k dropped")
	}

	assertDropped(t, dropped, "top_k")
}

func assertDropped(t *testing.T, got []string, want ...string) {
	t.Helper()

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dropped = %v, want %v", got, want)
	}
}
