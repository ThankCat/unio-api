package messages

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/app/gatewayapi/anthropic"
)

func TestEncodeStreamEventFraming(t *testing.T) {
	frame, err := EncodeStreamEvent(StreamContentBlockDelta{
		Type:  "content_block_delta",
		Index: 0,
		Delta: ContentBlockDelta{Type: "text_delta", Text: "hi"},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	got := string(frame)
	if !strings.HasPrefix(got, "event: content_block_delta\n") {
		t.Fatalf("missing event line: %q", got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("frame must end with blank line: %q", got)
	}

	dataLine := ""
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if strings.HasPrefix(line, "data: ") {
			dataLine = strings.TrimPrefix(line, "data: ")
		}
	}
	if dataLine == "" {
		t.Fatalf("missing data line: %q", got)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(dataLine), &payload); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	// data.type 必须与 event 名一致。
	if string(payload["type"]) != `"content_block_delta"` {
		t.Fatalf("data type = %s", payload["type"])
	}

	var delta map[string]string
	if err := json.Unmarshal(payload["delta"], &delta); err != nil {
		t.Fatalf("unmarshal delta: %v", err)
	}
	if delta["type"] != "text_delta" || delta["text"] != "hi" {
		t.Fatalf("delta = %#v", delta)
	}
	// text_delta 不应携带其它 union 字段。
	if _, ok := delta["partial_json"]; ok {
		t.Fatal("text_delta should not carry partial_json")
	}
}

func TestStreamEventNamesMatchType(t *testing.T) {
	cases := []struct {
		event StreamEvent
		want  string
	}{
		{StreamMessageStart{Type: "message_start"}, "message_start"},
		{StreamContentBlockStart{Type: "content_block_start"}, "content_block_start"},
		{StreamContentBlockStop{Type: "content_block_stop"}, "content_block_stop"},
		{StreamMessageDelta{Type: "message_delta"}, "message_delta"},
		{StreamMessageStop{Type: "message_stop"}, "message_stop"},
		{StreamPing{Type: "ping"}, "ping"},
		{StreamError{Type: "error", Error: anthropic.ErrorBody{Type: "overloaded_error", Message: "busy"}}, "error"},
	}

	for _, tc := range cases {
		if got := tc.event.EventName(); got != tc.want {
			t.Errorf("EventName() = %q, want %q", got, tc.want)
		}

		raw, err := json.Marshal(tc.event)
		if err != nil {
			t.Fatalf("marshal %s: %v", tc.want, err)
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("unmarshal %s: %v", tc.want, err)
		}
		if string(payload["type"]) != `"`+tc.want+`"` {
			t.Errorf("%s data type = %s", tc.want, payload["type"])
		}
	}
}
