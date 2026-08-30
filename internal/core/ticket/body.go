package ticket

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// 正文（Tiptap JSON）的结构约束。
const (
	// MaxBodyJSONBytes 原始 JSON 字节上限。
	MaxBodyJSONBytes = 64 * 1024
	// MaxBodyTextChars 提取后纯文本的字符（rune）上限。
	MaxBodyTextChars = 10_000
	// MaxBodyImages 单条消息图片节点上限。
	MaxBodyImages = 8

	maxBodyNodes = 2_000
	maxBodyDepth = 20
	maxAltChars  = 200
	maxLangChars = 32
)

// AttachmentSrcPrefix 是图片节点 src 的稳定引用协议：attachment:{uuid}。
// 不存环境相关 URL；渲染时由前端把它替换成短时效签名 URL。
const AttachmentSrcPrefix = "attachment:"

// ErrBodyInvalid 表示正文不符合白名单 schema；details 通过 %w 包装附加。
var ErrBodyInvalid = errors.New("ticket body invalid")

// Body 是校验并规范化后的消息正文。
type Body struct {
	// JSON 是重建后的文档：只包含白名单字段，未知节点/marks/attrs 全部剔除或报错。
	JSON []byte
	// Text 是提取的纯文本（块间以换行分隔），列表预览与搜索用。
	Text string
	// AttachmentUIDs 是正文引用的附件 uid（按出现顺序，去重）。
	AttachmentUIDs []uuid.UUID
}

