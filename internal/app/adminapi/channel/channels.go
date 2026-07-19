package channel

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channel"
)

// ChannelService 定义 adminapi 操作 channel 所需的最小能力。
type ChannelService interface {
	List(ctx context.Context, params channel.ListParams) (channel.ListResult, error)
	Get(ctx context.Context, id int64) (channel.Channel, error)
	Create(ctx context.Context, in channel.CreateInput) (channel.Channel, error)
	Update(ctx context.Context, in channel.UpdateInput) (channel.Channel, error)
	RotateCredential(ctx context.Context, in channel.RotateCredentialInput) error
	Delete(ctx context.Context, id int64) error
	Archive(ctx context.Context, id int64, replacementChannelID *int64) error
	Restore(ctx context.Context, id int64) error
	// AdapterKeyOptions 列出可选 adapter_key 枚举，供前端下拉而非手填。
	AdapterKeyOptions() []channel.AdapterKeyOption
}

// channelDTO 是 channel 的 admin API 响应体；含明文上游凭据（产品决策：渠道凭据明文，管理端可查看/复制）。
// ProviderName 仅分页列表场景有值；单条读取/写入返回为空。
type channelDTO struct {
	ID           int64  `json:"id"`
	ProviderID   int64  `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	Name         string `json:"name"`
	Protocol     string `json:"protocol"`
	AdapterKey   string `json:"adapter_key"`
	BaseURL      string `json:"base_url"`
	// Credential 是明文上游 API key（产品决策：明文存储，管理端可查看/复制/编辑）。
	Credential string `json:"credential"`
	Status     string `json:"status"`
	Priority   int32  `json:"priority"`
	TimeoutMs  *int32 `json:"timeout_ms"`
	// RPMLimit/TPMLimit/RPDLimit：渠道级限流上限（P2-8）。null=继承全局默认，0=不限，>0=具体上限。
	RPMLimit *int64 `json:"rpm_limit"`
	TPMLimit *int64 `json:"tpm_limit"`
	RPDLimit *int64 `json:"rpd_limit"`
	// ConcurrencyLimit：渠道在途并发上限（DEC-029）。null=继承全局默认，0=不限，>0=具体上限。
	ConcurrencyLimit *int64 `json:"concurrency_limit"`
	// BillsOnDisconnect：上游「断开仍计费」标记（DESIGN-bill-on-cancel 阶段一）。
	// true 时失败/取消路径记平台成本敞口，纯观测不影响路由与客户计费。
	BillsOnDisconnect bool    `json:"upstream_bills_on_disconnect"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
	ArchivedAt        *string `json:"archived_at"`
	// LastTest*：最近一次主动检测结果（渠道检测，阶段一）。全 null 表示从未检测。
	LastTestedAt      *string `json:"last_tested_at"`
	LastTestOK        *bool   `json:"last_test_ok"`
	LastTestLatencyMs *int32  `json:"last_test_latency_ms"`
	LastTestError     *string `json:"last_test_error"`
}

// adapterKeyOptionDTO 是某协议族下一个可选 adapter_key 的枚举项，供前端下拉渲染。
// is_default=true 表示与协议同名的忠实透传 adapter（创建时 adapter_key 留空即取它）。
type adapterKeyOptionDTO struct {
	Protocol   string `json:"protocol"`
	AdapterKey string `json:"adapter_key"`
	IsDefault  bool   `json:"is_default"`
}

// rateLimitsRequest 是渠道级限流的可选嵌套对象（P2-8，渠道层保护上游）。
// 整个对象缺省=不变；存在即原子替换三维：各字段 null/缺省=继承全局默认(NULL)，数字（含 0）=显式设定（0=不限）。
// 注：Key/线路侧限流已归线路（DEC-027），此类型仅服务于渠道级限流。
type rateLimitsRequest struct {
	RPM *int64 `json:"rpm"`
	TPM *int64 `json:"tpm"`
	RPD *int64 `json:"rpd"`
	// Concurrency 是渠道在途并发上限（DEC-029），语义同其余维度：null=继承全局默认，0=不限。
	Concurrency *int64 `json:"concurrency"`
}

