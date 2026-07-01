package adminapi

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/admin/channeltest"
)

// ChannelTestService 定义 adminapi 触发渠道主动检测所需的最小能力。
type ChannelTestService interface {
	Test(ctx context.Context, in channeltest.TestInput) (channeltest.TestResult, error)
}

// channelTestRequest 是 POST /channels/{id}/test 的请求体（字段均可选）。
// model 省略：自动取渠道第一个启用绑定模型；stream 阶段一忽略（只发非流式最小请求）。
type channelTestRequest struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

// channelTestResultDTO 是渠道检测结果响应体。
//
// 始终返回 HTTP 200（检测本身已成功执行），用 success 表达渠道是否健康——与 new-api 一致，
// 便于前端统一处理。error_code 成功时为 null。
type channelTestResultDTO struct {
	Success     bool    `json:"success"`
	LatencyMs   int64   `json:"latency_ms"`
	TestedModel string  `json:"tested_model"`
	HTTPStatus  int     `json:"http_status"`
	ErrorCode   *string `json:"error_code"`
	Message     string  `json:"message"`
	TestedAt    string  `json:"tested_at"`
}

type channelTestHandler struct {
	service ChannelTestService
}

func (h *channelTestHandler) test(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	// 请求体可选：DecodeJSON 对 JSON content-type 的空 body 返回零值（model 空 = 自动选模型）。
	var req channelTestRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	result, err := h.service.Test(r.Context(), channeltest.TestInput{
		ChannelID: id,
		Model:     req.Model,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toChannelTestResultDTO(result))
}

func toChannelTestResultDTO(r channeltest.TestResult) channelTestResultDTO {
	dto := channelTestResultDTO{
		Success:     r.Success,
		LatencyMs:   r.LatencyMs,
		TestedModel: r.TestedModel,
		HTTPStatus:  r.HTTPStatus,
		Message:     r.Message,
		TestedAt:    r.TestedAt.UTC().Format(time.RFC3339),
	}
	if r.ErrorCode != "" {
		code := r.ErrorCode
		dto.ErrorCode = &code
	}
	return dto
}
