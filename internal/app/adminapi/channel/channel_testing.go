package channel

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channeltest"
)

// ChannelTestService 定义 adminapi 触发渠道主动检测 + 查询检测日志所需的最小能力。
type ChannelTestService interface {
	Test(ctx context.Context, in channeltest.TestInput) (channeltest.TestResult, error)
	ListLogs(ctx context.Context, channelID int64, limit, offset int32) ([]channeltest.LogEntry, int64, error)
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
	Success       bool    `json:"success"`
	LatencyMs     int64   `json:"latency_ms"`
	TestedModel   string  `json:"tested_model"`
	HTTPStatus    int     `json:"http_status"`
	ErrorCode     *string `json:"error_code"`
	Message       string  `json:"message"`
	UpstreamError *string `json:"upstream_error"`
	TestedAt      string  `json:"tested_at"`
}

type channelTestHandler struct {
	service ChannelTestService
}

func (h *channelTestHandler) test(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	// 请求体可选：DecodeJSON 对 JSON content-type 的空 body 返回零值（model 空 = 自动选模型）。
	var req channelTestRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	result, err := h.service.Test(r.Context(), channeltest.TestInput{
		ChannelID: id,
		Model:     req.Model,
		Source:    channeltest.SourceManual,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toChannelTestResultDTO(result))
}

// channelTestLogDTO 是一条渠道检测/凭据事件日志（详情页「检测日志」区块）。
type channelTestLogDTO struct {
	ID                            int64   `json:"id"`
	CreatedAt                     string  `json:"created_at"`
	Source                        string  `json:"source"`
	Success                       bool    `json:"success"`
	ErrorCode                     *string `json:"error_code"`
	HTTPStatus                    *int    `json:"http_status"`
	LatencyMs                     int64   `json:"latency_ms"`
	TestedModel                   string  `json:"tested_model"`
	CredentialValidAfter          bool    `json:"credential_valid_after"`
	Message                       string  `json:"message"`
	UpstreamError                 *string `json:"upstream_error"`
	TestedEndpointBaseURLRevision *int64  `json:"tested_endpoint_base_url_revision"`
	TestedEndpointStatusRevision  *int64  `json:"tested_endpoint_status_revision"`
	TestedConfigRevision          *int64  `json:"tested_config_revision"`
	StateChangeApplied            bool    `json:"state_change_applied"`
}

// testLogs 分页返回某渠道的检测日志（GET /channels/{id}/test-logs）。
func (h *channelTestHandler) testLogs(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	page := adminhttp.ParsePage(r)
	logs, total, err := h.service.ListLogs(r.Context(), id, page.Limit(), page.Offset())
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	out := make([]channelTestLogDTO, 0, len(logs))
	for _, l := range logs {
		dto := channelTestLogDTO{
			ID:                            l.ID,
			CreatedAt:                     l.CreatedAt.UTC().Format(time.RFC3339),
			Source:                        l.Source,
			Success:                       l.Success,
			LatencyMs:                     l.LatencyMs,
			TestedModel:                   l.TestedModel,
			CredentialValidAfter:          l.CredentialValidAfter,
			Message:                       l.Message,
			TestedEndpointBaseURLRevision: l.TestedEndpointBaseURLRevision,
			TestedEndpointStatusRevision:  l.TestedEndpointStatusRevision,
			TestedConfigRevision:          l.TestedConfigRevision,
			StateChangeApplied:            l.StateChangeApplied,
		}
		if l.ErrorCode != "" {
			code := l.ErrorCode
			dto.ErrorCode = &code
		}
		if l.HTTPStatus > 0 {
			status := l.HTTPStatus
			dto.HTTPStatus = &status
		}
		if l.UpstreamError != "" {
			ue := l.UpstreamError
			dto.UpstreamError = &ue
		}
		out = append(out, dto)
	}

	adminhttp.WriteList(w, http.StatusOK, out, page, total)
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
	if r.UpstreamError != "" {
		ue := r.UpstreamError
		dto.UpstreamError = &ue
	}
	return dto
}
