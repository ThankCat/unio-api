package chatcompletions

import (
	"context"

	chatbridge "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatbridge"
	responsesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/openai/chatcompletions/dto"
)

// prepareChatCandidates 使用共享 lifecycle executor 生成 OpenAI operation 的保守 fallback plan。
// stickyChannelID 是会话粘性既有绑定渠道（0=无），非 0 时置顶该渠道（大 uncache 缺口 P0）。
func (s *ChatCompletionService) prepareChatCandidates(ctx context.Context, req dto.ChatCompletionRequest, candidates []routing.ChatRouteCandidate, stream bool, stickyChannelID int64) (lifecycle.CandidatePlan, error) {
	capabilities := []lifecycle.AdapterCapability{
		lifecycle.AdapterCapabilityInputTokenizer,
	}
	if stream {
		capabilities = append(capabilities, lifecycle.AdapterCapabilityStream)
	} else {
		capabilities = append(capabilities, lifecycle.AdapterCapabilityNonStream)
	}

	return s.candidates.PrepareCandidates(ctx, lifecycle.PrepareCandidatesParams{
		Protocol:            routing.ProtocolOpenAI,
		Candidates:          candidates,
		Capabilities:        capabilities,
		EstimateInputTokens: s.chatInputTokenEstimator(req),
		StickyChannelID:     stickyChannelID,
	})
}

// chatInputTokenEstimator 构造 OpenAI 协议族候选级 tokenizer closure。
//
// closure 持有 OpenAI HTTP DTO，并按每个 candidate 的 adapter_key 与 upstream model
// 查找对应 tokenizer。共享 lifecycle 只调用 closure，不接触 OpenAI DTO。
func (s *ChatCompletionService) chatInputTokenEstimator(req dto.ChatCompletionRequest) lifecycle.CandidateInputTokenEstimator {
	return func(_ context.Context, candidate routing.ChatRouteCandidate) (int64, error) {
		tokenizer, ok := s.registry.ChatInputTokenizer(candidate.AdapterKey)
		if !ok {
			// responses-only 上游（codex 号池）经反向桥接服务 chat 请求（第八节）：
			// 估算同样走桥接——把 chat 请求译成 responses 体，用该 adapter 的 responses tokenizer 计数。
			if bridged, bridgedOK := s.bridgedChatTokens(req, candidate); bridgedOK {
				return bridged, nil
			}
			return 0, failure.New(
				failure.CodeGatewayAdapterNotRegistered,
				failure.WithMessage("openai chat input tokenizer is not registered"),
				failure.WithField("protocol", routing.ProtocolOpenAI),
				failure.WithField("adapter_key", candidate.AdapterKey),
			)
		}

		inputTokens, err := tokenizer.CountChatInputTokens(
			mapGatewayRequestToAdapter(req, candidate.UpstreamModel),
		)
		if err != nil {
			return 0, failure.Wrap(
				failure.CodeAdapterTokenizeFailed,
				err,
				failure.WithMessage("count openai chat input tokens"),
				failure.WithField("protocol", routing.ProtocolOpenAI),
				failure.WithField("adapter_key", candidate.AdapterKey),
				failure.WithField("upstream_model", candidate.UpstreamModel),
			)
		}

		return inputTokens, nil
	}
}

// bridgedChatTokens 用反向桥接路径估算 chat 请求的输入 token（codex 等 responses-only 上游）。
func (s *ChatCompletionService) bridgedChatTokens(req dto.ChatCompletionRequest, candidate routing.ChatRouteCandidate) (int64, bool) {
	tokenizer, ok := s.registry.ResponsesInputTokenizer(candidate.AdapterKey)
	if !ok {
		return 0, false
	}
	body, err := chatbridge.BuildResponsesBodyForEstimate(mapGatewayRequestToAdapter(req, candidate.UpstreamModel))
	if err != nil {
		return 0, false
	}
	tokens, err := tokenizer.CountResponsesInputTokens(responsesadapter.Request{Body: body})
	if err != nil {
		return 0, false
	}
	return tokens, true
}

// estimateMaxCompletionTokens 返回客户显式给出的输出 token 上限；客户未给出时返回 0。
// 客户缺失时的兜底（候选模型 max_output_tokens → 进程级 fallback）由 authorization 统一决定，
// 不在协议层用全局默认替代，避免按偏小的全局值预冻结导致超额进平台核销。
func estimateMaxCompletionTokens(req dto.ChatCompletionRequest) int64 {
	if req.MaxCompletionTokens != nil && *req.MaxCompletionTokens > 0 {
		return int64(*req.MaxCompletionTokens)
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		return int64(*req.MaxTokens)
	}
	return 0
}
