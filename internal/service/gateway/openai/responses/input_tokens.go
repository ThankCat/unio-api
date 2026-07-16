package responses

import (
	"context"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// input_tokens.go 实现 POST /v1/responses/input_tokens 的本地估算（DEC-014 / GAP-11-008）。
//
// 仅用于 Codex 的 auto_compact 时机预检：不走候选 fallback、不调上游、不计费、不写账务。
// routing 只用于把客户模型名解析到一个 OpenAI 候选的 adapter_key + upstream model，从而取到对应
// tokenizer；估算先把 Responses 请求翻译成内部 ChatRequest 再交给 tokenizer，与真实调用一致。
// 与 OpenAI 服务端精确计数存在偏差，不反映 prompt cache 折扣（GAP-11-008）。

// inputTokenCountObject 是 input_tokens 响应固定的 object 值（openai-* SDK 确认）。
const inputTokenCountObject = "response.input_tokens"

// CountInputTokens 本地估算请求 input token 数。
func (s *ResponsesService) CountInputTokens(ctx context.Context, req gatewayapi.ResponsesRequest) (*gatewayapi.InputTokenCountResponse, error) {
	principal, ok := auth.APIKeyPrincipalFromContext(ctx)
	if !ok {
		return nil, failure.Wrap(
			failure.CodeAuthMissingAPIKey,
			auth.ErrMissingAPIKey,
			failure.WithMessage(auth.ErrMissingAPIKey.Error()),
		)
	}

	// routing 仅解析模型→候选，用于选取 tokenizer；不消费候选 fallback 计划，不做能力过滤/授权。
	// RouteID 必传：线路必填改造（DEC-026/027）后缺省会被 ErrRouteNotConfigured 拒绝。
	plan, err := s.router.PlanChat(ctx, routing.ChatRouteRequest{
		UserID:          principal.UserID,
		ModelID:         req.Model,
		IngressProtocol: routing.ProtocolOpenAI,
		Operation:       routing.OperationResponses,
		RouteID:         principal.RouteID,
	})
	if err != nil {
		return nil, err
	}
	if len(plan.Candidates) == 0 {
		return nil, failure.Wrap(
			failure.CodeRoutingNoAvailableChannel,
			routing.ErrNoAvailableChannel,
			failure.WithMessage(routing.ErrNoAvailableChannel.Error()),
		)
	}

	candidate := plan.Candidates[0]
	tokenizer, ok := s.registry.ChatInputTokenizer(candidate.AdapterKey)
	if !ok {
		return nil, failure.New(
			failure.CodeGatewayAdapterNotRegistered,
			failure.WithMessage("openai chat input tokenizer is not registered"),
			failure.WithField("protocol", routing.ProtocolOpenAI),
			failure.WithField("adapter_key", candidate.AdapterKey),
		)
	}

	chatReq, _ := mapResponsesRequestToChat(req, candidate.UpstreamModel)
	count, err := tokenizer.CountChatInputTokens(chatReq)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterTokenizeFailed,
			err,
			failure.WithMessage("count responses-bridged chat input tokens"),
			failure.WithField("protocol", routing.ProtocolOpenAI),
			failure.WithField("adapter_key", candidate.AdapterKey),
			failure.WithField("upstream_model", candidate.UpstreamModel),
		)
	}

	return &gatewayapi.InputTokenCountResponse{
		InputTokens: int(count),
		Object:      inputTokenCountObject,
	}, nil
}
