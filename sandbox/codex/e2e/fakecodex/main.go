// fakecodex 是号池 E2E 的本地假 Codex 上游：按 wire 样例的形状服务
// POST /backend-api/codex/responses（SSE + x-codex-* 用量头）。
//
// 用途：真实账号令牌被吊销时，仍能把「成功传输」的号池全链路（账号出站身份 → 用量头解析 →
// 快照落库 → 阈值暂停 → final_account_id → Sticky 账号绑定）跑通。上游行为逐字段对照
// sandbox/codex/wire/samples/upstream-usage-headers.json 与 upstream-usage-completed.json。
//
// 用法：go run ./sandbox/codex/e2e/fakecodex --port 18998 --primary-used 95
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

func main() {
	port := flag.Int("port", 18998, "listen port")
	primaryUsed := flag.Float64("primary-used", 95, "x-codex-primary-used-percent")
	flag.Parse()

	http.HandleFunc("/backend-api/codex/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.5","display_name":"GPT-5.5","visibility":"list"}]}`))
	})

	http.HandleFunc("/backend-api/codex/responses", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" || r.Header.Get("chatgpt-account-id") == "" {
			http.Error(w, `{"error":{"code":"missing_account_identity"}}`, http.StatusUnauthorized)
			return
		}
		now := time.Now().Unix()
		header := w.Header()
		header.Set("Content-Type", "text/event-stream")
		// 用量头按 wire 样例：primary=5h 窗口、secondary=7d 窗口。
		header.Set("x-codex-plan-type", "plus")
		header.Set("x-codex-primary-used-percent", strconv.FormatFloat(*primaryUsed, 'f', -1, 64))
		header.Set("x-codex-primary-window-minutes", "300")
		header.Set("x-codex-primary-reset-after-seconds", "3600")
		header.Set("x-codex-primary-reset-at", strconv.FormatInt(now+3600, 10))
		header.Set("x-codex-secondary-used-percent", "12")
		header.Set("x-codex-secondary-window-minutes", "10080")
		header.Set("x-codex-secondary-reset-after-seconds", "604800")
		header.Set("x-codex-secondary-reset-at", strconv.FormatInt(now+604800, 10))
		header.Set("x-codex-turn-state", "gAAAAABfakestate")
		w.WriteHeader(http.StatusOK)

		var body struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		// 非流式：直接回完整 response JSON（真实 codex 后端同样支持两种形态）。
		if !body.Stream {
			header.Set("Content-Type", "application/json")
			message := map[string]any{
				"type": "message", "id": "msg_fake_1", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "pong-fake", "annotations": []any{}}},
			}
			payload := map[string]any{
				"id": "resp_fakecodex_1", "object": "response", "status": "completed",
				"created_at": time.Now().Unix(), "model": body.Model, "service_tier": "auto",
				"output": []any{message},
				"usage": map[string]any{
					"input_tokens": 42, "input_tokens_details": map[string]any{"cached_tokens": 0},
					"output_tokens": 3, "output_tokens_details": map[string]any{"reasoning_tokens": 0},
					"total_tokens": 45,
				},
			}
			_ = json.NewEncoder(w).Encode(payload)
			return
		}

		flusher := w.(http.Flusher)
		emit := func(event string, payload map[string]any) {
			raw, _ := json.Marshal(payload)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
			flusher.Flush()
		}
		responseID := "resp_fakecodex_1"
		emit("response.created", map[string]any{
			"type": "response.created",
			"response": map[string]any{
				"id": responseID, "object": "response", "status": "in_progress",
				"model": body.Model, "output": []any{},
			},
		})
		message := map[string]any{
			"type": "message", "id": "msg_fake_1", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": "pong-fake", "annotations": []any{}}},
		}
		// 与真实 wire 一致：文本先经 output_text.delta 增量下发，再由 output_item.done 收口。
		for _, delta := range []string{"pong", "-fake"} {
			emit("response.output_text.delta", map[string]any{
				"type": "response.output_text.delta", "item_id": "msg_fake_1",
				"output_index": 0, "content_index": 0, "delta": delta,
			})
		}
		emit("response.output_item.done", map[string]any{
			"type": "response.output_item.done", "output_index": 0, "item": message,
		})
		emit("response.completed", map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"id": responseID, "object": "response", "status": "completed",
				"model": body.Model, "service_tier": "auto",
				"output": []any{message},
				"usage": map[string]any{
					"input_tokens": 42, "input_tokens_details": map[string]any{"cached_tokens": 0},
					"output_tokens": 3, "output_tokens_details": map[string]any{"reasoning_tokens": 0},
					"total_tokens": 45,
				},
			},
		})
	})

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	fmt.Println("fake codex upstream listening on", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Println(err)
	}
}
