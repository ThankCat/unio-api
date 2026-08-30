package ticket

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestParseBodyNormalizesAndExtracts(t *testing.T) {
	uid := uuid.New()
	raw := fmt.Sprintf(`{
		"type": "doc",
		"content": [
			{
				"type": "paragraph",
				"attrs": {"textAlign": "left", "onclick": "alert(1)"},
				"content": [
					{"type": "text", "text": "余额", "marks": [{"type": "bold"}]},
					{"type": "text", "text": "未到账 🙏"},
					{"type": "hardBreak"},
					{"type": "text", "text": "订单", "marks": [{"type": "link", "attrs": {"href": "https://example.com", "class": "x"}}]}
				]
			},
			{"type": "image", "attrs": {"src": "attachment:%s", "alt": "截图", "title": "drop-me"}},
			{
				"type": "bulletList",
				"content": [
					{"type": "listItem", "content": [{"type": "paragraph", "content": [{"type": "text", "text": "第一条"}]}]}
				]
			},
			{"type": "codeBlock", "attrs": {"language": "json"}, "content": [{"type": "text", "text": "{\"a\":1}"}]}
		]
	}`, uid)

	body, err := ParseBody([]byte(raw))
	if err != nil {
		t.Fatalf("ParseBody: %v", err)
	}
	if len(body.AttachmentUIDs) != 1 || body.AttachmentUIDs[0] != uid {
		t.Fatalf("attachment uids = %v, want [%s]", body.AttachmentUIDs, uid)
	}
	for _, want := range []string{"余额", "未到账 🙏", "订单", "第一条", `{"a":1}`} {
		if !strings.Contains(body.Text, want) {
			t.Fatalf("text %q missing %q", body.Text, want)
		}
	}

	normalized := string(body.JSON)
	for _, forbidden := range []string{"onclick", "textAlign", "title", "class", "drop-me"} {
		if strings.Contains(normalized, forbidden) {
			t.Fatalf("normalized JSON still contains %q: %s", forbidden, normalized)
		}
	}
	var doc map[string]any
	if err := json.Unmarshal(body.JSON, &doc); err != nil {
		t.Fatalf("normalized JSON invalid: %v", err)
	}
}

func TestParseBodyRejectsDisallowedContent(t *testing.T) {
	uid := uuid.New()
	cases := map[string]string{
		"unknown node":     `{"type":"doc","content":[{"type":"iframe"}]}`,
		"unknown mark":     `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"x","marks":[{"type":"underline"}]}]}]}`,
		"javascript link":  `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"x","marks":[{"type":"link","attrs":{"href":"javascript:alert(1)"}}]}]}]}`,
		"external image":   `{"type":"doc","content":[{"type":"image","attrs":{"src":"https://evil.example/x.png"}}]}`,
		"bad image uid":    `{"type":"doc","content":[{"type":"image","attrs":{"src":"attachment:not-a-uuid"}}]}`,
		"empty doc":        `{"type":"doc","content":[]}`,
		"blank paragraph":  `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"  "}]}]}`,
		"wrong root":       `{"type":"paragraph","content":[{"type":"text","text":"x"}]}`,
		"codeBlock marked": `{"type":"doc","content":[{"type":"codeBlock","content":[{"type":"text","text":"x","marks":[{"type":"bold"}]}]}]}`,
	}
	// blank paragraph 只有空白文本且无图片，应报空正文。
	_ = uid
	for name, raw := range cases {
		if _, err := ParseBody([]byte(raw)); !errors.Is(err, ErrBodyInvalid) {
			t.Fatalf("%s: err = %v, want ErrBodyInvalid", name, err)
		}
	}
}

func TestParseBodyLimits(t *testing.T) {
	big := `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"` +
		strings.Repeat("a", MaxBodyJSONBytes) + `"}]}]}`
	if _, err := ParseBody([]byte(big)); !errors.Is(err, ErrBodyInvalid) {
		t.Fatalf("oversized JSON: err = %v, want ErrBodyInvalid", err)
	}

	images := make([]string, 0, MaxBodyImages+1)
	for range MaxBodyImages + 1 {
		images = append(images, fmt.Sprintf(`{"type":"image","attrs":{"src":"attachment:%s"}}`, uuid.New()))
	}
	tooManyImages := `{"type":"doc","content":[` + strings.Join(images, ",") + `]}`
	if _, err := ParseBody([]byte(tooManyImages)); !errors.Is(err, ErrBodyInvalid) {
		t.Fatalf("too many images: err = %v, want ErrBodyInvalid", err)
	}

	deep := strings.Repeat(`{"type":"blockquote","content":[`, maxBodyDepth+1) +
		`{"type":"paragraph","content":[{"type":"text","text":"x"}]}` +
		strings.Repeat(`]}`, maxBodyDepth+1)
	if _, err := ParseBody([]byte(`{"type":"doc","content":[` + deep + `]}`)); !errors.Is(err, ErrBodyInvalid) {
		t.Fatalf("deep nesting: err = %v, want ErrBodyInvalid", err)
	}
}

func TestParseBodyImageOnlyIsValid(t *testing.T) {
	raw := fmt.Sprintf(`{"type":"doc","content":[{"type":"image","attrs":{"src":"attachment:%s"}}]}`, uuid.New())
	body, err := ParseBody([]byte(raw))
	if err != nil {
		t.Fatalf("ParseBody: %v", err)
	}
	if body.Text != "" {
		t.Fatalf("text = %q, want empty", body.Text)
	}
	if len(body.AttachmentUIDs) != 1 {
		t.Fatalf("attachment uids = %v, want 1", body.AttachmentUIDs)
	}
}
