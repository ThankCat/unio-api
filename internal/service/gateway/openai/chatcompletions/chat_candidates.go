package chatcompletions

import (
	"context"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/chatcompletions"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// prepareChatCandidates 使用共享 lifecycle executor 生成 OpenAI operation 的保守 fallback plan。
// mode 是解析后的线路策略（cheapest/stable/fixed），决定候选排序（阶段 15）。
func (s *ChatCompletionService) prepareChatCandidates(ctx context.Context, req gatewayapi.ChatCompletionRequest, candidates []routing.ChatRouteCandidate, mode string, stream bool) (lifecycle.CandidatePlan, error) {
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
		Available:           s.candidateAvailable,
		EstimateInputTokens: s.chatInputTokenEstimator(req),
		Mode:                mode,
		ChannelHealthScore:  s.channelHealthScore,
	})
}

// chatInputTokenEstimator 构造 OpenAI 协议族候选级 tokenizer closure。
//
// closure 持有 OpenAI HTTP DTO，并按每个 candidate 的 adapter_key 与 upstream model
// 查找对应 tokenizer。共享 lifecycle 只调用 closure，不接触 OpenAI DTO。
func (s *ChatCompletionService) chatInputTokenEstimator(req gatewayapi.ChatCompletionRequest) lifecycle.CandidateInputTokenEstimator {
	return func(_ context.Context, candidate routing.ChatRouteCandidate) (int64, error) {
		tokenizer, ok := s.registry.ChatInputTokenizer(candidate.AdapterKey)
		if !ok {
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

// estimateMaxCompletionTokens 返回客户显式给出的输出 token 上限；客户未给出时返回 0。
// 客户缺失时的兜底（候选模型 max_output_tokens → 进程级 fallback）由 authorization 统一决定，
// 不在协议层用全局默认替代，避免按偏小的全局值预冻结导致超额进平台核销。
func estimateMaxCompletionTokens(req gatewayapi.ChatCompletionRequest) int64 {
	if req.MaxCompletionTokens != nil && *req.MaxCompletionTokens > 0 {
		return int64(*req.MaxCompletionTokens)
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		return int64(*req.MaxTokens)
	}
	return 0
}
