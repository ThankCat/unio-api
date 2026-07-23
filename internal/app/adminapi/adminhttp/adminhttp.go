// Package adminhttp 汇总 admin HTTP 各模块子包共用的响应/请求/分页/排序小工具与共享 DTO。
//
// 它是叶子包（只依赖 platform 与少量 service 只读类型），供 adminapi 各模块子包
// （overview/provider/channel/... ）导入，避免各模块把这些样板重复实现，也避免
// 模块子包与根 adminapi 包互相导入形成环。
package adminhttp

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ThankCat/unio-gateway/internal/core/adminauth"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/platform/listquery"
	"github.com/ThankCat/unio-gateway/internal/service/admin/dashboard"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
)

type routingMarginMetrics interface {
	IncRoutingMarginGuard(result string)
}

type routingMarginMetricsHolder struct {
	recorder routingMarginMetrics
}

var routingMarginRecorder atomic.Pointer[routingMarginMetricsHolder]

// SetRoutingMarginMetrics wires configuration-rejection metrics into the shared error mapper.
func SetRoutingMarginMetrics(recorder routingMarginMetrics) {
	if recorder == nil {
		routingMarginRecorder.Store(nil)
		return
	}
	routingMarginRecorder.Store(&routingMarginMetricsHolder{recorder: recorder})
}

// ---- 响应信封 / 错误映射 ----

// WriteData 写出统一成功信封 { "data": ... }。
func WriteData(w http.ResponseWriter, status int, data any) {
	_ = httpx.WriteJSON(w, status, map[string]any{"data": data})
}

// WriteServiceError 把 service / 解码层的内部 failure 映射为安全的 admin 错误响应。
//
// 只回显 4xx 的安全摘要（failure.Error() 不含 cause 细节），5xx 一律返回通用文案，
// 不向客户端透传内部实现或上游原始信息。
func WriteServiceError(w http.ResponseWriter, err error) {
	code := failure.CodeOf(err)
	messageOverride := ""
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "ck_non_negative_route_margin" {
		code = failure.CodeAdminNegativeMargin
		messageOverride = pgErr.Message
		if holder := routingMarginRecorder.Load(); holder != nil {
			holder.recorder.IncRoutingMarginGuard("configuration_rejected")
		}
	}
	status := adminErrorStatus(code)

	codeStr := string(code)
	if codeStr == "" {
		codeStr = "internal_error"
	}

	message := "internal error"
	if status != http.StatusInternalServerError {
		if messageOverride != "" {
			message = messageOverride
		} else if m := err.Error(); m != "" {
			message = m
		}
	}

	_ = httpx.WriteError(w, status, codeStr, message)
}

// adminErrorStatus 把内部错误码映射为 HTTP 状态码。
func adminErrorStatus(code failure.Code) int {
	switch code {
	case failure.CodeAdminInvalidArgument:
		return http.StatusBadRequest
	case failure.CodeAdminAdapterBindingUnsupported:
		return http.StatusUnprocessableEntity
	case failure.CodeAdminPricingWindowOverlap:
		return http.StatusUnprocessableEntity
	case failure.CodeAdminNegativeMargin:
		return http.StatusUnprocessableEntity
	case failure.CodeAdminNotFound:
		return http.StatusNotFound
	case failure.CodeAdminConflict:
		return http.StatusConflict
	// M7 手工调额经由 ledger：把账本业务错误映射成可读的 4xx，而非笼统 500。
	case failure.CodeLedgerInvalidAmount:
		return http.StatusBadRequest
	case failure.CodeLedgerInsufficientBalance:
		return http.StatusUnprocessableEntity
	case failure.CodeLedgerIdempotencyConflict:
		return http.StatusConflict
	// M5 能力管理：core/capability 写入校验错误映射成可读的 4xx。
	case failure.CodeCapabilityInvalidKey,
		failure.CodeCapabilityInvalidSupportLevel,
		failure.CodeCapabilityInvalidSource:
		return http.StatusBadRequest
	case failure.CodeCapabilityNotFound:
		return http.StatusNotFound
	// P4 §8.4：Redis/BreakerStore 基础设施故障 → 503（准入/运行态数据源不可用），与普通 500 区分。
	case failure.CodeGatewayBreakerStoreUnavailable, failure.CodeDependencyRedisUnavailable:
		return http.StatusServiceUnavailable
	}

	if code.Category() == failure.CategoryHTTP {
		return http.StatusBadRequest
	}

	return http.StatusInternalServerError
}