// validateRateLimits 校验限流值非负（限流上限不能为负数）。
func validateRateLimits(rl *rateLimitsRequest) error {
	if rl == nil {
		return nil
	}
	for field, v := range map[string]*int64{"rpm": rl.RPM, "tpm": rl.TPM, "rpd": rl.RPD, "concurrency": rl.Concurrency} {
		if v != nil && *v < 0 {
			return failure.New(
				failure.CodeAdminInvalidArgument,
				failure.WithMessage("rate limit must be a non-negative integer (0 means unlimited)"),
				failure.WithField("field", "rate_limits."+field),
			)
		}
	}
	return nil
}

type createChannelRequest struct {
	ProviderID int64              `json:"provider_id"`
	Name       string             `json:"name"`
	Protocol   string             `json:"protocol"`
	AdapterKey string             `json:"adapter_key"`
	BaseURL    string             `json:"base_url"`
	Credential string             `json:"credential"`
	Status     string             `json:"status"`
	Priority   int32              `json:"priority"`
	TimeoutMs  *int32             `json:"timeout_ms"`
	RateLimits *rateLimitsRequest `json:"rate_limits"` // 可选渠道级限流；不传表示全继承全局默认
	// BillsOnDisconnect 可选：上游「断开仍计费」标记；缺省=false（正常上游）。
	BillsOnDisconnect *bool `json:"upstream_bills_on_disconnect"`
}

type updateChannelRequest struct {
	Name       string             `json:"name"`
	BaseURL    string             `json:"base_url"`
	Status     string             `json:"status"`
	Priority   int32              `json:"priority"`
	TimeoutMs  *int32             `json:"timeout_ms"`
	RateLimits *rateLimitsRequest `json:"rate_limits"` // 对象缺省=不变，存在即原子替换三维限流
	// BillsOnDisconnect 可选：上游「断开仍计费」标记；缺省=不变。
	BillsOnDisconnect *bool `json:"upstream_bills_on_disconnect"`
}

type rotateChannelCredentialRequest struct {
	Credential string `json:"credential"`
}

type channelsHandler struct {
	service ChannelService
}

func (h *channelsHandler) list(w http.ResponseWriter, r *http.Request) {
	providerID := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("provider_id")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			adminhttp.WriteServiceError(w, failure.New(
				failure.CodeAdminInvalidArgument,
				failure.WithMessage("provider_id query must be a positive integer"),
				failure.WithField("field", "provider_id"),
			))
			return
		}
		providerID = parsed
	}

	page := adminhttp.ParsePage(r)

	result, err := h.service.List(r.Context(), channel.ListParams{
		ProviderID: providerID,
		Status:     adminhttp.ListStatus(r),
		Query:      strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:      page.Limit(),
		Offset:     page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]channelDTO, 0, len(result.Items))
	for _, c := range result.Items {
		dtos = append(dtos, toChannelDTO(c))
	}

	adminhttp.WriteList(w, http.StatusOK, dtos, page, result.Total)
}

// adapterKeys 返回当前进程注册的全部可选 adapter_key（按协议分组项），供前端下拉。
func (h *channelsHandler) adapterKeys(w http.ResponseWriter, _ *http.Request) {
	options := h.service.AdapterKeyOptions()
	dtos := make([]adapterKeyOptionDTO, 0, len(options))
	for _, o := range options {
		dtos = append(dtos, adapterKeyOptionDTO{
			Protocol:   o.Protocol,
			AdapterKey: o.AdapterKey,
			IsDefault:  o.IsDefault,
		})
	}
	adminhttp.WriteData(w, http.StatusOK, dtos)
}

func (h *channelsHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	c, err := h.service.Get(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toChannelDTO(c))
}

