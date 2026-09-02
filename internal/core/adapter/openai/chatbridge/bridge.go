// Package chatbridge 实现 Chat Completions → Responses 反向桥接（第八节，DEC-014 的镜像方向）。
//
// 受益方是通用 chat 客户端（OpenAI SDK 的 chat.completions、LangChain、第三方应用）：
// 它们想用 responses-only 上游（codex 号池）的模型时只会说 chat 协议。Codex CLI 自 2026-02
// 起已移除 wire_api="chat"，不可能向我们发 chat 请求，因此桥接不影响 Codex 主链路。
//
// 桥接位于 adapter DTO 层：实现 chatcompletions.ChatAdapter / StreamChatAdapter，
// 背后调 responses.ResponsesAdapter / StreamResponsesAdapter。账务事实（ResponseFacts）
// 由 responses adapter 在同一次解析中产出并原样透传——settlement 消费的是 responses 侧
// 的 usage 与 service_tier 事实，桥接不重算、不伪造。
//
// 移植纪律（采信口径）：请求/响应字段映射与 Sub2API internal/pkg/apicompat 的语义对照实现，
// 但代码为本仓库契约重写；分发红线与来源标注见 PLAN 第八节。
package chatbridge

import (
	"context"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	chatcompletions "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	responses "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// Bridge 把 chat 请求译成 Responses 请求出站，把 Responses 结果译回 chat 形状。
type Bridge struct {
	responses       responses.ResponsesAdapter
	streamResponses responses.StreamResponsesAdapter
}

// New 创建反向桥接 adapter。二者可为同一实现（codex adapter 同时实现两接口）。
func New(nonStream responses.ResponsesAdapter, stream responses.StreamResponsesAdapter) *Bridge {
	return &Bridge{responses: nonStream, streamResponses: stream}
}

var (
	_ chatcompletions.ChatAdapter       = (*Bridge)(nil)
	_ chatcompletions.StreamChatAdapter = (*Bridge)(nil)
)

// ChatCompletions 非流式桥接：chat 请求 → POST /responses → chat 响应。
func (b *Bridge) ChatCompletions(ctx context.Context, ch channel.Runtime, req chatcompletions.ChatRequest) (*chatcompletions.ChatResponse, error) {
	if b.responses == nil {
		return nil, bridgeUnavailable()
	}
	body, err := buildResponsesBody(req, false)
	if err != nil {
		return nil, err
	}
	resp, err := b.responses.CreateResponse(ctx, ch, responses.Request{Body: body})
	if err != nil {
		return nil, err
	}
	return chatResponseFromResponses(resp)
}

// StreamChatCompletions 流式桥接：Responses SSE 事件逐个译成 chat chunk。
//
// 权威首字（ADR-0017）：只有 response.output_text.delta 会产出携带 Content 的 chunk；
// response.created / in_progress 等生命周期事件绝不触发 emit——它们不是生成内容，
// 判成首字会让 TTFT 样本失真并влиять五项评分 25% 权重项。
func (b *Bridge) StreamChatCompletions(ctx context.Context, ch channel.Runtime, req chatcompletions.ChatRequest, emit func(chatcompletions.ChatStreamChunk) error) (adapter.StreamOutcome, error) {
	if b.streamResponses == nil {
		return adapter.StreamOutcome{}, bridgeUnavailable()
	}
	body, err := buildResponsesBody(req, true)
	if err != nil {
		return adapter.StreamOutcome{}, err
	}
	translator := newStreamTranslator(req.Model, emit)
	outcome, err := b.streamResponses.StreamResponse(ctx, ch, responses.Request{Body: body}, translator.consume)
	if err != nil {
		return adapter.StreamOutcome{}, err
	}
	if flushErr := translator.finish(); flushErr != nil {
		return adapter.StreamOutcome{}, flushErr
	}
	return outcome, nil
}

func bridgeUnavailable() error {
	return failure.New(
		failure.CodeGatewayAdapterNotRegistered,
		failure.WithMessage("chat-to-responses bridge has no responses adapter"),
	)
}
