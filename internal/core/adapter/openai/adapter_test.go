package openai

import (
	"net/http"

	"github.com/ThankCat/unio-api/internal/core/adapter/openai/streamtranslate"
)

// newTestAdapter 创建测试用 OpenAI-compatible adapter，registry 与 bootstrap 保持一致。
func newTestAdapter(client *http.Client) *Adapter {
	return NewAdapter(client, streamtranslate.NewRegistry(
		streamtranslate.Default{},
		streamtranslate.DeepSeek{},
	))
}
