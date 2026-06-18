package openai

import (
	"errors"
	"fmt"
	"sort"

	chatcompletions "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
	responsesadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

var (
	// ErrInvalidAdapterRegistration 表示 adapter 注册信息不合法。
	ErrInvalidAdapterRegistration = errors.New("invalid adapter registration")
	// ErrDuplicateAdapterKey 表示同一个 adapter key 被重复注册。
	ErrDuplicateAdapterKey = errors.New("duplicate adapter key")
)

// Registration 表示一个 adapter key 对应的代码能力。
//
// chat 三槽是 chat completions / responses→chat 桥接复用的基线能力；responses 三槽是「上游 responses
// 直传」能力（adapter_key 原生支持上游 /responses 时注册）。同一个 adapter_key 可只注册其中一组，
// 也可两组都注册；responses service 据候选 adapter 是否有 responses 直传能力分流（直传 vs 桥接）。
type Registration struct {
	Key                string
	Chat               chatcompletions.ChatAdapter
	StreamChat         chatcompletions.StreamChatAdapter
	ChatInputTokenizer chatcompletions.ChatInputTokenizer

	Responses               responsesadapter.ResponsesAdapter
	StreamResponses         responsesadapter.StreamResponsesAdapter
	ResponsesInputTokenizer responsesadapter.ResponsesInputTokenizer
	ResponsesCompact        responsesadapter.ResponsesCompactAdapter
}

// Registry 根据 adapter key 查找对应 adapter 能力。
type Registry struct {
	chat               map[string]chatcompletions.ChatAdapter
	streamChat         map[string]chatcompletions.StreamChatAdapter
	chatInputTokenizer map[string]chatcompletions.ChatInputTokenizer

	responses               map[string]responsesadapter.ResponsesAdapter
	streamResponses         map[string]responsesadapter.StreamResponsesAdapter
	responsesInputTokenizer map[string]responsesadapter.ResponsesInputTokenizer
	responsesCompact        map[string]responsesadapter.ResponsesCompactAdapter
}

// NewRegistry 创建 adapter registry。
func NewRegistry(registrations ...Registration) (*Registry, error) {
	r := &Registry{
		chat:                    make(map[string]chatcompletions.ChatAdapter),
		streamChat:              make(map[string]chatcompletions.StreamChatAdapter),
		chatInputTokenizer:      make(map[string]chatcompletions.ChatInputTokenizer),
		responses:               make(map[string]responsesadapter.ResponsesAdapter),
		streamResponses:         make(map[string]responsesadapter.StreamResponsesAdapter),
		responsesInputTokenizer: make(map[string]responsesadapter.ResponsesInputTokenizer),
		responsesCompact:        make(map[string]responsesadapter.ResponsesCompactAdapter),
	}

	for _, reg := range registrations {
		if reg.Key == "" {
			return nil, failure.Wrap(
				failure.CodeAdapterInvalidRegistration,
				ErrInvalidAdapterRegistration,
				failure.WithMessage("adapter registration key is empty"),
			)
		}

		if reg.Chat == nil && reg.StreamChat == nil && reg.ChatInputTokenizer == nil &&
			reg.Responses == nil && reg.StreamResponses == nil && reg.ResponsesInputTokenizer == nil &&
			reg.ResponsesCompact == nil {
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

	if reg.Responses != nil {
		if _, exists := r.responses[reg.Key]; exists {
			return failure.Wrap(
				failure.CodeAdapterDuplicateKey,
				ErrDuplicateAdapterKey,
				failure.WithMessage(fmt.Sprintf("duplicate responses adapter key %q", reg.Key)),
			)
		}
		r.responses[reg.Key] = reg.Responses
	}

	if reg.StreamResponses != nil {
		if _, exists := r.streamResponses[reg.Key]; exists {
			return failure.Wrap(
				failure.CodeAdapterDuplicateKey,
				ErrDuplicateAdapterKey,
				failure.WithMessage(fmt.Sprintf("duplicate stream responses adapter key %q", reg.Key)),
			)
		}
		r.streamResponses[reg.Key] = reg.StreamResponses
	}

	if reg.ResponsesInputTokenizer != nil {
		if _, exists := r.responsesInputTokenizer[reg.Key]; exists {
			return failure.Wrap(
				failure.CodeAdapterDuplicateKey,
				ErrDuplicateAdapterKey,
				failure.WithMessage(fmt.Sprintf("duplicate responses input tokenizer key %q", reg.Key)),
			)
		}
		r.responsesInputTokenizer[reg.Key] = reg.ResponsesInputTokenizer
	}

	if reg.ResponsesCompact != nil {
		if _, exists := r.responsesCompact[reg.Key]; exists {
			return failure.Wrap(
				failure.CodeAdapterDuplicateKey,
				ErrDuplicateAdapterKey,
				failure.WithMessage(fmt.Sprintf("duplicate responses compact adapter key %q", reg.Key)),
			)
		}
		r.responsesCompact[reg.Key] = reg.ResponsesCompact
	}

	return nil
}

// Keys 返回该 registry 注册的全部 adapter key（去重、字典序）。
// admin 据此把可选 adapter_key 暴露成枚举，供前端下拉而非手填。
func (r *Registry) Keys() []string {
	seen := make(map[string]struct{})
	for key := range r.chat {
		seen[key] = struct{}{}
	}
	for key := range r.streamChat {
		seen[key] = struct{}{}
	}
	for key := range r.chatInputTokenizer {
		seen[key] = struct{}{}
	}
	for key := range r.responses {
		seen[key] = struct{}{}
	}
	for key := range r.streamResponses {
		seen[key] = struct{}{}
	}
	for key := range r.responsesInputTokenizer {
		seen[key] = struct{}{}
	}
	for key := range r.responsesCompact {
		seen[key] = struct{}{}
	}

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Chat 根据 adapter key 返回非流式聊天 adapter。
func (r *Registry) Chat(adapterKey string) (chatcompletions.ChatAdapter, bool) {
	adapter, ok := r.chat[adapterKey]
	return adapter, ok
}

// StreamChat 根据 adapter key 返回流式聊天 adapter。
func (r *Registry) StreamChat(adapterKey string) (chatcompletions.StreamChatAdapter, bool) {
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
func (r *Registry) ChatInputTokenizer(adapterKey string) (chatcompletions.ChatInputTokenizer, bool) {
	tokenizer, ok := r.chatInputTokenizer[adapterKey]
	return tokenizer, ok
}

// HasChatInputTokenizer 判断 adapter key 是否注册了 chat 输入 token 计数能力。
func (r *Registry) HasChatInputTokenizer(adapterKey string) bool {
	_, ok := r.chatInputTokenizer[adapterKey]
	return ok
}

// Responses 根据 adapter key 返回非流式 responses 直传 adapter。
func (r *Registry) Responses(adapterKey string) (responsesadapter.ResponsesAdapter, bool) {
	adapter, ok := r.responses[adapterKey]
	return adapter, ok
}

// StreamResponses 根据 adapter key 返回流式 responses 直传 adapter。
func (r *Registry) StreamResponses(adapterKey string) (responsesadapter.StreamResponsesAdapter, bool) {
	adapter, ok := r.streamResponses[adapterKey]
	return adapter, ok
}

// ResponsesInputTokenizer 根据 adapter key 返回 responses 输入 token 计数能力。
func (r *Registry) ResponsesInputTokenizer(adapterKey string) (responsesadapter.ResponsesInputTokenizer, bool) {
	tokenizer, ok := r.responsesInputTokenizer[adapterKey]
	return tokenizer, ok
}

// HasResponses 判断 adapter key 是否注册了非流式 responses 直传能力。
func (r *Registry) HasResponses(adapterKey string) bool {
	_, ok := r.responses[adapterKey]
	return ok
}

// HasStreamResponses 判断 adapter key 是否注册了流式 responses 直传能力。
func (r *Registry) HasStreamResponses(adapterKey string) bool {
	_, ok := r.streamResponses[adapterKey]
	return ok
}

// HasResponsesInputTokenizer 判断 adapter key 是否注册了 responses 输入 token 计数能力。
func (r *Registry) HasResponsesInputTokenizer(adapterKey string) bool {
	_, ok := r.responsesInputTokenizer[adapterKey]
	return ok
}

// ResponsesCompact 根据 adapter key 返回原生 responses 压缩（/responses/compact）直传 adapter。
func (r *Registry) ResponsesCompact(adapterKey string) (responsesadapter.ResponsesCompactAdapter, bool) {
	adapter, ok := r.responsesCompact[adapterKey]
	return adapter, ok
}

// HasResponsesCompact 判断 adapter key 是否注册了原生 responses 压缩能力（NativeCompact 分流依据）。
func (r *Registry) HasResponsesCompact(adapterKey string) bool {
	_, ok := r.responsesCompact[adapterKey]
	return ok
}