// InvalidRequestField 构造一个针对某字段的 admin_invalid_argument（映射为 400）。
func InvalidRequestField(field, message string) error {
	return failure.New(
		failure.CodeAdminInvalidArgument,
		failure.WithMessage(message),
		failure.WithField("field", field),
	)
}

// PathID 解析路径参数 {id}，非法或非正整数时返回 admin_invalid_argument。
func PathID(r *http.Request) (int64, error) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("id path parameter must be a positive integer"),
			failure.WithField("field", "id"),
		)
	}
	return id, nil
}

// ---- 分页 ----

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// PageParams 是解析后的分页请求（page 从 1 起）。
type PageParams struct {
	Page     int
	PageSize int
}

// Limit / Offset 换算成 SQL LIMIT/OFFSET（int32 与 sqlc 生成参数对齐）。
func (p PageParams) Limit() int32  { return int32(p.PageSize) }
func (p PageParams) Offset() int32 { return int32((p.Page - 1) * p.PageSize) }

// ParsePage 从 query 解析 page/page_size；非法或缺省回退默认值，并夹紧上限。
func ParsePage(r *http.Request) PageParams {
	page := 1
	pageSize := defaultPageSize
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n > 0 {
		page = n
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("page_size")); err == nil && n > 0 {
		pageSize = n
		if pageSize > maxPageSize {
			pageSize = maxPageSize
		}
	}
	return PageParams{Page: page, PageSize: pageSize}
}

// ParseBoolQuery 解析布尔型 query 参数；仅 "true"/"1" 视为真，其余（含缺省/非法）为假。
func ParseBoolQuery(r *http.Request, key string) bool {
	switch r.URL.Query().Get(key) {
	case "true", "1":
		return true
	default:
		return false
	}
}

// ListStatus 只接受 enabled/disabled/archived 作为状态过滤值，其它一律视为不过滤。
// archived 是 providers/channels/routes 的归档第三态；缺了它会导致前端选「已归档」
// 时被当成不过滤（返回全部），即归档筛选失效。
func ListStatus(r *http.Request) string {
	switch r.URL.Query().Get("status") {
	case "enabled":
		return "enabled"
	case "disabled":
		return "disabled"
	case "archived":
		return "archived"
	default:
		return ""
	}
}

// WriteList 写出带分页 meta 的成功信封 { "data": [...], "meta": {...} }。
func WriteList(w http.ResponseWriter, status int, data any, p PageParams, total int64) {
	_ = httpx.WriteJSON(w, status, map[string]any{
		"data": data,
		"meta": map[string]any{
			"page":      p.Page,
			"page_size": p.PageSize,
			"total":     total,
		},
	})
}

// ---- 排序 ----

// ParseListSort 解析 ?sort=field|-field；非法字段写 400。
func ParseListSort(r *http.Request, allowed map[string]struct{}, defaultField string, defaultDesc bool) (listquery.Sort, error) {
	return listquery.ParseSort(r, allowed, defaultField, defaultDesc)
}

// WriteSortError 写出排序参数校验失败的 400。
func WriteSortError(w http.ResponseWriter, err error) {
	_ = httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{
		"error": map[string]any{
			"code":    "invalid_argument",
			"message": err.Error(),
		},
	})
}

// ---- query 解析小工具 ----

// OptionalInt64Query 解析可选的正整数 query 过滤项；缺省返回 nil（不过滤），非法返回 admin_invalid_argument。
func OptionalInt64Query(r *http.Request, key string) (*int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return nil, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage(key+" must be a positive integer"),
			failure.WithField("field", key),
		)
	}
	return &v, nil
}

