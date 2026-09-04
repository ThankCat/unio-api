package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channeltest"
)

// ChannelTestService 定义 adminapi 触发渠道主动检测 + 查询检测日志所需的最小能力。
type ChannelTestService interface {
	Test(ctx context.Context, in channeltest.TestInput) (channeltest.TestResult, error)
	TestStream(ctx context.Context, in channeltest.TestInput, emit func(channeltest.ProbeEvent)) (channeltest.TestResult, error)
	ListLogs(ctx context.Context, channelID int64, limit, offset int32) ([]channeltest.LogEntry, int64, error)
}

// channelTestRequest 是 POST /channels/{id}/test 的请求体（字段均可选）。
// model 省略：自动取渠道第一个启用绑定模型；stream 阶段一忽略（只发非流式最小请求）。
// account_id 仅池型渠道有意义：按号检测；省略时自动选号（enabled 中 priority 最小者）。
type channelTestRequest struct {
	Model     string `json:"model"`
	Stream    bool   `json:"stream"`
	AccountID int64  `json:"account_id"`
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
	// 池型渠道：本次检测使用的账号（credential 型为 null/空）。
	TestedAccountID   *int64 `json:"tested_account_id,omitempty"`
	TestedAccountName string `json:"tested_account_name,omitempty"`
	// AccountUsage：本次上游响应携带的账号用量水位（成功头 / 429 失败头同源；无观测省略）。
	// 上游对水位满的最小探测可能仍回 200——弹窗需要它来解释「检测通过但账号已满/已暂停」。
	AccountUsage *accountUsageDTO `json:"account_usage,omitempty"`
	// AccountRuntime：检测处置落地后的账号运行态回读（冷却/隔离/暂停剩余；credential 型省略）。
	AccountRuntime *accountRuntimeDTO `json:"account_runtime,omitempty"`
}

// accountUsageDTO 是账号用量水位观测（字段口径与账号列表 usage_snapshot 一致）。
type accountUsageDTO struct {
	PlanType  string                 `json:"plan_type,omitempty"`
	Primary   *accountUsageWindowDTO `json:"primary,omitempty"`
	Secondary *accountUsageWindowDTO `json:"secondary,omitempty"`
}

type accountUsageWindowDTO struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int64   `json:"window_minutes,omitempty"`
	ResetAt       int64   `json:"reset_at,omitempty"`
}

// accountRuntimeDTO 与账号列表接口的 runtime 字段同构（同一 Redis 事实）。
type accountRuntimeDTO struct {
	CooldownRemainingMs      int64  `json:"cooldown_remaining_ms"`
	CooldownWindow           string `json:"cooldown_window,omitempty"`
	UnschedulableRemainingMs int64  `json:"unschedulable_remaining_ms"`
	UnschedulableReason      string `json:"unschedulable_reason,omitempty"`
	UsagePauseRemainingMs    int64  `json:"usage_pause_remaining_ms"`
	UsagePauseWindow         string `json:"usage_pause_window,omitempty"`
	InFlight                 int64  `json:"in_flight"`
}

type channelTestHandler struct {
	service ChannelTestService
}

func (h *channelTestHandler) test(w http.ResponseWriter, r *http.Request) {
	// 手动探测有独立的运行时 probe timeout，避免 server 级短 WriteTimeout 在探测完成前切断连接。
	if err := httpx.ClearResponseWriteDeadline(w); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
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
		AccountID: req.AccountID,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toChannelTestResultDTO(result))
}

// channelTestStreamEvent 是流式检测（SSE）的单个事件负载。
//
// 事件时序：test_start（每次真实上游尝试各一条，自动选模型顺延时会出现多条）→
// content*（流式探测的响应文本增量，非流式探测无此事件）→ test_complete（终态，带完整检测结果）。
// error 仅在检测已开始推流后编排失败（如落库失败）时出现，替代 test_complete 收尾。
type channelTestStreamEvent struct {
	Type        string                `json:"type"`
	Model       string                `json:"model,omitempty"`
	AccountName string                `json:"account_name,omitempty"`
	Text        string                `json:"text,omitempty"`
	Error       string                `json:"error,omitempty"`
	Result      *channelTestResultDTO `json:"result,omitempty"`
}

