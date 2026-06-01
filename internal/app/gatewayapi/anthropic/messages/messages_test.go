package messages

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestMessageRequestDecodeKeepsTypedAndExtensions(t *testing.T) {
	var req MessageRequest
	err := json.Unmarshal([]byte(`{
		"model": "claude-sonnet-4",
		"max_tokens": 64,
		"system": "be brief",
		"messages": [{"role":"user","content":"hi"}],
		"vendor_hint": {"x": 1}
	}`), &req)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if req.Model != "claude-sonnet-4" {
		t.Fatalf("model = %q", req.Model)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 64 {
		t.Fatalf("max_tokens = %v", req.MaxTokens)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
		t.Fatalf("messages = %#v", req.Messages)
	}
	if string(req.System) != `"be brief"` {
		t.Fatalf("system = %s", req.System)
	}
	if !req.HasExtension("vendor_hint") {
		t.Fatalf("expected vendor_hint kept in extensions, got %#v", req.Extensions)
	}
	if req.HasExtension("model") {
		t.Fatal("typed field model must not leak into extensions")
	}
}

func TestMessageRequestDecodeRejectsMCPServers(t *testing.T) {
	var req MessageRequest
	err := json.Unmarshal([]byte(`{"model":"x","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"mcp_servers":[]}`), &req)
	if err == nil {
		t.Fatal("expected reject error for mcp_servers")
	}

	var rejectErr *messageRequestRejectError
	if !errors.As(err, &rejectErr) {
		t.Fatalf("expected messageRequestRejectError, got %T", err)
	}
	if rejectErr.param != "mcp_servers" {
		t.Fatalf("param = %q, want mcp_servers", rejectErr.param)
	}
}

func TestValidateMessageRequest(t *testing.T) {
	maxTokens := func(v int) *int { return &v }
	temp := func(v float64) *float64 { return &v }
	validMessages := []Message{{Role: "user", Content: json.RawMessage(`"hi"`)}}

	tests := []struct {
		name      string
		req       MessageRequest
		wantParam string // "" means valid
	}{
		{"valid", MessageRequest{Model: "m", MaxTokens: maxTokens(16), Messages: validMessages}, ""},
		{"valid zero max_tokens", MessageRequest{Model: "m", MaxTokens: maxTokens(0), Messages: validMessages}, ""},
		{"missing model", MessageRequest{MaxTokens: maxTokens(16), Messages: validMessages}, "model"},
		{"missing max_tokens", MessageRequest{Model: "m", Messages: validMessages}, "max_tokens"},
		{"negative max_tokens", MessageRequest{Model: "m", MaxTokens: maxTokens(-1), Messages: validMessages}, "max_tokens"},
		{"missing messages", MessageRequest{Model: "m", MaxTokens: maxTokens(16)}, "messages"},
		{"bad role", MessageRequest{Model: "m", MaxTokens: maxTokens(16), Messages: []Message{{Role: "tool", Content: json.RawMessage(`"x"`)}}}, "messages.0.role"},
		{"empty content", MessageRequest{Model: "m", MaxTokens: maxTokens(16), Messages: []Message{{Role: "user"}}}, "messages.0.content"},
		{"temperature too high", MessageRequest{Model: "m", MaxTokens: maxTokens(16), Messages: validMessages, Temperature: temp(1.5)}, "temperature"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMessageRequest(tt.req)
			if tt.wantParam == "" {
				if err != nil {
					t.Fatalf("expected valid, got %+v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error for param %q", tt.wantParam)
			}
			if err.param != tt.wantParam {
				t.Fatalf("param = %q, want %q (message %q)", err.param, tt.wantParam, err.message)
			}
		})
	}
}