func (h *channelsHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createChannelRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	if err := validateRateLimits(req.RateLimits); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	in := channel.CreateInput{
		ProviderID: req.ProviderID,
		Name:       req.Name,
		Protocol:   req.Protocol,
		AdapterKey: req.AdapterKey,
		BaseURL:    req.BaseURL,
		Credential: req.Credential,
		Status:     req.Status,
		Priority:   req.Priority,
		TimeoutMs:  req.TimeoutMs,
	}
	if req.RateLimits != nil {
		in.RateLimitsProvided = true
		in.RPMLimit = req.RateLimits.RPM
		in.TPMLimit = req.RateLimits.TPM
		in.RPDLimit = req.RateLimits.RPD
		in.ConcurrencyLimit = req.RateLimits.Concurrency
	}
	in.BillsOnDisconnect = req.BillsOnDisconnect

	c, err := h.service.Create(r.Context(), in)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusCreated, toChannelDTO(c))
}

func (h *channelsHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req updateChannelRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	if err := validateRateLimits(req.RateLimits); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	in := channel.UpdateInput{
		ID:        id,
		Name:      req.Name,
		BaseURL:   req.BaseURL,
		Status:    req.Status,
		Priority:  req.Priority,
		TimeoutMs: req.TimeoutMs,
	}
	if req.RateLimits != nil {
		in.RateLimitsProvided = true
		in.RPMLimit = req.RateLimits.RPM
		in.TPMLimit = req.RateLimits.TPM
		in.RPDLimit = req.RateLimits.RPD
		in.ConcurrencyLimit = req.RateLimits.Concurrency
	}
	in.BillsOnDisconnect = req.BillsOnDisconnect

	c, err := h.service.Update(r.Context(), in)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toChannelDTO(c))
}

func (h *channelsHandler) rotateCredential(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req rotateChannelCredentialRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	if err := h.service.RotateCredential(r.Context(), channel.RotateCredentialInput{
		ID:         id,
		Credential: req.Credential,
	}); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	// 凭据只写不回；轮换成功返回 204 无响应体。
	w.WriteHeader(http.StatusNoContent)
}

func (h *channelsHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if err := h.service.Delete(r.Context(), id); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *channelsHandler) archive(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req struct {
		ReplacementChannelID *int64 `json:"replacement_channel_id"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil && !errors.Is(err, httpx.ErrEmptyJSONBody) {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if err := h.service.Archive(r.Context(), id, req.ReplacementChannelID); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *channelsHandler) restore(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if err := h.service.Restore(r.Context(), id); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toChannelDTO(c channel.Channel) channelDTO {
	return channelDTO{
		ID:                c.ID,
		ProviderID:        c.ProviderID,
		ProviderName:      c.ProviderName,
		Name:              c.Name,
		Protocol:          c.Protocol,
		AdapterKey:        c.AdapterKey,
		BaseURL:           c.BaseURL,
		Credential:        c.Credential,
		Status:            c.Status,
		Priority:          c.Priority,
		TimeoutMs:         c.TimeoutMs,
		RPMLimit:          c.RPMLimit,
		TPMLimit:          c.TPMLimit,
		RPDLimit:          c.RPDLimit,
		ConcurrencyLimit:  c.ConcurrencyLimit,
		BillsOnDisconnect: c.BillsOnDisconnect,
		CreatedAt:         c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         c.UpdatedAt.UTC().Format(time.RFC3339),
		ArchivedAt:        formatOptionalTime(c.ArchivedAt),

		LastTestedAt:      formatOptionalTime(c.LastTestedAt),
		LastTestOK:        c.LastTestOK,
		LastTestLatencyMs: c.LastTestLatencyMs,
		LastTestError:     c.LastTestError,
	}
}

// formatOptionalTime 把可空时间格式化成 RFC3339 字符串指针（nil 保持 nil）。
func formatOptionalTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}