// testStream 是 POST /channels/{id}/test/stream：与 test 同一条检测编排（同样落库），
// 以 SSE 实时推送验证过程（供账号/渠道检测弹窗展示）。探测失败不是 HTTP 错误——
// 终态一律 test_complete（result.success 表达健康与否）；4xx 编排错误发生在推流前，回标准错误 JSON。
func (h *channelTestHandler) testStream(w http.ResponseWriter, r *http.Request) {
	// 手动探测有独立的运行时 probe timeout，避免 server 级短 WriteTimeout 在探测完成前切断连接。
	if err := httpx.ClearResponseWriteDeadline(w); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req channelTestRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		adminhttp.WriteServiceError(w, fmt.Errorf("response writer does not support streaming"))
		return
	}

	// SSE 头延迟到首个事件再写：编排错误（非法参数/渠道不存在/池空）全部发生在首次探测前，
	// 未推流时仍可回标准错误 JSON，前端错误处理与非流式检测同一条路径。
	started := false
	writeEvent := func(ev channelTestStreamEvent) {
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			// 反代（如 nginx）默认缓冲响应会把过程事件攒到最后一次性吐出，SSE 必须显式关闭。
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			started = true
		}
		payload, err := json.Marshal(ev)
		if err != nil {
			return
		}
		// 写失败（弹窗被关闭）不中断探测：检测结果必须完成落库。
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}

	result, err := h.service.TestStream(r.Context(), channeltest.TestInput{
		ChannelID: id,
		Model:     req.Model,
		Source:    channeltest.SourceManual,
		AccountID: req.AccountID,
	}, func(ev channeltest.ProbeEvent) {
		switch ev.Type {
		case channeltest.ProbeEventStart:
			writeEvent(channelTestStreamEvent{Type: "test_start", Model: ev.Model, AccountName: ev.AccountName})
		case channeltest.ProbeEventContent:
			writeEvent(channelTestStreamEvent{Type: "content", Text: ev.Text})
		}
	})
	if err != nil {
		if !started {
			adminhttp.WriteServiceError(w, err)
			return
		}
		// 已推流后编排失败（如结果落库失败）：SSE 内收尾，不透出内部细节（与 5xx 通用文案同纪律）。
		writeEvent(channelTestStreamEvent{Type: "error", Error: "检测执行失败，请稍后重试"})
		return
	}

	dto := toChannelTestResultDTO(result)
	writeEvent(channelTestStreamEvent{Type: "test_complete", Result: &dto})
}

// channelTestLogDTO 是一条渠道检测/凭据事件日志（详情页「检测日志」区块）。
type channelTestLogDTO struct {
	ID                           int64   `json:"id"`
	CreatedAt                    string  `json:"created_at"`
	Source                       string  `json:"source"`
	Success                      bool    `json:"success"`
	ErrorCode                    *string `json:"error_code"`
	HTTPStatus                   *int    `json:"http_status"`
	LatencyMs                    int64   `json:"latency_ms"`
	TestedModel                  string  `json:"tested_model"`
	CredentialValidAfter         bool    `json:"credential_valid_after"`
	Message                      string  `json:"message"`
	UpstreamError                *string `json:"upstream_error"`
	TestedOriginRevision         *int64  `json:"tested_origin_revision"`
	TestedProviderStatusRevision *int64  `json:"tested_status_revision"`
	TestedConfigRevision         *int64  `json:"tested_config_revision"`
	StateChangeApplied           bool    `json:"state_change_applied"`
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
			ID:                           l.ID,
			CreatedAt:                    l.CreatedAt.UTC().Format(time.RFC3339),
			Source:                       l.Source,
			Success:                      l.Success,
			LatencyMs:                    l.LatencyMs,
			TestedModel:                  l.TestedModel,
			CredentialValidAfter:         l.CredentialValidAfter,
			Message:                      l.Message,
			TestedOriginRevision:         l.TestedOriginRevision,
			TestedProviderStatusRevision: l.TestedProviderStatusRevision,
			TestedConfigRevision:         l.TestedConfigRevision,
			StateChangeApplied:           l.StateChangeApplied,
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
	if r.TestedAccountID > 0 {
		id := r.TestedAccountID
		dto.TestedAccountID = &id
		dto.TestedAccountName = r.TestedAccountName
	}
	dto.AccountUsage = toAccountUsageDTO(r.AccountUsage)
	dto.AccountRuntime = toAccountRuntimeDTO(r.AccountRuntime)
	return dto
}

func toAccountUsageDTO(usage *adapter.AccountUsageFacts) *accountUsageDTO {
	if usage == nil {
		return nil
	}
	dto := &accountUsageDTO{PlanType: usage.PlanType}
	dto.Primary = toAccountUsageWindowDTO(usage.Primary)
	dto.Secondary = toAccountUsageWindowDTO(usage.Secondary)
	if dto.PlanType == "" && dto.Primary == nil && dto.Secondary == nil {
		return nil
	}
	return dto
}

func toAccountUsageWindowDTO(window adapter.AccountUsageWindowFacts) *accountUsageWindowDTO {
	if !window.Present {
		return nil
	}
	dto := &accountUsageWindowDTO{
		UsedPercent:   window.UsedPercent,
		WindowMinutes: window.WindowMinutes,
		ResetAt:       window.ResetAtUnix,
	}
	// 上游只给相对秒时折算成绝对时刻，前端不用再关心两种形态。
	if dto.ResetAt <= 0 && window.ResetAfterSeconds > 0 {
		dto.ResetAt = time.Now().Unix() + window.ResetAfterSeconds
	}
	return dto
}

func toAccountRuntimeDTO(runtime *breakerstore.AccountRuntime) *accountRuntimeDTO {
	if runtime == nil {
		return nil
	}
	return &accountRuntimeDTO{
		CooldownRemainingMs:      runtime.CooldownRemainingMs,
		CooldownWindow:           string(runtime.CooldownWindow),
		UnschedulableRemainingMs: runtime.UnschedulableRemainingMs,
		UnschedulableReason:      string(runtime.UnschedulableReason),
		UsagePauseRemainingMs:    runtime.UsagePauseRemainingMs,
		UsagePauseWindow:         string(runtime.UsagePauseWindow),
		InFlight:                 runtime.InFlight,
	}
}
