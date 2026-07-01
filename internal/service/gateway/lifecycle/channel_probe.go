package lifecycle

import (
	"context"
	"encoding/json"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	messagesadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic/messages"
	chatadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// probeMaxTokens 是检测请求的最大输出 token：足够拿到一个合法的最小响应，成本可忽略（约十几 token）。
const probeMaxTokens = 16

// probeUserContent 是检测请求的用户消息内容（最小 "hi"，OpenAI/Anthropic 通用的 JSON string content）。
// adapter 只读取它，跨请求共享安全。
var probeUserContent = json.RawMessage(`"hi"`)

// ProbeChannel 用 (protocol, adapter_key) 对应的 adapter，向 rt 描述的真实上游发一个最小 "hi"
// 请求，验证「连得上 + 凭据有效 + 模型可用」。它走的是与网关完全相同的 adapter/HTTP client 代码路径，
// 因此检测结果 = 真实调用行为（DEC：主动探测复用真实链路）。
//
// 返回上游 HTTP 状态码与错误：
//   - err==nil：检测通过，状态码取自成功响应的 upstream 元信息（通常 200）。
//   - err!=nil：检测失败，错误链上携带 *adapter.UpstreamError（稳定分类 + 元信息），
//     调用方据此把失败归类为凭据无效 / 模型不可用 / 超时 / 连不上等；此时状态码取自 UpstreamError
//     元信息（连接失败/超时未拿到响应时为 0）。
func (r *AdapterRegistry) ProbeChannel(ctx context.Context, protocol, adapterKey string, rt channel.Runtime, upstreamModel string) (int, error) {
	if r == nil {
		return 0, failure.New(
			failure.CodeAdapterInvalidRegistration,
			failure.WithMessage("adapter registry is nil"),
		)
	}

	switch protocol {
	case routing.ProtocolOpenAI:
		if r.OpenAI == nil {
			return 0, errProbeUnsupported(protocol, adapterKey)
		}
		chat, ok := r.OpenAI.Chat(adapterKey)
		if !ok {
			return 0, errProbeUnsupported(protocol, adapterKey)
		}
		maxTokens := probeMaxTokens
		resp, err := chat.ChatCompletions(ctx, rt, chatadapter.ChatRequest{
			Model:     upstreamModel,
			Messages:  []chatadapter.ChatMessage{{Role: "user", Content: probeUserContent}},
			MaxTokens: &maxTokens,
		})
		if err != nil {
			return probeStatusFromError(err), err
		}
		return resp.Upstream.StatusCode, nil

	case routing.ProtocolAnthropic:
		if r.Anthropic == nil {
			return 0, errProbeUnsupported(protocol, adapterKey)
		}
		msg, ok := r.Anthropic.Messages(adapterKey)
		if !ok {
			return 0, errProbeUnsupported(protocol, adapterKey)
		}
		maxTokens := probeMaxTokens
		resp, err := msg.Messages(ctx, rt, messagesadapter.MessageRequest{
			Model:     upstreamModel,
			Messages:  []messagesadapter.Message{{Role: "user", Content: probeUserContent}},
			MaxTokens: &maxTokens,
		})
		if err != nil {
			return probeStatusFromError(err), err
		}
		return resp.Upstream.StatusCode, nil

	default:
		return 0, errProbeUnsupported(protocol, adapterKey)
	}
}

// probeStatusFromError 从错误链的上游元信息取 HTTP 状态码（无则 0）。
func probeStatusFromError(err error) int {
	if meta, ok := adapter.UpstreamMetadataOf(err); ok {
		return meta.StatusCode
	}
	return 0
}

func errProbeUnsupported(protocol, adapterKey string) error {
	return failure.New(
		failure.CodeAdapterInvalidRegistration,
		failure.WithMessage("channel (protocol, adapter_key) is not registered for probing"),
		failure.WithField("protocol", protocol),
		failure.WithField("adapter_key", adapterKey),
	)
}
