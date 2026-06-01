package deepseek

import (
	"encoding/json"
	"testing"

	anthropicadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

func userMessage(content string) anthropicadapter.Message {
	return anthropicadapter.Message{Role: "user", Content: json.RawMessage(content)}
}

// rejectParam 从 reject 错误中提取 "param" 字段值。
func rejectParam(err error) string {
	for _, field := range failure.FieldsOf(err) {
		if field.Key == "param" {
			if param, ok := field.Value.(string); ok {
				return param
			}
		}
	}
	return ""
}

func TestRejectUnsupportedRequestAccepts(t *testing.T) {
	cases := []struct {
		name string
		req  anthropicadapter.MessageRequest
	}{
		{"string shorthand", anthropicadapter.MessageRequest{Messages: []anthropicadapter.Message{userMessage(`"hi"`)}}},
		{"text block", anthropicadapter.MessageRequest{Messages: []anthropicadapter.Message{userMessage(`[{"type":"text","text":"hi"}]`)}}},
		{"thinking + tool_use", anthropicadapter.MessageRequest{Messages: []anthropicadapter.Message{
			userMessage(`[{"type":"thinking","thinking":"x"},{"type":"tool_use","id":"t1","name":"f","input":{}}]`),
		}}},
		{"custom tool", anthropicadapter.MessageRequest{
			Messages: []anthropicadapter.Message{userMessage(`"hi"`)},
			Tools:    json.RawMessage(`[{"name":"get_weather","input_schema":{"type":"object"}}]`),
		}},
		{"metadata user_id only", anthropicadapter.MessageRequest{
			Messages: []anthropicadapter.Message{userMessage(`"hi"`)},
			Metadata: json.RawMessage(`{"user_id":"u1"}`),
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := rejectUnsupportedRequest(tc.req); err != nil {
				t.Fatalf("unexpected reject: %v", err)
			}
		})
	}
}

func TestRejectUnsupportedRequestRejects(t *testing.T) {
	cases := []struct {
		name      string
		req       anthropicadapter.MessageRequest
		wantParam string
	}{
		{
			"image content block (silent-ignore danger)",
			anthropicadapter.MessageRequest{Messages: []anthropicadapter.Message{
				userMessage(`[{"type":"image","source":{"type":"base64","data":"x"}}]`),
			}},
			"messages.0.content.0.type",
		},
		{
			"redacted_thinking block",
			anthropicadapter.MessageRequest{Messages: []anthropicadapter.Message{
				userMessage(`[{"type":"redacted_thinking","data":"x"}]`),
			}},
			"messages.0.content.0.type",
		},
		{
			"built-in web_search tool",
			anthropicadapter.MessageRequest{
				Messages: []anthropicadapter.Message{userMessage(`"hi"`)},
				Tools:    json.RawMessage(`[{"type":"web_search_20250305","name":"web_search"}]`),
			},
			"tools.0.type",
		},
		{
			"metadata non user_id",
			anthropicadapter.MessageRequest{
				Messages: []anthropicadapter.Message{userMessage(`"hi"`)},
				Metadata: json.RawMessage(`{"user_id":"u1","session":"s1"}`),
			},
			"metadata.session",
		},
		{
			"container extension",
			anthropicadapter.MessageRequest{
				Messages:   []anthropicadapter.Message{userMessage(`"hi"`)},
				Extensions: map[string]json.RawMessage{"container": json.RawMessage(`"c1"`)},
			},
			"container",
		},
		{
			"output_config.format extension",
			anthropicadapter.MessageRequest{
				Messages:   []anthropicadapter.Message{userMessage(`"hi"`)},
				Extensions: map[string]json.RawMessage{"output_config": json.RawMessage(`{"format":{"type":"json_schema"}}`)},
			},
			"output_config.format",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectUnsupportedRequest(tc.req)
			if err == nil {
				t.Fatal("expected reject")
			}
			if code := failure.CodeOf(err); code != failure.CodeAdapterRequestUnsupported {
				t.Fatalf("code = %q, want %q", code, failure.CodeAdapterRequestUnsupported)
			}
			if param := rejectParam(err); param != tc.wantParam {
				t.Fatalf("param = %q, want %q", param, tc.wantParam)
			}
		})
	}
}
