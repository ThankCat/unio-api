package responses

import (
	"context"
	"strings"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
)

// compact_response.go 实现 POST /v1/responses/compact 的无状态降级压缩（DEC-014 / GAP-11-007）。
//
// Unio 无服务端会话存储：把待压缩历史（input[]）+ 压缩指令（instructions）当作一次普通非流式
// chat 请求，复用 executeNonStreamChat 走完整 routing/authorization/计费/lifecycle，再把模型摘要
// 包成 {"output":[message]} 返回。第一版用单条 assistant message 承载摘要，不签发 compaction
// 密文 item：Codex 把 output 当作压缩后的会话历史，下一轮原样回传即按普通 message 解析（透明往返）。

// defaultCompactionInstruction 在 compact 请求未携带 instructions 时注入的兜底压缩指令。
//
// Codex 通常会自带解析好的压缩指令；缺省时若直接发历史，模型会续写而非压缩，故注入显式摘要指令。
const defaultCompactionInstruction = "Summarize the conversation so far into a concise summary that preserves all important context, decisions, file paths, and open tasks. Respond with only the summary."

// CompactHistory 无状态降级压缩会话历史，返回压缩后的新历史 {"output":[ResponseItem,...]}。
func (s *ResponsesService) CompactHistory(ctx context.Context, req gatewayapi.ResponsesRequest) (*gatewayapi.CompactHistoryResponse, error) {
	if req.Instructions == nil || strings.TrimSpace(*req.Instructions) == "" {
		def := defaultCompactionInstruction
		req.Instructions = &def
	}

	chatResp, err := s.executeNonStreamChat(ctx, req)
	if err != nil {
		return nil, err
	}

	resp := mapChatResponseToCompaction(*chatResp)
	return &resp, nil
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
