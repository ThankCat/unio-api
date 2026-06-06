// Command codexcapture 是阶段 11 的开发期抓包工具（非生产代码）。
//
// 用途：把 Codex CLI 指向本工具的 /v1，捕获真实的 /v1/responses* 请求体，落盘到
// internal/blackbox/fixtures/codex/，供 TASK-11.01 字段冻结与 TASK-11.15 黑盒 fixture 使用。
//
// 安全：只 dump 请求 body，绝不记录任何 header（Authorization 里有 API key）。
// 落盘文件不含 credential；本工具不连数据库、不计费。
//
// 两种模式：
//   - canned（默认，无需任何上游）：返回最小可用的 Responses 响应，Codex 能完成一轮，
//     稳定捕获 Codex 发出的首个真实请求（含 instructions / input / tools / reasoning / text / include）。
//   - forward（设 FORWARD_URL）：把请求透传到真实上游（如 https://api.openai.com），
//     真实响应原样回给 Codex，可自然捕获多轮 / 工具往返 / compact。FORWARD_KEY 作为上游 bearer。
//
// 运行：
//   go run ./tools/codexcapture
//   OUT_DIR=internal/blackbox/fixtures/codex ADDR=127.0.0.1:8899 go run ./tools/codexcapture
//   FORWARD_URL=https://api.openai.com FORWARD_KEY=sk-... go run ./tools/codexcapture
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

func main() {
	addr := getenv("ADDR", "127.0.0.1:8899")
	outDir := getenv("OUT_DIR", "internal/blackbox/fixtures/codex")
	model := getenv("MODEL", "gpt-5-codex")
	forwardURL := strings.TrimRight(os.Getenv("FORWARD_URL"), "/")
	forwardKey := os.Getenv("FORWARD_KEY")

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatalf("codexcapture: mkdir %s: %v", outDir, err)
	}

	srv := &captureServer{
		outDir:     outDir,
		model:      model,
		forwardURL: forwardURL,
		forwardKey: forwardKey,
		client:     &http.Client{Timeout: 5 * time.Minute},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", srv.handleModels)
	mux.HandleFunc("/v1/responses", srv.handleResponses)
	mux.HandleFunc("/v1/responses/", srv.handleResponses) // compact / input_tokens / {id}/...
	mux.HandleFunc("/", srv.handleCatchAll)

	mode := "canned"
	if forwardURL != "" {
		mode = "forward → " + forwardURL
	}
	log.Printf("codexcapture: listening on http://%s/v1 (mode=%s, out=%s, model=%s)", addr, mode, outDir, model)
	log.Printf("codexcapture: 把 Codex 的 base_url 配成 http://%s/v1（api_key 随便填）", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("codexcapture: serve: %v", err)
	}
}

type captureServer struct {
	outDir     string
	model      string
	forwardURL string
	forwardKey string
	client     *http.Client
	seq        int64
}

func (s *captureServer) handleModels(w http.ResponseWriter, r *http.Request) {
	if s.forwardURL != "" {
		s.forward(w, r, nil)
		return
	}
	// Codex v0.130 的 codex_models_manager 期望 `{"models":[ModelInfo...]}`
	// （ModelInfo 仅 slug/display_name 必填），收到标准 OpenAI `{"data":...}` 会记 benign
	// "missing field models" 错误。这里同时给 `models`（消除噪声）与 `data`/`object`（OpenAI 兼容）。
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"id": s.model, "object": "model", "created": time.Now().Unix(), "owned_by": "unio-codexcapture"},
		},
		"models": []map[string]any{
			{"slug": s.model, "display_name": s.model},
		},
	})
}

func (s *captureServer) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// Codex 几乎不发 GET /responses/{id}；有则 dump 并回 501-ish，便于观察调用面。
		s.dump(r, nil)
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error": map[string]any{"type": "api_error", "code": "unsupported_endpoint_stateless", "message": "codexcapture: stateless"},
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	s.dump(r, body)

	if s.forwardURL != "" {
		s.forward(w, r, body)
		return
	}

	switch {
	case strings.HasSuffix(r.URL.Path, "/input_tokens"):
		s.cannedInputTokens(w, body)
	case strings.HasSuffix(r.URL.Path, "/compact"):
		s.cannedCompact(w, body)
	default:
		s.cannedResponses(w, body)
	}
}

func (s *captureServer) handleCatchAll(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(r.Body, 8<<20))
	}
	s.dump(r, body)
	if s.forwardURL != "" {
		s.forward(w, r, body)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "noop", "note": "codexcapture catch-all"})
}

