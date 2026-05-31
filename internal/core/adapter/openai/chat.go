package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai/streamtranslate"
	adaptersse "github.com/ThankCat/unio-api/internal/core/adapter/sse"
	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

const (
	// maxOpenAIStreamEventBytes 是单个上游 OpenAI-compatible SSE event 的读取上限。
	maxOpenAIStreamEventBytes = 4 * 1024 * 1024
)

// Adapter 调用 OpenAI-compatible 上游接口。
type Adapter struct {
	client      *http.Client
	translators *streamtranslate.Registry
}

// NewAdapter 创建 OpenAI-compatible adapter。
func NewAdapter(client *http.Client, translators *streamtranslate.Registry) *Adapter {
	if client == nil {
		client = http.DefaultClient
	}

	if translators == nil {
		translators = streamtranslate.NewRegistry(streamtranslate.Default{})
	}

	return &Adapter{
		client:      client,
		translators: translators,
	}
}

// ChatCompletions 调用上游 /chat/completions，并转换为统一 adapter 响应。
func (a *Adapter) ChatCompletions(ctx context.Context, ch channel.Runtime, req adapter.ChatRequest) (*adapter.ChatResponse, error) {
	if ch.BaseURL == "" {
		return nil, failure.New(
			failure.CodeAdapterChannelInvalid,
			failure.WithMessage("openai adapter channel base url is empty"),
		)
	}

	if ch.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ch.Timeout)
		defer cancel()
	}

	url := strings.TrimRight(ch.BaseURL, "/") + "/chat/completions"

	buf, err := encodeRequestBody(req, false)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterEncodeRequestFailed,
			err,
			failure.WithMessage("openai adapter encode chat completion request"),
		)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, buf)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterCreateRequestFailed,
			err,
			failure.WithMessage("openai adapter create chat completion request"),
		)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", ch.APIKey))

	upstreamResp, err := a.client.Do(request)
	if err != nil {
		return nil, newUpstreamSendError(err, "send chat completion request")
	}
	defer upstreamResp.Body.Close()

	if upstreamResp.StatusCode < http.StatusOK || upstreamResp.StatusCode >= http.StatusMultipleChoices {
		return nil, newUpstreamStatusError(upstreamResp, "upstream")
	}

	var upstreamRespBody chatCompletionResponse
	if err := json.NewDecoder(upstreamResp.Body).Decode(&upstreamRespBody); err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterDecodeResponseFailed,
			err,
			failure.WithMessage("openai adapter decode chat completion response"),
		)
	}

	if upstreamRespBody.Choices == nil || len(upstreamRespBody.Choices) == 0 {
		return nil, failure.New(
			failure.CodeAdapterInvalidResponse,
			failure.WithMessage("openai adapter empty chat completion choices"),
		)
	}

	usage, err := chatUsageFromOpenAI(upstreamRespBody.Usage)
	if err != nil {
		return nil, err
	}

	toolCalls, err := wireToolCallsToAdapter(upstreamRespBody.Choices[0].Message.ToolCalls)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterDecodeResponseFailed,
			err,
			failure.WithMessage("openai adapter decode chat completion tool_calls"),
		)
	}

	return &adapter.ChatResponse{
		ID:               upstreamRespBody.ID,
		Model:            upstreamRespBody.Model,
		Content:          wireMessageContentString(upstreamRespBody.Choices[0].Message.Content),
		ReasoningContent: upstreamRespBody.Choices[0].Message.ReasoningContent,
		ToolCalls:        toolCalls,
		FinishReason:     upstreamFinishReason(upstreamRespBody.Choices[0]),
		Usage:   usage,
		Upstream: adapter.UpstreamMetadata{
			StatusCode: upstreamResp.StatusCode,
			RequestID:  upstreamResp.Header.Get(upstreamRequestIDHeader),
		},
	}, nil
}

