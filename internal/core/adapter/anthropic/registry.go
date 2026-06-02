package anthropic

import (
	"errors"
	"fmt"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

var (
	// ErrInvalidAdapterRegistration 表示 adapter 注册信息不合法。
	ErrInvalidAdapterRegistration = errors.New("invalid anthropic adapter registration")
	// ErrDuplicateAdapterKey 表示同一个 adapter key 被重复注册。
	ErrDuplicateAdapterKey = errors.New("duplicate anthropic adapter key")
)

// Registration 表示一个 Anthropic 协议族 adapter key 对应的代码能力。
//
// 与 OpenAI 协议族平行：非流式、流式、输入 tokenizer 分别登记，不强制组合成单一大接口。
// SQL 先按 protocol=anthropic 筛选 channel，lifecycle 再按 registry capability 过滤。
type Registration struct {
	Key                    string
	Messages               MessagesAdapter
	StreamMessages         StreamMessagesAdapter
	MessagesInputTokenizer MessagesInputTokenizer
}

// Registry 根据 adapter key 查找 Anthropic 协议族 adapter 能力。
type Registry struct {
	messages       map[string]MessagesAdapter
	streamMessages map[string]StreamMessagesAdapter
	tokenizer      map[string]MessagesInputTokenizer
}

// NewRegistry 创建 Anthropic 协议族 adapter registry。
func NewRegistry(registrations ...Registration) (*Registry, error) {
	r := &Registry{
		messages:       make(map[string]MessagesAdapter),
		streamMessages: make(map[string]StreamMessagesAdapter),
		tokenizer:      make(map[string]MessagesInputTokenizer),
	}

	for _, reg := range registrations {
		if reg.Key == "" {
			return nil, failure.Wrap(
				failure.CodeAdapterInvalidRegistration,
				ErrInvalidAdapterRegistration,
				failure.WithMessage("anthropic adapter registration key is empty"),
			)
		}

		if reg.Messages == nil && reg.StreamMessages == nil && reg.MessagesInputTokenizer == nil {
			return nil, failure.Wrap(
				failure.CodeAdapterInvalidRegistration,
				ErrInvalidAdapterRegistration,
				failure.WithMessage(fmt.Sprintf("anthropic adapter %q has no capability", reg.Key)),
			)
		}

		if err := registerCapabilities(reg, r); err != nil {
			return nil, err
		}
	}

	return r, nil
}

func registerCapabilities(reg Registration, r *Registry) error {
	if reg.Messages != nil {
		if _, exists := r.messages[reg.Key]; exists {
			return duplicateKey("messages adapter", reg.Key)
		}
		r.messages[reg.Key] = reg.Messages
	}

	if reg.StreamMessages != nil {
		if _, exists := r.streamMessages[reg.Key]; exists {
			return duplicateKey("stream messages adapter", reg.Key)
		}
		r.streamMessages[reg.Key] = reg.StreamMessages
	}

	if reg.MessagesInputTokenizer != nil {
		if _, exists := r.tokenizer[reg.Key]; exists {
			return duplicateKey("messages input tokenizer", reg.Key)
		}
		r.tokenizer[reg.Key] = reg.MessagesInputTokenizer
	}

	return nil
}

func duplicateKey(capability, key string) error {
	return failure.Wrap(
		failure.CodeAdapterDuplicateKey,
		ErrDuplicateAdapterKey,
		failure.WithMessage(fmt.Sprintf("duplicate %s key %q", capability, key)),
	)
}

// Messages 根据 adapter key 返回非流式 Messages adapter。
func (r *Registry) Messages(adapterKey string) (MessagesAdapter, bool) {
	a, ok := r.messages[adapterKey]
	return a, ok
}

// StreamMessages 根据 adapter key 返回流式 Messages adapter。
func (r *Registry) StreamMessages(adapterKey string) (StreamMessagesAdapter, bool) {
	a, ok := r.streamMessages[adapterKey]
	return a, ok
}

// MessagesInputTokenizer 根据 adapter key 返回 Messages 输入 token 计数能力。
func (r *Registry) MessagesInputTokenizer(adapterKey string) (MessagesInputTokenizer, bool) {
	t, ok := r.tokenizer[adapterKey]
	return t, ok
}

// HasMessages 判断 adapter key 是否注册了非流式 Messages 能力。
func (r *Registry) HasMessages(adapterKey string) bool {
	_, ok := r.messages[adapterKey]
	return ok
}

// HasStreamMessages 判断 adapter key 是否注册了流式 Messages 能力。
func (r *Registry) HasStreamMessages(adapterKey string) bool {
	_, ok := r.streamMessages[adapterKey]
	return ok
}

// HasMessagesInputTokenizer 判断 adapter key 是否注册了 Messages 输入 token 计数能力。
func (r *Registry) HasMessagesInputTokenizer(adapterKey string) bool {
	_, ok := r.tokenizer[adapterKey]
	return ok
}
