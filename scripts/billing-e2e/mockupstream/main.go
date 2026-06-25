// mock_upstream — 本地可控上游，仅用于 Phase 7 账单 E2E 的流式边界路线（B/C/D）。
//
// 行为由「请求体里用户消息文本包含的关键字」选择，便于 curl/Codex 直接驱动：
//   ROUTE_OK         正常 SSE：role + content + finish + usage + [DONE]（路线 A 对照）
//   ROUTE_D_NOUSAGE  SSE：role + content + finish(stop) + [DONE]，但省略 usage chunk（路线 D：已 emit 缺 final usage）
//   ROUTE_B_CLOSE    SSE：role + content 后直接断开连接（无 finish/usage/[DONE]）（路线 B：上游中断）
//   ROUTE_SLOW       首字节前 sleep 8s（配合 curl --max-time 触发「首 token 前取消」路线 C）
//   ROUTE_C_500      立即返回 500 JSON（路线 C：emit 前上游错误 → 释放、不计费）
//   ROUTE_BIGUSAGE   正常 SSE 但 usage 极大（配合低余额触发 write_off）
//
// 仅监听 127.0.0.1，本地测试用。支持 /v1/chat/completions（流式与非流式）。
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type chatRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

func lastUserText(req chatRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		// content 可能是字符串或数组；这里只做包含匹配，原样追加。
		b.Write(m.Content)
		b.WriteByte(' ')
	}
	return b.String()
}

func main() {
	addr := "127.0.0.1:8599"
	if v := os.Getenv("MOCK_ADDR"); v != "" {
		addr = v
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", handleChat)
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{}})
	})

	log.Printf("mock upstream listening on %s", addr)
	srv := &http.Server{Addr: addr, Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("mock upstream: %v", err)
	}
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
		return
	}
	text := lastUserText(req)

	switch {
	case strings.Contains(text, "ROUTE_C_500"):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"mock upstream forced 500","type":"server_error"}}`))
		return
	}

	if !req.Stream {
		// 非流式：返回一次性 JSON（供 WO-01 等用）。
		writeNonStream(w, req, text)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	if strings.Contains(text, "ROUTE_SLOW") {
		// 首字节前 sleep，给客户端在 emit 前取消的窗口（路线 C）。
		time.Sleep(8 * time.Second)
	}

	id := "chatcmpl-mock-" + fmt.Sprint(time.Now().UnixNano())
	model := req.Model
	send := func(payload string) {
		_, _ = w.Write([]byte("data: " + payload + "\n\n"))
		flusher.Flush()
	}

	// role 帧
	send(fmt.Sprintf(`{"id":%q,"object":"chat.completion.chunk","created":%d,"model":%q,"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`, id, time.Now().Unix(), model))
	// 两个内容帧
	send(fmt.Sprintf(`{"id":%q,"object":"chat.completion.chunk","created":%d,"model":%q,"choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`, id, time.Now().Unix(), model))
	time.Sleep(150 * time.Millisecond)
	send(fmt.Sprintf(`{"id":%q,"object":"chat.completion.chunk","created":%d,"model":%q,"choices":[{"index":0,"delta":{"content":" from mock upstream stream"},"finish_reason":null}]}`, id, time.Now().Unix(), model))

	if strings.Contains(text, "ROUTE_B_CLOSE") {
		// 已 emit 内容后直接断开（无 finish/usage/[DONE]）：模拟上游中断 → 网关路线 B。
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
				return
			}
		}
		// 兜底：无法 hijack 时直接 return（handler 结束，连接关闭）。
		return
	}

	if strings.Contains(text, "ROUTE_SLOW") {
		// 已 emit 后继续慢速 hang，给「emit 后客户端取消」窗口（路线 B），最终自然结束。
		for i := 0; i < 30; i++ {
			time.Sleep(1 * time.Second)
			send(fmt.Sprintf(`{"id":%q,"object":"chat.completion.chunk","created":%d,"model":%q,"choices":[{"index":0,"delta":{"content":"."},"finish_reason":null}]}`, id, time.Now().Unix(), model))
		}
	}

	// finish 帧
	send(fmt.Sprintf(`{"id":%q,"object":"chat.completion.chunk","created":%d,"model":%q,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, id, time.Now().Unix(), model))

	if !strings.Contains(text, "ROUTE_D_NOUSAGE") {
		// usage 帧（ROUTE_D 省略它 → 网关路线 D）。
		prompt, completion := 120, 6
		if strings.Contains(text, "ROUTE_BIGUSAGE") {
			prompt, completion = 200000, 120000
		}
		send(fmt.Sprintf(`{"id":%q,"object":"chat.completion.chunk","created":%d,"model":%q,"choices":[],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`, id, time.Now().Unix(), model, prompt, completion, prompt+completion))
	}

	send("[DONE]")
}

func writeNonStream(w http.ResponseWriter, req chatRequest, text string) {
	prompt, completion := 120, 6
	if strings.Contains(text, "ROUTE_BIGUSAGE") {
		prompt, completion = 200000, 120000
	}
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{
		"id":      "chatcmpl-mock-ns",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": "Hello from mock upstream (non-stream)."},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": prompt + completion},
	}
	_ = json.NewEncoder(w).Encode(resp)
}
