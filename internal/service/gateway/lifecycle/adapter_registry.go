// Package lifecycle 放置 OpenAI 与 Anthropic 共享的 gateway 请求生命周期能力。
package lifecycle

import (
	"errors"

	"github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic"
	"github.com/ThankCat/unio-gateway/internal/core/adapter/openai"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

var (
	// ErrProtocolRegistryMissing 表示共享 registry 缺少某个必需的协议族 registry。
	ErrProtocolRegistryMissing = errors.New("protocol adapter registry missing")
)

// AdapterCapability 表示 lifecycle 在尝试某个 channel 前要求的 adapter 能力。
type AdapterCapability string

const (
	// AdapterCapabilityNonStream 表示非流式调用能力。
	AdapterCapabilityNonStream AdapterCapability = "non_stream"
	// AdapterCapabilityStream 表示流式调用能力。
	AdapterCapabilityStream AdapterCapability = "stream"
	// AdapterCapabilityInputTokenizer 表示调用上游前的输入 token 估算能力。
	AdapterCapabilityInputTokenizer AdapterCapability = "input_tokenizer"

	// AdapterCapabilityResponsesServeNonStream 表示能服务一次非流式 Responses 请求：
	// 候选 adapter 原生支持上游 responses 直传（HasResponses）或可经桥接走 chat（HasChat）。
	AdapterCapabilityResponsesServeNonStream AdapterCapability = "responses_serve_non_stream"
	// AdapterCapabilityResponsesServeStream 表示能服务一次流式 Responses 请求（直传或桥接）。
	AdapterCapabilityResponsesServeStream AdapterCapability = "responses_serve_stream"
	// AdapterCapabilityResponsesServeTokenizer 表示能为一次 Responses 请求估算输入 token（直传或桥接）。
	AdapterCapabilityResponsesServeTokenizer AdapterCapability = "responses_serve_tokenizer"
)

// AdapterRegistry 是双协议 gateway 的共享 registry facade。
//
// SQL 先按 channel.protocol 选择同协议候选；lifecycle 再通过这个 facade 使用
// (protocol, adapter_key) 复合键过滤本次 operation 缺少的代码能力。
type AdapterRegistry struct {
	OpenAI    *openai.Registry
	Anthropic *anthropic.Registry
}

// NewAdapterRegistry 创建双协议共享 registry facade。
func NewAdapterRegistry(openAI *openai.Registry, anthropicRegistry *anthropic.Registry) (*AdapterRegistry, error) {
	if openAI == nil {
		return nil, failure.Wrap(
			failure.CodeAdapterInvalidRegistration,
			ErrProtocolRegistryMissing,
			failure.WithMessage("openai protocol adapter registry is missing"),
			failure.WithField("protocol", routing.ProtocolOpenAI),
		)
	}
	if anthropicRegistry == nil {
		return nil, failure.Wrap(
			failure.CodeAdapterInvalidRegistration,
			ErrProtocolRegistryMissing,
			failure.WithMessage("anthropic protocol adapter registry is missing"),
			failure.WithField("protocol", routing.ProtocolAnthropic),
		)
	}

	return &AdapterRegistry{
		OpenAI:    openAI,
		Anthropic: anthropicRegistry,
	}, nil
}

// Has 判断 (protocol, adapter_key) 是否注册了指定能力。
func (r *AdapterRegistry) Has(protocol string, adapterKey string, capability AdapterCapability) bool {
	if r == nil {
		return false
	}

	switch protocol {
	case routing.ProtocolOpenAI:
		return r.hasOpenAI(adapterKey, capability)
	case routing.ProtocolAnthropic:
		return r.hasAnthropic(adapterKey, capability)
	default:
		return false
	}
}

// HasAny 判断 (protocol, adapter_key) 是否至少注册了一种代码能力。
//
// bootstrap 用它拒绝完全未知的 channel 绑定；具体 operation 仍由 FilterCandidates
// 按 non-stream、stream 和 input tokenizer 等实际需要继续过滤。
func (r *AdapterRegistry) HasAny(protocol string, adapterKey string) bool {
	return r.Has(protocol, adapterKey, AdapterCapabilityNonStream) ||
		r.Has(protocol, adapterKey, AdapterCapabilityStream) ||
		r.Has(protocol, adapterKey, AdapterCapabilityInputTokenizer) ||
		r.Has(protocol, adapterKey, AdapterCapabilityResponsesServeNonStream) ||
		r.Has(protocol, adapterKey, AdapterCapabilityResponsesServeStream)
}

// AdapterKeys 返回指定协议族下当前进程注册的全部 adapter key（去重、字典序）。
//
// admin 据此把可选 adapter_key 暴露成枚举供前端下拉；未知协议或对应 registry 缺失返回 nil。
func (r *AdapterRegistry) AdapterKeys(protocol string) []string {
	if r == nil {
		return nil
	}

	switch protocol {
	case routing.ProtocolOpenAI:
		if r.OpenAI == nil {
			return nil
		}
		return r.OpenAI.Keys()
	case routing.ProtocolAnthropic:
		if r.Anthropic == nil {
			return nil
		}
		return r.Anthropic.Keys()
	default:
		return nil
	}
}

// FilterCandidates 保持 SQL routing 顺序，过滤缺少任一指定能力的候选。
func (r *AdapterRegistry) FilterCandidates(protocol string, candidates []routing.ChatRouteCandidate, capabilities ...AdapterCapability) []routing.ChatRouteCandidate {
	filtered := make([]routing.ChatRouteCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if r.hasAll(protocol, candidate.AdapterKey, capabilities) {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func (r *AdapterRegistry) hasAll(protocol string, adapterKey string, capabilities []AdapterCapability) bool {
	for _, capability := range capabilities {
		if !r.Has(protocol, adapterKey, capability) {
			return false
		}
	}
	return true
}

func (r *AdapterRegistry) hasOpenAI(adapterKey string, capability AdapterCapability) bool {
	if r.OpenAI == nil {
		return false
	}

	switch capability {
	case AdapterCapabilityNonStream:
		return r.OpenAI.HasChat(adapterKey)
	case AdapterCapabilityStream:
		return r.OpenAI.HasStreamChat(adapterKey)
	case AdapterCapabilityInputTokenizer:
		return r.OpenAI.HasChatInputTokenizer(adapterKey)
	case AdapterCapabilityResponsesServeNonStream:
		// 直传（上游 /responses）或桥接（responses→chat）任一可服务即保留候选；
		// 具体走哪条由 responses service 据 HasResponses 分流。
		return r.OpenAI.HasResponses(adapterKey) || r.OpenAI.HasChat(adapterKey)
	case AdapterCapabilityResponsesServeStream:
		return r.OpenAI.HasStreamResponses(adapterKey) || r.OpenAI.HasStreamChat(adapterKey)
	case AdapterCapabilityResponsesServeTokenizer:
		return r.OpenAI.HasResponsesInputTokenizer(adapterKey) || r.OpenAI.HasChatInputTokenizer(adapterKey)
	default:
		return false
	}
}

func (r *AdapterRegistry) hasAnthropic(adapterKey string, capability AdapterCapability) bool {
	if r.Anthropic == nil {
		return false
	}

	switch capability {
	case AdapterCapabilityNonStream:
		return r.Anthropic.HasMessages(adapterKey)
	case AdapterCapabilityStream:
		return r.Anthropic.HasStreamMessages(adapterKey)
	case AdapterCapabilityInputTokenizer:
		return r.Anthropic.HasMessagesInputTokenizer(adapterKey)
	default:
		return false
	}
}
