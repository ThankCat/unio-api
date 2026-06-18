package responses

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
	responsesadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// compact_orchestrator.go 实现 POST /v1/responses/compact 的双路径编排（GAP-11-014）：
//
//	NativeCompact   ：选中渠道 adapter 注册了原生 /responses/compact 能力 → 透传上游，响应原文返回。
//	SyntheticCompact：其余（chat-only 第三方，如 DeepSeek）→ 沿用 DEC-014 chat 摘要降级（GAP-11-007）。
//
// 与 CreateResponse 的直传/桥接分流哲学对称：运行期分叉以 adapter 代码能力（HasResponsesCompact）为准，
// 与 DEC-018 一致；能力 key responses.compact.native/synthetic 用于契约/矩阵声明。两条路径共用 runNonStream
// 资金关键 scaffold（routing/authorization/settlement/终态），账务与 lifecycle 不变量一致。

// compactResult 是一次 compact 成功调用的判别式结果：恰好其一非空。
//
// native 来自上游 /responses/compact 原文透传（Raw + facts）；synthetic 来自 chat 摘要降级（内部 ChatResponse）。
type compactResult struct {
	native    *responsesadapter.Response
	synthetic *chatcompletionsadapter.ChatResponse
}

// CompactHistory 无状态压缩会话历史，返回压缩后的新历史 {"output":[...]}。
//
// 缺省 instructions 时注入兜底压缩指令（Codex 通常自带；缺省时直发历史会让模型续写而非压缩）。
// 注入对 Native 透传无害（instructions 是 compact 合法字段，且原始 RawBody 透传优先），对 Synthetic 必要。
func (s *ResponsesService) CompactHistory(ctx context.Context, req gatewayapi.ResponsesRequest) (*gatewayapi.CompactHistoryResponse, error) {
	if req.Instructions == nil || strings.TrimSpace(*req.Instructions) == "" {
		def := defaultCompactionInstruction
		req.Instructions = &def
	}

	result, err := s.executeCompact(ctx, req)
	if err != nil {
		return nil, err
	}
	if result.native != nil {
		// NativeCompact：原文透传上游压缩响应体，仅改写顶层 model 回显为客户请求名。
		data := rewriteResponsesModel(result.native.Raw, req.Model)
		return gatewayapi.RawCompactHistoryResponse(data), nil
	}
	if result.synthetic == nil {
		return nil, failure.New(
			failure.CodeGatewayAdapterNotRegistered,
			failure.WithMessage("responses compact produced no result"),
		)
	}
	resp := mapChatResponseToCompaction(*result.synthetic)
	return &resp, nil
}

// executeCompact 执行 compact 双路径候选 fallback 计费循环，按候选 adapter 能力分流 Native/Synthetic。
//
// 候选过滤/估算复用 chat 桥接口径（allowDirect=false，与历史 compact 行为一致，零回归）；per-candidate
// 在 invoke 内分叉：注册了原生 compact 能力者走 NativeCompact，命中「上游不支持」时按配置回落 Synthetic（Q2）。
func (s *ResponsesService) executeCompact(ctx context.Context, req gatewayapi.ResponsesRequest) (compactResult, error) {
	var (
		compactAdapter responsesadapter.ResponsesCompactAdapter
		chatAdapter    chatcompletionsadapter.ChatAdapter
		result         compactResult
	)
	err := s.runNonStream(ctx, req, nonStreamStrategy{
		allowDirect: false,
		resolve: func(candidate routing.ChatRouteCandidate) error {
			if s.registry.HasResponsesCompact(candidate.AdapterKey) {
				adapter, ok := s.registry.ResponsesCompact(candidate.AdapterKey)
				if !ok {
					return failure.New(
						failure.CodeGatewayAdapterNotRegistered,
						failure.WithMessage(fmt.Sprintf("gateway responses compact adapter %q not registered", candidate.AdapterKey)),
					)
				}
				compactAdapter = adapter
				// 解析同 adapter_key 的 chat 能力，供 Native 不支持时回落 Synthetic（Q2，best-effort）。
				if chat, ok := s.registry.Chat(candidate.AdapterKey); ok {
					chatAdapter = chat
				}
				return nil
			}
			chat, ok := s.registry.Chat(candidate.AdapterKey)
			if !ok {
				return failure.New(
					failure.CodeGatewayAdapterNotRegistered,
					failure.WithMessage(fmt.Sprintf("gateway chat adapter %q not registered", candidate.AdapterKey)),
				)
			}
			chatAdapter = chat
			return nil
		},
		invoke: func(ctx context.Context, candidate routing.ChatRouteCandidate) (lifecycle.AttemptSuccess, error) {
			if compactAdapter != nil && s.registry.HasResponsesCompact(candidate.AdapterKey) {
				body, err := encodeUpstreamResponsesBody(req, candidate.UpstreamModel, false)
				if err != nil {
					return lifecycle.AttemptSuccess{}, err
				}
				adapterCtx, adapterSpan := lifecycle.StartGatewaySpan(ctx, "adapter.responses_compact", lifecycle.UpstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
				resp, err := compactAdapter.CompactResponse(adapterCtx, candidate.Channel, responsesadapter.Request{Body: body})
				lifecycle.EndGatewaySpan(adapterSpan, err)
				if err != nil {
					if s.compactNativeFallback && isNativeCompactUnsupported(err) {
						slog.WarnContext(ctx, "native compact unsupported; falling back to synthetic compaction",
							slog.String("adapter_key", candidate.AdapterKey),
							slog.Int64("channel_id", candidate.Channel.ID),
							slog.String("upstream_model", candidate.UpstreamModel),
						)
						return s.invokeSyntheticCompact(ctx, candidate, req, chatAdapter, &result)
					}
					return lifecycle.AttemptSuccess{}, err
				}
				result = compactResult{native: resp}
				return lifecycle.AttemptSuccess{ResponseID: resp.ResponseID, Facts: resp.Facts}, nil
			}
			return s.invokeSyntheticCompact(ctx, candidate, req, chatAdapter, &result)
		},
	})
	return result, err
}
