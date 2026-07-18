package messages

import (
	"context"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/anthropic/messages"
	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// prepareMessageCandidates 生成 Anthropic Messages 的保守 fallback plan。
// stickyChannelID 是会话粘性既有绑定渠道（0=无），非 0 时置顶该渠道（大 uncache 缺口 P0）。
func (s *MessagesService) prepareMessageCandidates(ctx context.Context, req gatewayapi.MessageRequest, candidates []routing.ChatRouteCandidate, mode string, stream bool, stickyChannelID int64) (lifecycle.CandidatePlan, error) {
	capabilities := []lifecycle.AdapterCapability{
		lifecycle.AdapterCapabilityInputTokenizer,
	}
	if stream {
		capabilities = append(capabilities, lifecycle.AdapterCapabilityStream)
	} else {
		capabilities = append(capabilities, lifecycle.AdapterCapabilityNonStream)
	}

	return s.candidates.PrepareCandidates(ctx, lifecycle.PrepareCandidatesParams{
		Protocol:            routing.ProtocolAnthropic,
		Candidates:          candidates,
		Capabilities:        capabilities,
		Available:           s.candidateAvailable,
		FailurePreferred:    s.lifecycle.CandidateFailurePreferred,
		EstimateInputTokens: s.messagesInputTokenEstimator(req),
		Mode:                mode,
		ChannelHealthScore:  s.channelHealthScore,
		StickyChannelID:     stickyChannelID,
	})
}

func (s *MessagesService) messagesInputTokenEstimator(req gatewayapi.MessageRequest) lifecycle.CandidateInputTokenEstimator {
	return func(_ context.Context, candidate routing.ChatRouteCandidate) (int64, error) {
		tokenizer, ok := s.registry.MessagesInputTokenizer(candidate.AdapterKey)
		if !ok {
			return 0, failure.New(
				failure.CodeGatewayAdapterNotRegistered,
				failure.WithMessage("anthropic messages input tokenizer is not registered"),
				failure.WithField("protocol", routing.ProtocolAnthropic),
				failure.WithField("adapter_key", candidate.AdapterKey),
			)
		}

		adapterReq := mapGatewayRequestToAdapter(req, candidate.UpstreamModel)
		inputTokens, err := tokenizer.CountMessagesInputTokens(messagesadapter.MessagesInputTokenizeRequest{
			Model:    adapterReq.Model,
			System:   adapterReq.System,
			Messages: adapterReq.Messages,
			Tools:    adapterReq.Tools,
		})
		if err != nil {
			return 0, failure.Wrap(
				failure.CodeAdapterTokenizeFailed,
				err,
				failure.WithMessage("count anthropic messages input tokens"),
				failure.WithField("protocol", routing.ProtocolAnthropic),
				failure.WithField("adapter_key", candidate.AdapterKey),
				failure.WithField("upstream_model", candidate.UpstreamModel),
			)
		}

		return inputTokens, nil
	}
}

// estimateMaxOutputTokens 返回客户显式给出的输出 token 上限；客户未给出时返回 0。
// Anthropic Messages 协议要求客户必传 max_tokens，这里仍兜底为 0，
// 客户缺失时的兜底（候选模型 max_output_tokens → 进程级 fallback）由 authorization 统一决定。
func estimateMaxOutputTokens(req gatewayapi.MessageRequest) int64 {
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		return int64(*req.MaxTokens)
	}
	return 0
}