// OptionalTimeQuery 解析可选的 RFC3339 时间过滤项；缺省返回 nil（不过滤），非法返回 admin_invalid_argument。
func OptionalTimeQuery(r *http.Request, key string) (*time.Time, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage(key+" must be an RFC3339 timestamp"),
			failure.WithField("field", key),
		)
	}
	return &t, nil
}

// BoolQuery 解析布尔开关 query：1/true/yes 为 true，其余（含缺省）为 false。
func BoolQuery(r *http.Request, key string) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// QueryString 取 trim 后的 query 值。
func QueryString(r *http.Request, key string) string {
	return strings.TrimSpace(r.URL.Query().Get(key))
}

// RFC3339 把时间格式化为 UTC RFC3339 字符串。
func RFC3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// RFC3339Ptr 把可空时间格式化为 *string（UTC RFC3339）；nil → nil。
func RFC3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// ---- 运维时间窗 / 延迟画像 DTO（各 *_ops 模块共用）----

// RangeWindow 解析运维看板的时间窗：range=all 表示全量（from 零值）；
// 否则用 from/to（RFC3339，半开区间，缺省近 24h）。interval 由跨度推导
// （≤ 1 小时 → minute；≤ 8 天 → hour；否则 day），可被 ?interval= 覆盖。
func RangeWindow(r *http.Request) (from, to time.Time, interval string, err error) {
	to = time.Now()
	if QueryString(r, "range") == "all" {
		return time.Time{}, to, dashboard.IntervalDay, nil
	}

	fromPtr, err := OptionalTimeQuery(r, "from")
	if err != nil {
		return time.Time{}, time.Time{}, "", err
	}
	toPtr, err := OptionalTimeQuery(r, "to")
	if err != nil {
		return time.Time{}, time.Time{}, "", err
	}
	if toPtr != nil {
		to = *toPtr
	}
	from = to.Add(-24 * time.Hour)
	if fromPtr != nil {
		from = *fromPtr
	}

	switch span := to.Sub(from); {
	case span <= time.Hour:
		interval = dashboard.IntervalMinute
	case span > 8*24*time.Hour:
		interval = dashboard.IntervalDay
	default:
		interval = dashboard.IntervalHour
	}
	return from, to, interval, nil
}

// LatencyStatsDTO 是 attempt 粒度延迟分位画像响应体（各 *_ops 模块共用）。
type LatencyStatsDTO struct {
	Avg      float64 `json:"avg"`
	P50      float64 `json:"p50"`
	P90      float64 `json:"p90"`
	P95      float64 `json:"p95"`
	P99      float64 `json:"p99"`
	Sample   int64   `json:"sample"`
	Coverage float64 `json:"coverage"`
}

// LatencyStatsFrom 从 service 层延迟画像组装响应 DTO。
func LatencyStatsFrom(s opsutil.LatencyStats) LatencyStatsDTO {
	return LatencyStatsDTO{
		Avg:      s.Avg,
		P50:      s.P50,
		P90:      s.P90,
		P95:      s.P95,
		P99:      s.P99,
		Sample:   s.Sample,
		Coverage: s.Coverage,
	}
}

// ---- 审计身份 ----

// AdminActor 从 admin 认证身份取调用者标识，用于写入审计的 updated_by；缺失回退空串。
func AdminActor(r *http.Request) string {
	if principal, ok := adminauth.PrincipalFromContext(r.Context()); ok && principal != nil {
		return principal.Subject
	}
	return ""
}

// ---- RFC3339 时间解析（生效窗口/过期时间等写入参数共用）----

// ParseRFC3339 解析必填 RFC3339 时间，非法时返回 admin_invalid_argument。
func ParseRFC3339(field, raw string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage(field+" must be an RFC3339 timestamp"),
			failure.WithField("field", field),
		)
	}
	return t, nil
}

// ParseOptionalRFC3339 解析可选 RFC3339 时间：nil/空串 → nil。
func ParseOptionalRFC3339(field string, raw *string) (*time.Time, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	t, err := ParseRFC3339(field, *raw)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