// StreamChatCompletions 调用上游 /chat/completions stream，并转换为统一 adapter chunk。
func (a *Adapter) StreamChatCompletions(ctx context.Context, ch channel.Runtime, req adapter.ChatRequest, emit func(adapter.ChatStreamChunk) error) error {
	if emit == nil {
		return failure.New(
			failure.CodeAdapterEmitFailed,
			failure.WithMessage("openai adapter stream emit is nil"),
		)
	}

	if ch.BaseURL == "" {
		return failure.New(
			failure.CodeAdapterChannelInvalid,
			failure.WithMessage("openai adapter channel base url is empty"),
		)
	}

	if ch.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ch.Timeout)
		defer cancel()
	}

	url := strings.TrimRight(ch.BaseURL, "/") + "/chat/completions"

	buf, err := encodeRequestBody(req, true)
	if err != nil {
		return failure.Wrap(
			failure.CodeAdapterEncodeRequestFailed,
			err,
			failure.WithMessage("openai adapter encode stream chat completion request"),
		)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, buf)
	if err != nil {
		return failure.Wrap(
			failure.CodeAdapterCreateRequestFailed,
			err,
			failure.WithMessage("openai adapter create stream chat completion request"),
		)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", ch.APIKey))

	upstreamResp, err := a.client.Do(request)
	if err != nil {
		return newUpstreamSendError(err, "send stream chat completion request")
	}
	defer upstreamResp.Body.Close()

	if upstreamResp.StatusCode < http.StatusOK || upstreamResp.StatusCode >= http.StatusMultipleChoices {
		return newUpstreamStatusError(upstreamResp, "upstream stream")
	}

	streamReader := adaptersse.NewReader(upstreamResp.Body, adaptersse.Config{
		MaxLineBytes:  maxOpenAIStreamEventBytes,
		MaxEventBytes: maxOpenAIStreamEventBytes,
	})

	for streamReader.Next() {
		payload := bytes.TrimSpace(streamReader.Event().Data)
		if bytes.Equal(payload, []byte("[DONE]")) {
			break
		}

		var streamResp chatCompletionStreamResponse
		if err := json.Unmarshal(payload, &streamResp); err != nil {
			return failure.Wrap(
				failure.CodeAdapterDecodeResponseFailed,
				err,
				failure.WithMessage("openai adapter decode stream chunk"),
			)
		}

		translator := a.translators.Resolve(ch.ProviderSlug)

		streamIn, err := streamInputFromResponse(streamResp)
		if err != nil {
			return err
		}

		events, err := translator.TranslateStreamEvent(streamIn)
		if err != nil {
			return err
		}

		for _, event := range events {
			chunk := adapter.ChatStreamChunk{
				ID:               event.ID,
				Model:            event.Model,
				Role:             event.Role,
				Content:          event.Content,
				ReasoningContent: stringPtrOrNil(event.ReasoningContent),
				ToolCalls:        cloneRawMessage(event.ToolCalls),
				FinishReason:     event.FinishReason,
			}

			if event.Usage != nil {
				usage := *event.Usage
				chunk.Usage = &usage
				chunk.Upstream = &adapter.UpstreamMetadata{
					StatusCode: upstreamResp.StatusCode,
					RequestID:  upstreamResp.Header.Get(upstreamRequestIDHeader),
				}
			}

			if err := emit(chunk); err != nil {
				return failure.Wrap(
					failure.CodeAdapterEmitFailed,
					err,
					failure.WithMessage("openai adapter send stream chunk"),
				)
			}
		}
	}

	if err := streamReader.Err(); err != nil {
		return failure.Wrap(
			failure.CodeAdapterReadStreamFailed,
			err,
			failure.WithMessage("openai adapter read stream event"),
		)
	}

	return nil
}

// streamInputFromResponse 将上游 stream JSON DTO 转成 stream translator 输入。
func streamInputFromResponse(streamResp chatCompletionStreamResponse) (streamtranslate.StreamInput, error) {
	in := streamtranslate.StreamInput{
		ID:    streamResp.ID,
		Model: streamResp.Model,
	}

	if streamResp.Usage != nil {
		usage, err := chatUsageFromOpenAI(streamResp.Usage)
		if err != nil {
			return streamtranslate.StreamInput{}, err
		}

		in.Usage = &usage
	}

	if len(streamResp.Choices) == 0 {
		return in, nil
	}

	in.Choices = make([]streamtranslate.StreamChoice, 0, len(streamResp.Choices))
	for _, choice := range streamResp.Choices {
		streamChoice := streamtranslate.StreamChoice{
			Role:         choice.Delta.Role,
			Content:      choice.Delta.Content,
			ToolCalls:    cloneRawMessage(choice.Delta.ToolCalls),
			FinishReason: choice.FinishReason,
		}
		if choice.Delta.ReasoningContent != nil {
			streamChoice.ReasoningContent = *choice.Delta.ReasoningContent
		}

		in.Choices = append(in.Choices, streamChoice)
	}

	return in, nil
}

// chatUsageFromOpenAI 将 OpenAI usage DTO 转成 adapter 内部 usage DTO。
// 非流式成功响应和 stream final usage 都必须提供完整 usage，避免缺字段被当成 0 元请求。
func chatUsageFromOpenAI(usage *chatCompletionUsage) (adapter.ChatUsage, error) {
	if usage == nil {
		return adapter.ChatUsage{}, failure.New(
			failure.CodeAdapterInvalidResponse,
			failure.WithMessage("openai adapter missing chat completion usage"),
		)
	}

	if usage.PromptTokens == nil || usage.CompletionTokens == nil || usage.TotalTokens == nil {
		return adapter.ChatUsage{}, failure.New(
			failure.CodeAdapterInvalidResponse,
			failure.WithMessage("openai adapter missing required chat completion usage token fields"),
		)
	}

	if *usage.PromptTokens <= 0 || *usage.CompletionTokens < 0 || *usage.TotalTokens <= 0 {
		return adapter.ChatUsage{}, failure.New(
			failure.CodeAdapterInvalidResponse,
			failure.WithMessage("openai adapter invalid chat completion usage token counts"),
		)
	}

	if *usage.TotalTokens != *usage.PromptTokens+*usage.CompletionTokens {
		return adapter.ChatUsage{}, failure.New(
			failure.CodeAdapterInvalidResponse,
			failure.WithMessage("openai adapter inconsistent chat completion usage token counts"),
		)
	}

	return adapter.ChatUsage{
		PromptTokens:     *usage.PromptTokens,
		CompletionTokens: *usage.CompletionTokens,
		TotalTokens:      *usage.TotalTokens,
		CachedTokens:     usage.PromptTokensDetails.CachedTokens,
		ReasoningTokens:  usage.CompletionTokensDetails.ReasoningTokens,
	}, nil
}
