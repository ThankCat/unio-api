package bootstrap

import (
	"net/http"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic"
	anthropicdeepseek "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/deepseek/messages"
	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/core/adapter/openai"
	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	openaideepseek "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/deepseek/chatcompletions"
	openairesponses "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// NewAdapterRegistry 创建当前 server 进程支持的双协议 adapter registry。
//
// 当前进程支持：
//   - OpenAI 协议族：DeepSeek（adapter_key="deepseek"，chat-only）与 OpenAI 官方 1P
//     （adapter_key="openai"）。官方 1P 在同一个 adapter_key 上同时承载 chat completions 直传
//     与上游 responses 直传（chat 三槽 + responses 三槽），故一个 channel 即可服务 /chat/completions
//     与 /responses 两个端点：前者直连上游 /chat/completions、后者直连上游 /responses，皆零 Drop
//     忠实透传（见 providers/openai/upgrade-plan N2）。chat-only 第三方（如 DeepSeek）不注册
//     responses 三槽，gateway 据 HasResponses 自动落 responses→chat 桥接（DEC-014）。
//   - Anthropic 协议族：DeepSeek（adapter_key="deepseek"）与 Anthropic 官方 1P（adapter_key="anthropic"，
//     薄封装挂 beta 白名单透传 + base tokenizer，见 providers/anthropic/upgrade-plan N2/N3）。
//
// logger 注入到各 provider adapter，用于记录按 DEC-012 出站 Drop 的请求字段；传 nil 时
// adapter 内部回退到 zap no-op logger。官方 1P adapter 零 Drop，无需 logger。
func NewAdapterRegistry(client *http.Client, logger *zap.Logger) (*lifecycle.AdapterRegistry, error) {
	client = upstreamHTTPClient(client)

	openAIDeepSeekAdapter := openaideepseek.NewAdapter(client, logger)
	openAIOfficialAdapter := chatcompletionsadapter.NewAdapter(client)
	openAIResponsesAdapter := openairesponses.NewAdapter(client)

	openAIRegistry, err := openai.NewRegistry(
		openai.Registration{
			Key:                "deepseek",
			Chat:               openAIDeepSeekAdapter,
			StreamChat:         openAIDeepSeekAdapter,
			ChatInputTokenizer: openAIDeepSeekAdapter,
		},
		// OpenAI 官方 1P：单个 adapter_key=openai 同时承载 chat completions 与 responses 直传
		// 两组能力。一个 channel 绑定它即可服务两个端点（gateway 据 HasResponses 分流：/responses
		// 走 responses 直传、/chat/completions 走 chat 直传）。
		openai.Registration{
			Key:                     "openai",
			Chat:                    openAIOfficialAdapter,
			StreamChat:              openAIOfficialAdapter,
			ChatInputTokenizer:      openAIOfficialAdapter,
			Responses:               openAIResponsesAdapter,
			StreamResponses:         openAIResponsesAdapter,
			ResponsesInputTokenizer: openAIResponsesAdapter,
			ResponsesCompact:        openAIResponsesAdapter,
		},
	)
	if err != nil {
		return nil, err
	}

	anthropicDeepSeekAdapter := anthropicdeepseek.NewAdapter(client, logger)
	anthropicOfficialAdapter := messagesadapter.NewOfficialAdapter(client, logger)

	anthropicRegistry, err := anthropic.NewRegistry(
		anthropic.Registration{
			Key:                    "deepseek",
			Messages:               anthropicDeepSeekAdapter,
			StreamMessages:         anthropicDeepSeekAdapter,
			MessagesInputTokenizer: anthropicDeepSeekAdapter,
		},
		anthropic.Registration{
			Key:                    "anthropic",
			Messages:               anthropicOfficialAdapter,
			StreamMessages:         anthropicOfficialAdapter,
			MessagesInputTokenizer: anthropicOfficialAdapter,
		},
	)
	if err != nil {
		return nil, err
	}

	return lifecycle.NewAdapterRegistry(openAIRegistry, anthropicRegistry)
}

// upstreamHTTPClient preserves the caller's transport/timeouts while making one adapter call
// correspond to at most one real HTTP request. In particular, 307/308 must not replay POST bodies.
func upstreamHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	client := *base
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client
}