// node / markNode 对应 Tiptap 文档节点；只声明白名单字段，重建时其余字段自然丢弃。
type node struct {
	Type    string         `json:"type"`
	Text    string         `json:"text,omitempty"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Marks   []markNode     `json:"marks,omitempty"`
	Content []node         `json:"content,omitempty"`
}

type markNode struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

type bodyParser struct {
	nodeCount int
	images    int
	uids      []uuid.UUID
	seen      map[uuid.UUID]bool
	text      strings.Builder
}

// ParseBody 校验 Tiptap JSON 并返回规范化正文。
// 白名单：doc / paragraph / text / hardBreak / bulletList / orderedList / listItem /
// codeBlock / blockquote / image；marks：bold / italic / code / link（仅 http、https）。
func ParseBody(raw []byte) (Body, error) {
	if len(raw) == 0 {
		return Body{}, invalid("body is required")
	}
	if len(raw) > MaxBodyJSONBytes {
		return Body{}, invalid("body exceeds %d bytes", MaxBodyJSONBytes)
	}
	var root node
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(&root); err != nil {
		return Body{}, invalid("body is not valid JSON document")
	}
	if root.Type != "doc" {
		return Body{}, invalid("root node must be doc")
	}
	if len(root.Content) == 0 {
		return Body{}, invalid("body is empty")
	}

	p := &bodyParser{seen: map[uuid.UUID]bool{}}
	cleanedContent := make([]node, 0, len(root.Content))
	for _, child := range root.Content {
		cleaned, err := p.walkBlock(child, 1)
		if err != nil {
			return Body{}, err
		}
		cleanedContent = append(cleanedContent, cleaned)
	}

	text := strings.TrimSpace(p.text.String())
	if utf8.RuneCountInString(text) > MaxBodyTextChars {
		return Body{}, invalid("body text exceeds %d characters", MaxBodyTextChars)
	}
	if text == "" && p.images == 0 {
		return Body{}, invalid("body is empty")
	}

	cleanedDoc := node{Type: "doc", Content: cleanedContent}
	normalized, err := json.Marshal(cleanedDoc)
	if err != nil {
		return Body{}, fmt.Errorf("marshal normalized body: %w", err)
	}
	return Body{JSON: normalized, Text: text, AttachmentUIDs: p.uids}, nil
}

// walkBlock 校验块级节点（doc 直接子节点，以及 blockquote/listItem 的块级内容）。
func (p *bodyParser) walkBlock(n node, depth int) (node, error) {
	if err := p.enter(depth); err != nil {
		return node{}, err
	}
	switch n.Type {
	case "paragraph":
		return p.walkParagraph(n, depth)
	case "bulletList", "orderedList":
		return p.walkList(n, depth)
	case "codeBlock":
		return p.walkCodeBlock(n)
	case "blockquote":
		return p.walkBlockquote(n, depth)
	case "image":
		return p.walkImage(n)
	default:
		return node{}, invalid("node type %q is not allowed", n.Type)
	}
}

func (p *bodyParser) walkParagraph(n node, depth int) (node, error) {
	cleaned := node{Type: "paragraph"}
	for _, child := range n.Content {
		inline, err := p.walkInline(child, depth+1)
		if err != nil {
			return node{}, err
		}
		cleaned.Content = append(cleaned.Content, inline)
	}
	p.text.WriteString("\n")
	return cleaned, nil
}

func (p *bodyParser) walkInline(n node, depth int) (node, error) {
	if err := p.enter(depth); err != nil {
		return node{}, err
	}
	switch n.Type {
	case "text":
		if n.Text == "" {
			return node{}, invalid("text node must not be empty")
		}
		cleaned := node{Type: "text", Text: n.Text}
		for _, m := range n.Marks {
			cleanedMark, err := cleanMark(m)
			if err != nil {
				return node{}, err
			}
			cleaned.Marks = append(cleaned.Marks, cleanedMark)
		}
		p.text.WriteString(n.Text)
		return cleaned, nil
	case "hardBreak":
		p.text.WriteString("\n")
		return node{Type: "hardBreak"}, nil
	default:
		return node{}, invalid("inline node type %q is not allowed", n.Type)
	}
}

func (p *bodyParser) walkList(n node, depth int) (node, error) {
	if len(n.Content) == 0 {
		return node{}, invalid("%s must contain list items", n.Type)
	}
	cleaned := node{Type: n.Type}
	for _, item := range n.Content {
		if err := p.enter(depth + 1); err != nil {
			return node{}, err
		}
		if item.Type != "listItem" {
			return node{}, invalid("%s children must be listItem", n.Type)
		}
		cleanedItem := node{Type: "listItem"}
		for _, block := range item.Content {
			switch block.Type {
			case "paragraph", "bulletList", "orderedList":
				cleanedBlock, err := p.walkBlock(block, depth+2)
				if err != nil {
					return node{}, err
				}
				cleanedItem.Content = append(cleanedItem.Content, cleanedBlock)
			default:
				return node{}, invalid("listItem child %q is not allowed", block.Type)
			}
		}
		cleaned.Content = append(cleaned.Content, cleanedItem)
	}
	return cleaned, nil
}

func (p *bodyParser) walkCodeBlock(n node) (node, error) {
	cleaned := node{Type: "codeBlock"}
	if lang, ok := n.Attrs["language"].(string); ok && lang != "" {
		if len(lang) > maxLangChars || !isSimpleToken(lang) {
			return node{}, invalid("codeBlock language is not valid")
		}
		cleaned.Attrs = map[string]any{"language": lang}
	}
	for _, child := range n.Content {
		p.nodeCount++
		if child.Type != "text" || child.Text == "" || len(child.Marks) > 0 {
			return node{}, invalid("codeBlock content must be plain text")
		}
		cleaned.Content = append(cleaned.Content, node{Type: "text", Text: child.Text})
		p.text.WriteString(child.Text)
	}
	p.text.WriteString("\n")
	return cleaned, nil
}

func (p *bodyParser) walkBlockquote(n node, depth int) (node, error) {
	if len(n.Content) == 0 {
		return node{}, invalid("blockquote must not be empty")
	}
	cleaned := node{Type: "blockquote"}
	for _, block := range n.Content {
		switch block.Type {
		case "paragraph", "bulletList", "orderedList":
			cleanedBlock, err := p.walkBlock(block, depth+1)
			if err != nil {
				return node{}, err
			}
			cleaned.Content = append(cleaned.Content, cleanedBlock)
		default:
			return node{}, invalid("blockquote child %q is not allowed", block.Type)
		}
	}
	return cleaned, nil
}

func (p *bodyParser) walkImage(n node) (node, error) {
	p.images++
	if p.images > MaxBodyImages {
		return node{}, invalid("body exceeds %d images", MaxBodyImages)
	}
	src, _ := n.Attrs["src"].(string)
	uidText, ok := strings.CutPrefix(src, AttachmentSrcPrefix)
	if !ok {
		return node{}, invalid("image src must use %s{uid}", AttachmentSrcPrefix)
	}
	uid, err := uuid.Parse(uidText)
	if err != nil {
		return node{}, invalid("image src attachment uid is not valid")
	}
	attrs := map[string]any{"src": AttachmentSrcPrefix + uid.String()}
	if alt, ok := n.Attrs["alt"].(string); ok && alt != "" {
		if utf8.RuneCountInString(alt) > maxAltChars {
			return node{}, invalid("image alt exceeds %d characters", maxAltChars)
		}
		attrs["alt"] = alt
	}
	if !p.seen[uid] {
		p.seen[uid] = true
		p.uids = append(p.uids, uid)
	}
	return node{Type: "image", Attrs: attrs}, nil
}

func cleanMark(m markNode) (markNode, error) {
	switch m.Type {
	case "bold", "italic", "code":
		return markNode{Type: m.Type}, nil
	case "link":
		href, _ := m.Attrs["href"].(string)
		if !strings.HasPrefix(href, "https://") && !strings.HasPrefix(href, "http://") {
			return markNode{}, invalid("link href must be http or https")
		}
		return markNode{Type: "link", Attrs: map[string]any{"href": href}}, nil
	default:
		return markNode{}, invalid("mark type %q is not allowed", m.Type)
	}
}

func (p *bodyParser) enter(depth int) error {
	p.nodeCount++
	if p.nodeCount > maxBodyNodes {
		return invalid("body exceeds %d nodes", maxBodyNodes)
	}
	if depth > maxBodyDepth {
		return invalid("body nesting exceeds depth %d", maxBodyDepth)
	}
	return nil
}

func isSimpleToken(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '+', r == '_':
		default:
			return false
		}
	}
	return true
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBodyInvalid, fmt.Sprintf(format, args...))
}
