package bootstrap

import (
	"net/http"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai/streamtranslate"
)

// NewAdapterRegistry 创建当前 server 进程支持的 adapter registry。
func NewAdapterRegistry(client *http.Client) (*adapter.Registry, error) {
	translators := streamtranslate.NewRegistry(
		streamtranslate.Default{},
		streamtranslate.DeepSeek{},
	)
	openaiAdapter := openai.NewAdapter(client, translators)
	return adapter.NewRegistry(adapter.Registration{
		Key:                "openai",
		Chat:               openaiAdapter,
		StreamChat:         openaiAdapter,
		ChatInputTokenizer: openaiAdapter,
	})
}
