package responses

import (
	"context"
	"fmt"
	"strings"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// compact_synthetic.go 实现 SyntheticCompact：无状态降级压缩（DEC-014 / GAP-11-007）。
//
// Unio 无服务端会话存储：把待压缩历史（input[]）+ 压缩指令（instructions）当作一次普通非流式 chat 请求，
// 复用共享候选循环走完整 routing/authorization/计费/lifecycle，再把模型摘要包成 {"output":[message]} 返回。
// 第一版用单条 assistant message 承载摘要，不签发 compaction 密文 item（Synthetic 路径限制，GAP-11-007）：
// Codex 把 output 当作压缩后的会话历史，下一轮原样回传即按普通 message 解析（透明往返）。

// defaultCompactionInstruction 在 compact 请求未携带 instructions 时注入的兜底压缩指令。
//
// Codex 通常会自带解析好的压缩指令；缺省时若直接发历史，模型会续写而非压缩，故注入显式摘要指令。
const defaultCompactionInstruction = "Summarize the conversation so far into a concise summary that preserves all important context, decisions, file paths, and open tasks. Respond with only the summary."

// invokeSyntheticCompact 走 chat 摘要降级：把待压缩历史翻译成一次非流式 chat 调用，返回 AttemptSuccess
// 供共享计费循环结算，并把内部 ChatResponse 捕获到 result.synthetic（供编排层映射成 compaction output）。
func (s *ResponsesService) invokeSyntheticCompact(
	ctx context.Context,
	candidate routing.ChatRouteCandidate,
	req gatewayapi.ResponsesRequest,
	chatAdapter chatcompletionsadapter.ChatAdapter,
	result *compactResult,
) (lifecycle.AttemptSuccess, error) {
	if chatAdapter == nil {
		return lifecycle.AttemptSuccess{}, failure.New(
			failure.CodeGatewayAdapterNotRegistered,
			failure.WithMessage(fmt.Sprintf("gateway chat adapter %q not registered", candidate.AdapterKey)),
		)
	}

	chatReq, _ := mapResponsesRequestToChat(req, candidate.UpstreamModel)
	adapterCtx, adapterSpan := lifecycle.StartGatewaySpan(ctx, "adapter.chat_completions", lifecycle.UpstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
	resp, err := chatAdapter.ChatCompletions(adapterCtx, candidate.Channel, chatReq)
	lifecycle.EndGatewaySpan(adapterSpan, err)
	if err != nil {
		return lifecycle.AttemptSuccess{}, err
	}

	result.synthetic = resp
	return lifecycle.AttemptSuccess{ResponseID: resp.ID, Facts: resp.Facts}, nil
}

// mapChatResponseToCompaction 把摘要 ChatResponse 包成 compact 的 {"output":[message]}。
//
// 第一版只承载摘要文本为单条 assistant message（output_text）；摘要为空时返回空 output，
// 由 Codex 决定回退策略。
func mapChatResponseToCompaction(chatResp chatcompletionsadapter.ChatResponse) gatewayapi.CompactHistoryResponse {
	output := make([]gatewayapi.ResponseOutputItem, 0, 1)

	summary := strings.TrimSpace(chatResp.Content)
	if summary != "" {
		output = append(output, gatewayapi.ResponseOutputItem{
			Type:   "message",
			ID:     newResponsesID("msg"),
			Role:   "assistant",
			Status: "completed",
			Content: []gatewayapi.ResponseOutputContent{{
				Type: "output_text",
				Text: summary,
			}},
		})
	}

	return gatewayapi.CompactHistoryResponse{Output: output}
}
