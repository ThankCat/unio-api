package channel

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channel"
	"github.com/ThankCat/unio-gateway/internal/service/admin/supply"
)

// ChannelService 定义 adminapi 操作 channel 所需的最小能力。
type ChannelService interface {
	List(ctx context.Context, params channel.ListParams) (channel.ListResult, error)
	Get(ctx context.Context, id int64) (channel.Channel, error)
	Create(ctx context.Context, in channel.CreateInput) (channel.Channel, error)
	Update(ctx context.Context, in channel.UpdateInput) (channel.Channel, error)
	RotateCredential(ctx context.Context, in channel.RotateCredentialInput) (channel.RotateCredentialResult, error)
	Delete(ctx context.Context, id int64) error
	Archive(ctx context.Context, id int64) error
	Restore(ctx context.Context, id int64) error
	// AdapterKeyOptions 列出可选 adapter_key 枚举，供前端下拉而非手填。
	AdapterKeyOptions() []channel.AdapterKeyOption
}

// channelDTO 是 channel 的 admin API 响应体；含明文上游凭据（产品决策：渠道凭据明文，管理端可查看/复制）。
// ProviderName 仅分页列表场景有值；单条读取/写入返回为空。
type channelDTO struct {
	ID                 int64  `json:"id"`
	ProviderID         int64  `json:"provider_id"`
	ProviderName       string `json:"provider_name"`
	Name               string `json:"name"`
	Protocol           string `json:"protocol"`
	AdapterKey         string `json:"adapter_key"`
	Origin             string `json:"origin"`
	SupportsOpenAIFast bool   `json:"supports_openai_fast"`
	ConfigRevision     int64  `json:"config_revision"`
	CapacityRevision   int64  `json:"capacity_revision"`
	RuntimeSyncPending bool   `json:"runtime_sync_pending"`
	// Credential 是明文上游 API key（产品决策：明文存储，管理端可查看/复制/编辑）。
	Credential          string `json:"credential"`
	Status              string `json:"status"`
	Priority            int32  `json:"priority"`
	ResponseTimeoutMs   *int32 `json:"response_timeout_ms"`
	FirstTokenTimeoutMs *int32 `json:"first_token_timeout_ms"`
	StickyEnabled       *bool  `json:"sticky_enabled"`
	StickyTTLms         *int64 `json:"sticky_ttl_ms"`
	// ConcurrencyLimit：渠道在途并发上限（DEC-029）。null=继承并发默认 channel_limit，0=不限，>0=具体上限。
	ConcurrencyLimit *int64  `json:"concurrency_limit"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
	ArchivedAt       *string `json:"archived_at"`
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

// optionalInt64 区分 PUT 中字段缺省与显式 null。
type optionalInt64 struct {
	Set   bool
	Value *int64
}

func (f *optionalInt64) UnmarshalJSON(raw []byte) error {
	f.Set = true
	if string(raw) == "null" {
		f.Value = nil
		return nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	f.Value = &value
	return nil
}

func validateConcurrencyLimit(value *int64) error {
	if value != nil && *value < 0 {
		return failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("concurrency limit must be a non-negative integer (0 means unlimited)"),
			failure.WithField("field", "concurrency_limit"),
		)
	}
	return nil
}

type createChannelRequest struct {
	ProviderID          int64         `json:"provider_id"`
	Name                string        `json:"name"`
	Protocol            string        `json:"protocol"`
	AdapterKey          string        `json:"adapter_key"`
	Credential          string        `json:"credential"`
	Status              string        `json:"status"`
	Priority            int32         `json:"priority"`
	ResponseTimeoutMs   *int32        `json:"response_timeout_ms"`
	FirstTokenTimeoutMs *int32        `json:"first_token_timeout_ms"`
	StickyEnabled       *bool         `json:"sticky_enabled"`
	StickyTTLms         *int64        `json:"sticky_ttl_ms"`
	ConcurrencyLimit    optionalInt64 `json:"concurrency_limit"`
	SupportsOpenAIFast  bool          `json:"supports_openai_fast"`
}

type updateChannelRequest struct {
	Name                string        `json:"name"`
	ProviderID          int64         `json:"provider_id"`
	Status              string        `json:"status"`
	Priority            int32         `json:"priority"`
	ResponseTimeoutMs   *int32        `json:"response_timeout_ms"`
	FirstTokenTimeoutMs *int32        `json:"first_token_timeout_ms"`
	StickyEnabled       *bool         `json:"sticky_enabled"`
	StickyTTLms         *int64        `json:"sticky_ttl_ms"`
	ConcurrencyLimit    optionalInt64 `json:"concurrency_limit"`
	SupportsOpenAIFast  *bool         `json:"supports_openai_fast"`
	// ConfirmSupplyImpact + ExpectedImpactFingerprint 是停用 Channel 触发 Offering 联动时的
	// ADR-0019 Channel 暂停影响确认；首次请求缺省，收到 409 后携带最新指纹重试。
	ConfirmSupplyImpact       bool   `json:"confirm_supply_impact"`
	ExpectedImpactFingerprint string `json:"expected_impact_fingerprint"`
}

type rotateChannelCredentialRequest struct {
	Credential string `json:"credential"`
}

type rotateCredentialResultDTO struct {
	CredentialSaved       bool                      `json:"credential_saved"`
	CredentialChanged     bool                      `json:"credential_changed"`
	SavedConfigRevision   int64                     `json:"saved_config_revision"`
	Verification          credentialVerificationDTO `json:"verification"`
	CurrentConfigRevision int64                     `json:"current_config_revision"`
}

type credentialVerificationDTO struct {
	State                        string                `json:"state"`
	TestedOriginRevision         *int64                `json:"tested_origin_revision"`
	TestedProviderStatusRevision *int64                `json:"tested_status_revision"`
	TestedConfigRevision         *int64                `json:"tested_config_revision"`
	StateChangeApplied           bool                  `json:"state_change_applied"`
	CredentialValidAfter         bool                  `json:"credential_valid_after"`
	Result                       *channelTestResultDTO `json:"result"`
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

	if err := validateConcurrencyLimit(req.ConcurrencyLimit.Value); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	in := channel.CreateInput{
		ProviderID:          req.ProviderID,
		Name:                req.Name,
		Protocol:            req.Protocol,
		AdapterKey:          req.AdapterKey,
		Credential:          req.Credential,
		Status:              req.Status,
		Priority:            req.Priority,
		ResponseTimeoutMs:   req.ResponseTimeoutMs,
		FirstTokenTimeoutMs: req.FirstTokenTimeoutMs,
		StickyEnabled:       req.StickyEnabled,
		StickyTTLms:         req.StickyTTLms,
		ConcurrencyLimit:    req.ConcurrencyLimit.Value,
		SupportsOpenAIFast:  req.SupportsOpenAIFast,
	}
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

	if err := validateConcurrencyLimit(req.ConcurrencyLimit.Value); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	in := channel.UpdateInput{
		ID:                  id,
		Name:                req.Name,
		ProviderID:          req.ProviderID,
		Status:              req.Status,
		Priority:            req.Priority,
		ResponseTimeoutMs:   req.ResponseTimeoutMs,
		FirstTokenTimeoutMs: req.FirstTokenTimeoutMs,
		StickyEnabled:       req.StickyEnabled,
		StickyTTLms:         req.StickyTTLms,
		CapacityProvided:    req.ConcurrencyLimit.Set,
		ConcurrencyLimit:    req.ConcurrencyLimit.Value,
		SupportsOpenAIFast:  req.SupportsOpenAIFast,
		Confirmation: supply.Confirmation{
			Confirm:             req.ConfirmSupplyImpact,
			ExpectedFingerprint: req.ExpectedImpactFingerprint,
		},
	}
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

	result, err := h.service.RotateCredential(r.Context(), channel.RotateCredentialInput{
		ID:         id,
		Credential: req.Credential,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toRotateCredentialResultDTO(result))
}

func toRotateCredentialResultDTO(result channel.RotateCredentialResult) rotateCredentialResultDTO {
	dto := rotateCredentialResultDTO{
		CredentialSaved: result.CredentialSaved, CredentialChanged: result.CredentialChanged,
		SavedConfigRevision: result.SavedConfigRevision, CurrentConfigRevision: result.CurrentConfigRevision,
		Verification: credentialVerificationDTO{
			State:                        string(result.Verification.State),
			TestedOriginRevision:         result.Verification.TestedOriginRevision,
			TestedProviderStatusRevision: result.Verification.TestedProviderStatusRevision,
			TestedConfigRevision:         result.Verification.TestedConfigRevision,
			StateChangeApplied:           result.Verification.StateChangeApplied,
			CredentialValidAfter:         result.Verification.CredentialValidAfter,
		},
	}
	if probe := result.Verification.Result; probe != nil {
		test := channelTestResultDTO{
			Success: probe.Success, LatencyMs: probe.LatencyMs, TestedModel: probe.TestedModel,
			HTTPStatus: probe.HTTPStatus, Message: probe.Message, TestedAt: probe.TestedAt.UTC().Format(time.RFC3339),
		}
		if probe.ErrorCode != "" {
			code := probe.ErrorCode
			test.ErrorCode = &code
		}
		if probe.UpstreamError != "" {
			upstreamError := probe.UpstreamError
			test.UpstreamError = &upstreamError
		}
		dto.Verification.Result = &test
	}
	return dto
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
	if err := h.service.Archive(r.Context(), id); err != nil {
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
		ID:                  c.ID,
		ProviderID:          c.ProviderID,
		ProviderName:        c.ProviderName,
		ConfigRevision:      c.ConfigRevision,
		CapacityRevision:    c.CapacityRevision,
		RuntimeSyncPending:  c.RuntimeSyncPending,
		Name:                c.Name,
		Protocol:            c.Protocol,
		AdapterKey:          c.AdapterKey,
		Origin:              c.Origin,
		SupportsOpenAIFast:  c.SupportsOpenAIFast,
		Credential:          c.Credential,
		Status:              c.Status,
		Priority:            c.Priority,
		ResponseTimeoutMs:   c.ResponseTimeoutMs,
		FirstTokenTimeoutMs: c.FirstTokenTimeoutMs,
		StickyEnabled:       c.StickyEnabled,
		StickyTTLms:         c.StickyTTLms,
		ConcurrencyLimit:    c.ConcurrencyLimit,
		CreatedAt:           c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:           c.UpdatedAt.UTC().Format(time.RFC3339),
		ArchivedAt:          formatOptionalTime(c.ArchivedAt),

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
