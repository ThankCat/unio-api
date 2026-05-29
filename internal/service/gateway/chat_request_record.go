package gateway

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ThankCat/unio-api/internal/app/gatewayapi"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/observability/logfields"
)

const maxRequestLogInternalErrorDetailBytes = 2048

// createRequestRecord 创建用户可见请求记录，并立即推进到 running 状态。
// request_records.request_id 由服务端生成，用作数据库唯一事实 ID；
// HTTP X-Request-ID 只作为日志 correlation id，不能直接复用为账务请求 ID。
func (s *ChatCompletionService) createRequestRecord(ctx context.Context, principal *auth.APIKeyPrincipal, req gatewayapi.ChatCompletionRequest, stream bool) (requestlog.RequestRecord, error) {
	requestID, err := requestlog.GenerateRequestID()
	if err != nil {
		return requestlog.RequestRecord{}, err
	}

	// 把业务 request_id 写入结构化日志字段，使其与 HTTP correlation_id 在同一条访问日志可关联。
	logfields.SetRequestID(ctx, requestID)

	record, err := s.requestLog.CreateRequest(ctx, requestlog.CreateRequestParams{
		RequestID:        requestID,
		UserID:           principal.UserID,
		ProjectID:        principal.ProjectID,
		APIKeyID:         principal.APIKeyID,
		RequestedModelID: req.Model,
		Stream:           stream,
		StartedAt:        time.Now(),
	})
	if err != nil {
		return requestlog.RequestRecord{}, err
	}

	record, err = s.requestLog.MarkRequestRunning(ctx, record.ID)
	if err != nil {
		return requestlog.RequestRecord{}, err
	}

	return record, nil
}

// createAttemptRecord 创建一次上游 channel 尝试记录。
// attempt 记录 fallback 链路中的单次 provider/channel 调用，必须先于 adapter 调用创建。
func (s *ChatCompletionService) createAttemptRecord(ctx context.Context, requestRecord requestlog.RequestRecord, attemptIndex int, candidate routing.ChatRouteCandidate) (requestlog.AttemptRecord, error) {
	attempt, err := s.requestLog.CreateAttempt(ctx, requestlog.CreateAttemptParams{
		RequestRecordID: requestRecord.ID,
		AttemptIndex:    attemptIndex,
		ProviderID:      candidate.ProviderID,
		ChannelID:       candidate.Channel.ID,
		AdapterKey:      candidate.AdapterKey,
		UpstreamModel:   candidate.UpstreamModel,
		StartedAt:       time.Now(),
	})
	if err != nil {
		return requestlog.AttemptRecord{}, err
	}

	return attempt, nil
}

// markRequestRecordSucceeded 把请求标记为成功，并记录最终响应模型和 provider/channel。
func (s *ChatCompletionService) markRequestRecordSucceeded(ctx context.Context, requestRecord requestlog.RequestRecord, req gatewayapi.ChatCompletionRequest, candidate routing.ChatRouteCandidate) error {
	_, err := s.requestLog.MarkRequestSucceeded(ctx, requestlog.MarkRequestSucceededParams{
		ID:              requestRecord.ID,
		ResponseModelID: req.Model,
		FinalProviderID: candidate.ProviderID,
		FinalChannelID:  candidate.Channel.ID,
		CompletedAt:     time.Now(),
	})
	return err
}

// markAttemptRecordSucceeded 把一次上游尝试标记为成功，并记录上游实际响应模型。
func (s *ChatCompletionService) markAttemptRecordSucceeded(ctx context.Context, attempt requestlog.AttemptRecord, upstreamModel string) error {
	_, err := s.requestLog.MarkAttemptSucceeded(ctx, requestlog.MarkAttemptSucceededParams{
		ID:                    attempt.ID,
		UpstreamResponseModel: upstreamModel,
		UpstreamStatusCode:    200,
		UpstreamRequestID:     nil,
		CompletedAt:           time.Now(),
	})
	return err
}

// routingFailureCode 将 routing 内部错误转换成 request_records.error_code。
func routingFailureCode(err error) string {
	if code := failure.CodeOf(err); code != "" {
		return string(code)
	}

	switch {
	case errors.Is(err, routing.ErrModelNotFound):
		return "model_not_found"
	case errors.Is(err, routing.ErrModelNotAvailable):
		return "model_not_available"
	case errors.Is(err, routing.ErrNoAvailableChannel):
		return "no_available_channel"
	default:
		return "routing_error"
	}
}

// markRequestRecordFailed 把 request record 标记为失败。
// 失败状态写入是审计动作，调用方仍然返回原始业务错误，避免状态写入细节覆盖根因。
func (s *ChatCompletionService) markRequestRecordFailed(ctx context.Context, requestRecord requestlog.RequestRecord, code string, err error) {
	errorCode, safeMessage, internalDetail := requestLogErrorFacts(code, err)

	_, _ = s.requestLog.MarkRequestFailed(ctx, requestlog.MarkRequestFailedParams{
		ID:                  requestRecord.ID,
		ErrorCode:           errorCode,
		ErrorMessage:        safeMessage,
		InternalErrorDetail: internalDetail,
		CompletedAt:         time.Now(),
	})
}

