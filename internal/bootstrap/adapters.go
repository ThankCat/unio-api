package bootstrap

import (
	"net/http"

	"github.com/ThankCat/unio-api/internal/core/adapter/anthropic"
	anthropicdeepseek "github.com/ThankCat/unio-api/internal/core/adapter/anthropic/deepseek"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
	openaideepseek "github.com/ThankCat/unio-api/internal/core/adapter/openai/deepseek"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// NewAdapterRegistry 创建当前 server 进程支持的双协议 adapter registry。
//
// 当前进程同时支持 DeepSeek 的 OpenAI 与 Anthropic 协议族 adapter。
// 两侧 channels.adapter_key 都是 "deepseek"，由 channel.protocol 组成运行时复合键。
func NewAdapterRegistry(client *http.Client) (*lifecycle.AdapterRegistry, error) {
	openAIDeepSeekAdapter := openaideepseek.NewAdapter(client)
	openAIRegistry, err := openai.NewRegistry(openai.Registration{
		Key:                "deepseek",
		Chat:               openAIDeepSeekAdapter,
		StreamChat:         openAIDeepSeekAdapter,
		ChatInputTokenizer: openAIDeepSeekAdapter,
	})
	if err != nil {
		return nil, err
	}

	anthropicDeepSeekAdapter := anthropicdeepseek.NewAdapter(client)
	anthropicRegistry, err := anthropic.NewRegistry(anthropic.Registration{
		Key:                    "deepseek",
		Messages:               anthropicDeepSeekAdapter,
		StreamMessages:         anthropicDeepSeekAdapter,
		MessagesInputTokenizer: anthropicDeepSeekAdapter,
	})
	if err != nil {
		return nil, err
	}

	return lifecycle.NewAdapterRegistry(openAIRegistry, anthropicRegistry)
}
