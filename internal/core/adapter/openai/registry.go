package openai

import (
	"errors"
	"fmt"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

var (
	// ErrInvalidAdapterRegistration 表示 adapter 注册信息不合法。
	ErrInvalidAdapterRegistration = errors.New("invalid adapter registration")
	// ErrDuplicateAdapterKey 表示同一个 adapter key 被重复注册。
	ErrDuplicateAdapterKey = errors.New("duplicate adapter key")
)

// Registration 表示一个 adapter key 对应的代码能力。
type Registration struct {
	Key                string
	Chat               ChatAdapter
	StreamChat         StreamChatAdapter
	ChatInputTokenizer ChatInputTokenizer
}

// Registry 根据 adapter key 查找对应 adapter 能力。
type Registry struct {
	chat               map[string]ChatAdapter
	streamChat         map[string]StreamChatAdapter
	chatInputTokenizer map[string]ChatInputTokenizer
}

// NewRegistry 创建 adapter registry。
func NewRegistry(registrations ...Registration) (*Registry, error) {
	r := &Registry{
		chat:               make(map[string]ChatAdapter),
		streamChat:         make(map[string]StreamChatAdapter),
		chatInputTokenizer: make(map[string]ChatInputTokenizer),
	}

	for _, reg := range registrations {
		if reg.Key == "" {
			return nil, failure.Wrap(
				failure.CodeAdapterInvalidRegistration,
				ErrInvalidAdapterRegistration,
				failure.WithMessage("adapter registration key is empty"),
			)
		}

		if reg.Chat == nil && reg.StreamChat == nil && reg.ChatInputTokenizer == nil {
			return nil, failure.Wrap(
				failure.CodeAdapterInvalidRegistration,
				ErrInvalidAdapterRegistration,
				failure.WithMessage(fmt.Sprintf("adapter %q has no capability", reg.Key)),
			)
		}

		if err := registerCapabilities(reg, r); err != nil {
			return nil, err
		}
	}

	return r, nil
}

func registerCapabilities(reg Registration, r *Registry) error {
	if reg.Chat != nil {
		if _, exists := r.chat[reg.Key]; exists {
			return failure.Wrap(
				failure.CodeAdapterDuplicateKey,
				ErrDuplicateAdapterKey,
				failure.WithMessage(fmt.Sprintf("duplicate chat adapter key %q", reg.Key)),
			)
		}
		r.chat[reg.Key] = reg.Chat
	}

	if reg.StreamChat != nil {
		if _, exists := r.streamChat[reg.Key]; exists {
			return failure.Wrap(
				failure.CodeAdapterDuplicateKey,
				ErrDuplicateAdapterKey,
				failure.WithMessage(fmt.Sprintf("duplicate stream adapter key %q", reg.Key)),
			)
		}
		r.streamChat[reg.Key] = reg.StreamChat
	}

	if reg.ChatInputTokenizer != nil {
		if _, exists := r.chatInputTokenizer[reg.Key]; exists {
			return failure.Wrap(
				failure.CodeAdapterDuplicateKey,
				ErrDuplicateAdapterKey,
				failure.WithMessage(fmt.Sprintf("duplicate chat input tokenizer key %q", reg.Key)),
			)
		}
		r.chatInputTokenizer[reg.Key] = reg.ChatInputTokenizer
	}

	return nil
}

// Chat 根据 adapter key 返回非流式聊天 adapter。
func (r *Registry) Chat(adapterKey string) (ChatAdapter, bool) {
	adapter, ok := r.chat[adapterKey]
	return adapter, ok
}

// StreamChat 根据 adapter key 返回流式聊天 adapter。
func (r *Registry) StreamChat(adapterKey string) (StreamChatAdapter, bool) {
	adapter, ok := r.streamChat[adapterKey]
	return adapter, ok
}

// HasChat 判断 adapter key 是否注册了非流式聊天能力。
func (r *Registry) HasChat(adapterKey string) bool {
	_, ok := r.chat[adapterKey]
	return ok
}

// HasStreamChat 判断 adapter key 是否注册了流式聊天能力。
func (r *Registry) HasStreamChat(adapterKey string) bool {
	_, ok := r.streamChat[adapterKey]
	return ok
}

// ChatInputTokenizer 根据 adapter key 返回 chat 输入 token 计数能力。
func (r *Registry) ChatInputTokenizer(adapterKey string) (ChatInputTokenizer, bool) {
	tokenizer, ok := r.chatInputTokenizer[adapterKey]
	return tokenizer, ok
}

// HasChatInputTokenizer 判断 adapter key 是否注册了 chat 输入 token 计数能力。
func (r *Registry) HasChatInputTokenizer(adapterKey string) bool {
	_, ok := r.chatInputTokenizer[adapterKey]
	return ok
}