// dump 只落盘请求 body（不含任何 header），文件名带时间戳与 endpoint，便于回溯。
func (s *captureServer) dump(r *http.Request, body []byte) {
	ts := time.Now().Format("20060102_150405.000")
	slug := sanitize(r.Method + r.URL.Path)
	name := fmt.Sprintf("%s_%s.json", ts, slug)
	path := filepath.Join(s.outDir, name)

	out := body
	if pretty, ok := prettyJSON(body); ok {
		out = pretty
	}
	if len(out) == 0 {
		out = []byte("{}\n")
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		log.Printf("codexcapture: WARN write %s: %v", path, err)
		return
	}
	log.Printf("codexcapture: %s %s  (%d bytes) → %s", r.Method, r.URL.Path, len(body), path)
}

// forward 透传到真实上游，并把响应原样回给 Codex（SSE 也是逐字节 copy）。
func (s *captureServer) forward(w http.ResponseWriter, r *http.Request, body []byte) {
	url := s.forwardURL + r.URL.Path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, rdr)
	if err != nil {
		http.Error(w, "build upstream request", http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", r.Header.Get("Accept"))
	if s.forwardKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.forwardKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
}

func (s *captureServer) cannedResponses(w http.ResponseWriter, body []byte) {
	model := s.modelFrom(body)
	if isStream(body) {
		s.cannedStream(w, model)
		return
	}
	writeJSON(w, http.StatusOK, s.responseObject(model, "completed"))
}

func (s *captureServer) cannedStream(w http.ResponseWriter, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusOK, s.responseObject(model, "completed"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	const text = "[unio codexcapture] request captured; see internal/blackbox/fixtures/codex/."
	msg := map[string]any{"id": "msg_capture", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}
	emit := func(event string, data map[string]any) {
		data["type"] = event
		data["sequence_number"] = atomic.AddInt64(&s.seq, 1) - 1
		payload, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
		flusher.Flush()
	}

	emit("response.created", map[string]any{"response": s.responseObject(model, "in_progress")})
	emit("response.in_progress", map[string]any{"response": s.responseObject(model, "in_progress")})
	emit("response.output_item.added", map[string]any{"output_index": 0, "item": msg})
	emit("response.content_part.added", map[string]any{"item_id": "msg_capture", "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": ""}})
	emit("response.output_text.delta", map[string]any{"item_id": "msg_capture", "output_index": 0, "content_index": 0, "delta": text})
	emit("response.output_text.done", map[string]any{"item_id": "msg_capture", "output_index": 0, "content_index": 0, "text": text})
	emit("response.content_part.done", map[string]any{"item_id": "msg_capture", "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": text}})

	doneMsg := map[string]any{"id": "msg_capture", "type": "message", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
	emit("response.output_item.done", map[string]any{"output_index": 0, "item": doneMsg})

	final := s.responseObject(model, "completed")
	final["output"] = []any{doneMsg}
	emit("response.completed", map[string]any{"response": final})
}

func (s *captureServer) cannedInputTokens(w http.ResponseWriter, body []byte) {
	// 粗略估算：body 长度 / 4，仅用于让 Codex 预检不报错。
	writeJSON(w, http.StatusOK, map[string]any{
		"object":       "response.input_tokens",
		"input_tokens": len(body) / 4,
	})
}

func (s *captureServer) cannedCompact(w http.ResponseWriter, body []byte) {
	// codex-rs CompactHistoryResponse 期望 {"output":[ResponseItem...]}（非完整 response 对象）。
	// 返回一条可稳定反序列化的 message item 作为压缩后历史。
	writeJSON(w, http.StatusOK, map[string]any{
		"output": []any{
			map[string]any{
				"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "[unio codexcapture] compacted history placeholder"}},
			},
		},
	})
}

func (s *captureServer) responseObject(model, status string) map[string]any {
	return map[string]any{
		"id": "resp_capture", "object": "response", "status": status,
		"created_at": time.Now().Unix(), "model": model,
		"output": []any{},
		"usage":  map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
	}
}

func (s *captureServer) modelFrom(body []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &v) == nil && v.Model != "" {
		return v.Model
	}
	return s.model
}

func isStream(body []byte) bool {
	var v struct {
		Stream *bool `json:"stream"`
	}
	if json.Unmarshal(body, &v) == nil && v.Stream != nil {
		return *v.Stream
	}
	return false
}

func prettyJSON(body []byte) ([]byte, bool) {
	var v any
	if json.Unmarshal(body, &v) != nil {
		return nil, false
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, false
	}
	return append(out, '\n'), true
}

func sanitize(s string) string {
	s = strings.TrimPrefix(s, "/")
	repl := strings.NewReplacer("/", "_", " ", "_", ":", "_", "?", "_")
	out := repl.Replace(s)
	if out == "" {
		return "root"
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
