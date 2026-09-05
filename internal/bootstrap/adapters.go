package bootstrap

import (
	"context"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic"
	anthropicdeepseek "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/deepseek/messages"
	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/core/adapter/modeldiscovery"
	"github.com/ThankCat/unio-gateway/internal/core/adapter/openai"
	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	codexresponses "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/codex/responses"
	openaideepseek "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/deepseek/chatcompletions"
	openairesponses "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/codexidentity"
	"github.com/ThankCat/unio-gateway/internal/platform/proxyclient"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// codexVersionSource 把 settings store 包装成 Codex 客户端版本来源：每次读取都走 store
// （本地 3s 缓存 → Redis → DB），Admin 改覆写或 worker 同步新版本后秒级生效，无需重启。
func codexVersionSource(store *appsettings.SettingsStore) codexidentity.VersionSource {
	if store == nil {
		return nil
	}
	return func() string {
		return appsettings.GatewayCodexClientVersion(context.Background(), store)
	}
}

const (
	upstreamMaxIdleConns        = 256
	upstreamMaxIdleConnsPerHost = 32
	upstreamMaxConnsPerHost     = 64
	// upstreamH2PingIdle / upstreamH2PingTimeout 是 HTTP/2 连接健康探测参数：
	// 连接上 30s 没有任何帧就发一次 PING，15s 内不回即判死并关闭（见 upstreamHTTPClient）。
	upstreamH2PingIdle    = 30 * time.Second
	upstreamH2PingTimeout = 15 * time.Second
)

// NewAdapterRegistry 创建当前 server 进程支持的双协议 adapter registry。
//
// 当前进程支持：
//   - OpenAI 协议族：DeepSeek（adapter_key="deepseek"，chat-only）与 OpenAI 官方 1P
//     （adapter_key="openai"）。官方 1P 在同一个 adapter_key 上同时承载 chat completions 直传
//     与上游 responses 直传（chat 三槽 + responses 三槽），故一个 channel 即可服务 /chat/completions
//     与 /responses 两个上游源站：前者直连上游 /chat/completions、后者直连上游 /responses，皆零 Drop
//     忠实透传（见 providers/openai/upgrade-plan N2）。chat-only 第三方（如 DeepSeek）不注册
//     responses 三槽，gateway 据 HasResponses 自动落 responses→chat 桥接（DEC-014）。
//   - Anthropic 协议族：DeepSeek（adapter_key="deepseek"）与 Anthropic 官方 1P（adapter_key="anthropic"，
//     薄封装挂 beta 白名单透传 + base tokenizer，见 providers/anthropic/upgrade-plan N2/N3）。
//
// logger 注入到各 provider adapter，用于记录按 DEC-012 出站 Drop 的请求字段；传 nil 时
// adapter 内部回退到 zap no-op logger。官方 1P adapter 零 Drop，无需 logger。
//
// codexVersion 提供 Codex 出站身份的当前生效版本（Admin 覆写 → 自动同步 → 基线），
// 由持有 settings store 的进程注入；nil 用编译期基线。
func NewAdapterRegistry(client *http.Client, logger *zap.Logger, codexVersion codexidentity.VersionSource) (*lifecycle.AdapterRegistry, error) {
	client = upstreamHTTPClient(client)

	// 出站代理解析器（账号代理与渠道代理共用同一份 client 缓存）：
	// 回退链 = 账号代理 → 渠道代理 → 直连，在各 adapter 的 client 选择处统一执行。
	accountProxyClients := proxyclient.NewResolver(client)

	openAIDeepSeekAdapter := openaideepseek.NewAdapter(client, logger).SetProxyClientResolver(accountProxyClients.ClientFor)
	openAIOfficialAdapter := chatcompletionsadapter.NewAdapter(client).SetProxyClientResolver(accountProxyClients.ClientFor)
	openAIResponsesAdapter := openairesponses.NewAdapter(client).SetProxyClientResolver(accountProxyClients.ClientFor)
	openAIModelLister := modeldiscovery.NewOpenAICompatible(client).SetProxyClientResolver(accountProxyClients.ClientFor)
	anthropicModelLister := modeldiscovery.NewAnthropic(client).SetProxyClientResolver(accountProxyClients.ClientFor)

	// Codex 订阅 wire（号池渠道）：按账号代理经 proxyclient 解析，导入/刷新/正式请求三条路径
	// 在 bootstrap 各自注入同一个解析器，adapter 不自管 client 池（边界 29）。
	codexAdapter := codexresponses.NewAdapter(client, accountProxyClients.ClientFor, codexVersion)
	codexModelLister := codexresponses.NewModelLister(client, accountProxyClients.ClientFor, codexVersion)

	openAIRegistry, err := openai.NewRegistry(
		openai.Registration{
			Key:                "deepseek",
			Models:             openAIModelLister,
			Chat:               openAIDeepSeekAdapter,
			StreamChat:         openAIDeepSeekAdapter,
			ChatInputTokenizer: openAIDeepSeekAdapter,
		},
		// OpenAI 官方 1P：单个 adapter_key=openai 同时承载 chat completions 与 responses 直传
		// 两组能力。一个 channel 绑定它即可服务两个上游源站（gateway 据 HasResponses 分流：/responses
		// 走 responses 直传、/chat/completions 走 chat 直传）。
		openai.Registration{
			Key:                     "openai",
			Models:                  openAIModelLister,
			Chat:                    openAIOfficialAdapter,
			StreamChat:              openAIOfficialAdapter,
			ChatInputTokenizer:      openAIOfficialAdapter,
			Responses:               openAIResponsesAdapter,
			StreamResponses:         openAIResponsesAdapter,
			ResponsesInputTokenizer: openAIResponsesAdapter,
			ResponsesCompact:        openAIResponsesAdapter,
		},
		// Codex 订阅后端（号池渠道专用）：只登记 responses 四槽，不登记 chat 三槽——
		// chat 请求经候选资格放开（HasChat || HasResponses）走 chat→responses 反向桥接。
		openai.Registration{
			Key:                     "codex",
			Models:                  codexModelLister,
			Responses:               codexAdapter,
			StreamResponses:         codexAdapter,
			ResponsesInputTokenizer: codexAdapter,
			ResponsesCompact:        codexAdapter,
		},
	)
	if err != nil {
		return nil, err
	}

	anthropicDeepSeekAdapter := anthropicdeepseek.NewAdapter(client, logger).SetProxyClientResolver(accountProxyClients.ClientFor)
	anthropicOfficialAdapter := messagesadapter.NewOfficialAdapter(client, logger).SetProxyClientResolver(accountProxyClients.ClientFor)

	anthropicRegistry, err := anthropic.NewRegistry(
		anthropic.Registration{
			Key: "deepseek",
			// DeepSeek 的模型目录仍是 OpenAI-compatible Bearer `/v1/models`。
			Models:                 openAIModelLister,
			Messages:               anthropicDeepSeekAdapter,
			StreamMessages:         anthropicDeepSeekAdapter,
			MessagesInputTokenizer: anthropicDeepSeekAdapter,
		},
		anthropic.Registration{
			Key:                    "anthropic",
			Models:                 anthropicModelLister,
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
	if base.Transport == nil || base.Transport == http.DefaultTransport {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxIdleConns = upstreamMaxIdleConns
		transport.MaxIdleConnsPerHost = upstreamMaxIdleConnsPerHost
		transport.MaxConnsPerHost = upstreamMaxConnsPerHost
		transport.ForceAttemptHTTP2 = true
		// HTTP/2 连接健康探测（2026-09-05 e2e 实测）：上游/中间设备静默断掉池里的 h2 连接后，
		// 下一次复用它的 POST 会在写完请求后直接收到 EOF；POST 体不可重放，Go 不会自动重试，
		// 客户端只能看到一次 502。空闲 30s 无帧就发 PING、15s 不回就关连接，让下一次请求重新拨号。
		transport.HTTP2 = &http.HTTP2Config{
			SendPingTimeout: upstreamH2PingIdle,
			PingTimeout:     upstreamH2PingTimeout,
		}
		client.Transport = transport
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client
}