// markAttemptRecordFailed 把一次上游尝试标记为失败。
// 失败状态写入是审计动作，调用方仍然返回原始业务错误，避免状态写入细节覆盖根因。
func (s *ChatCompletionService) markAttemptRecordFailed(ctx context.Context, attempt requestlog.AttemptRecord, code string, err error) {
	errorCode, safeMessage, internalDetail := requestLogErrorFacts(code, err)

	_, _ = s.requestLog.MarkAttemptFailed(ctx, requestlog.MarkAttemptFailedParams{
		ID:                  attempt.ID,
		ErrorCode:           errorCode,
		ErrorMessage:        safeMessage,
		InternalErrorDetail: internalDetail,
		CompletedAt:         time.Now(),
	})
}

func failureCodeOrFallback(err error, fallback string) string {
	if code := failure.CodeOf(err); code != "" {
		return string(code)
	}

	return fallback
}

// markRequestCanceled 把 request record 和当前 attempt 标记为客户端取消。
func (s *ChatCompletionService) markRequestCanceled(ctx context.Context, requestRecord requestlog.RequestRecord, attemptRecord requestlog.AttemptRecord, err error) {
	errorCode, safeMessage, internalDetail := requestLogErrorFacts("client_canceled", err)

	// 账务 release 或 risk_exposure 由调用方在进入 canceled 状态前处理；这里仅写请求审计状态。
	// 客户端断开时原请求 ctx 通常已经取消；这里脱离请求取消，
	// 给审计写入一个很短的补偿窗口，避免 canceled 状态写不进去。
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()

	_, _ = s.requestLog.MarkAttemptCanceled(auditCtx, requestlog.MarkAttemptCanceledParams{
		ID:                  attemptRecord.ID,
		ErrorCode:           errorCode,
		ErrorMessage:        safeMessage,
		InternalErrorDetail: internalDetail,
		CompletedAt:         time.Now(),
	})

	_, _ = s.requestLog.MarkRequestCanceled(auditCtx, requestlog.MarkRequestCanceledParams{
		ID:                  requestRecord.ID,
		ErrorCode:           errorCode,
		ErrorMessage:        safeMessage,
		InternalErrorDetail: internalDetail,
		CompletedAt:         time.Now(),
	})
}

// requestLogErrorFacts 生成 request log 的安全错误摘要和内部诊断详情。
// error_message 只保存可展示文案；internal_error_detail 才保存截断后的内部错误文本。
func requestLogErrorFacts(fallbackCode string, err error) (errorCode string, safeMessage string, internalDetail string) {
	errorCode = failureCodeOrFallback(err, fallbackCode)
	return errorCode, safeRequestLogErrorMessage(errorCode), internalErrorDetail(err)
}

// safeRequestLogErrorMessage 将内部错误码映射成可展示的安全文案。
// 这里不能使用 err.Error()，避免把 SQL、上游响应、路径或配置细节暴露给后台/console 日志展示。
func safeRequestLogErrorMessage(code string) string {
	switch code {
	case "client_canceled":
		return "Request was canceled by the client."
	case "model_not_found", string(failure.CodeRoutingModelNotFound):
		return "The requested model was not found."
	case "model_not_available", "no_available_channel", string(failure.CodeRoutingModelNotAvailable), string(failure.CodeRoutingNoAvailableChannel):
		return "The requested model is temporarily unavailable."
	case "chat_authorization_failed", string(failure.CodeGatewayChatAuthorizationFailed):
		return "Request authorization failed."
	case "chat_authorization_release_failed":
		return "Request billing cleanup failed."
	case "chat_settlement_failed", "stream_chat_settlement_failed", string(failure.CodeGatewayChatSettlementFailed):
		return "Request settlement failed."
	case string(failure.CodeLedgerInsufficientBalance):
		return "Insufficient balance."
	case string(failure.CodeGatewayStreamUsageMissing):
		return "Stream usage is missing."
	}

	switch failure.Code(code).Category() {
	case failure.CategoryAdapter:
		return "Upstream provider request failed."
	case failure.CategoryRouting:
		return "Request routing failed."
	case failure.CategoryLedger, failure.CategoryBilling:
		return "Request billing failed."
	case failure.CategoryGateway:
		return "Gateway request failed."
	default:
		return "Request failed."
	}
}

// internalErrorDetail 返回供内部排查使用的错误详情，并限制长度避免请求日志行无限膨胀。
func internalErrorDetail(err error) string {
	if err == nil {
		return ""
	}

	detail := strings.TrimSpace(err.Error())
	if len(detail) <= maxRequestLogInternalErrorDetailBytes {
		return detail
	}

	return detail[:maxRequestLogInternalErrorDetailBytes] + "...[truncated]"
}
