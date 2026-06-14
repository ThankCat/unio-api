package chatcompletions

import "net/http"

// newTestAdapter 创建测试用 OpenAI-compatible adapter。
func newTestAdapter(client *http.Client) *Adapter {
	return NewAdapter(client)
}
