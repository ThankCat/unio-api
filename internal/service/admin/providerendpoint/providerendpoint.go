// Package providerendpoint 编排 admin 管理端的 ProviderEndpoint 读写（P4 §4.2、§8.1）。
//
// ProviderEndpoint 是「一个 API Root = 一个上游公共故障域」，唯一持有规范化 base_url。本服务负责
// URL 规范化/唯一冲突、Provider 归属校验、以及创建时初始化可恢复的 Endpoint control（§4.2.18）。
// BaseURL/status 的热更新走独立的 revision fence（EndpointFencePublisher），不在本文件的 Create/List/Get/Rename。
package providerendpoint

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// errFencerNotConfigured 表示状态/地址围栏更新器未注入（生产必须注入；缺失即拒绝，不静默降级）。
var errFencerNotConfigured = errors.New("providerendpoint: endpoint fencer not configured")

const (
	StatusEnabled  = "enabled"
	StatusDisabled = "disabled"
	StatusArchived = "archived"
)

// Store 定义 ProviderEndpoint 管理所需的存储能力（provider_endpoint.sql 生成查询子集）。
type Store interface {
	GetProvider(ctx context.Context, id int64) (sqlc.Provider, error)
	CreateProviderEndpoint(ctx context.Context, arg sqlc.CreateProviderEndpointParams) (sqlc.ProviderEndpoint, error)
	GetProviderEndpoint(ctx context.Context, id int64) (sqlc.ProviderEndpoint, error)
	ListProviderEndpointsPage(ctx context.Context, arg sqlc.ListProviderEndpointsPageParams) ([]sqlc.ListProviderEndpointsPageRow, error)
	CountProviderEndpoints(ctx context.Context, arg sqlc.CountProviderEndpointsParams) (int64, error)
	UpdateProviderEndpointName(ctx context.Context, arg sqlc.UpdateProviderEndpointNameParams) (sqlc.ProviderEndpoint, error)
	CountChannelsByProviderEndpoint(ctx context.Context, providerEndpointID int64) (int64, error)
}

// ControlInitializer 在创建 Endpoint 后初始化可恢复的 Redis control（§4.2.18）。
type ControlInitializer interface {
	InitEndpointControl(ctx context.Context, endpointID, baseURLRevision, statusRevision int64, effectiveStatus string) (bool, error)
}

type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ProviderEndpoint 是 admin 视角的 Endpoint 业务事实。
type ProviderEndpoint struct {
	ID              int64
	ProviderID      int64
	ProviderName    string
	Name            string
	BaseURL         string
	BaseURLRevision int64
	Status          string
	StatusRevision  int64
	ChannelCount    int64
	// RuntimeSyncPending 表示 Endpoint 业务行已写入，但 Redis control 初始化未确认（fail-closed，
	// 该 Endpoint 在 control 恢复前不可被准入）。
	RuntimeSyncPending bool
	ArchivedAt         *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ListParams 分页/过滤入参。
type ListParams struct {
	ProviderID int64
	Status     string
	Query      string
	Limit      int32
	Offset     int32
}

// ListResult 分页结果。
type ListResult struct {
	Items []ProviderEndpoint
	Total int64
}

// CreateInput 创建 Endpoint 入参。
type CreateInput struct {
	ProviderID int64
	Name       string
	BaseURL    string
	Status     string
}

// Service 编排 ProviderEndpoint 管理。
type Service struct {
	store   Store
	control ControlInitializer
	fencer  *EndpointFencer
	db      txBeginner
}

// NewService 创建服务。control 可为 nil（仅单测 CRUD 校验时）；生产必须注入以初始化 control。
func NewService(store Store, control ControlInitializer) *Service {
	if store == nil {
		panic("providerendpoint: store is required")
	}
	return &Service{store: store, control: control}
}

// WithFencer 注入 status/base_url 可恢复围栏更新器（生产由 bootstrap 注入；nil 时状态/地址热更新返回未配置错误）。
func (s *Service) WithFencer(fencer *EndpointFencer) *Service {
	s.fencer = fencer
	return s
}

// WithTransactionalDB enables the production Provider -> Endpoint lock order for Endpoint creation.
func (s *Service) WithTransactionalDB(db txBeginner) *Service {
	s.db = db
	return s
}

// NormalizeBaseURL 规范化并校验上游 API Root（§4.2）：仅 http/https；禁止 userinfo/query/fragment；
// scheme/host 转小写、去默认端口、去尾斜杠；path 大小写保持原样。
func NormalizeBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", invalidArgument("base_url", "base_url is required")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", invalidArgument("base_url", "base_url is not a valid URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", invalidArgument("base_url", "base_url must use http or https")
	}
	if u.User != nil {
		return "", invalidArgument("base_url", "base_url must not contain userinfo")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", invalidArgument("base_url", "base_url must not contain a query")
	}
	if u.Fragment != "" {
		return "", invalidArgument("base_url", "base_url must not contain a fragment")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", invalidArgument("base_url", "base_url must include a host")
	}
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	hostPort := host
	if port != "" {
		hostPort = host + ":" + port
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	return scheme + "://" + hostPort + path, nil
}

// Create 创建 Endpoint：校验 Provider 存在，规范化 base_url，写入业务行，再初始化 Redis control。
func (s *Service) Create(ctx context.Context, in CreateInput) (ProviderEndpoint, error) {
	name := strings.TrimSpace(in.Name)
	status := strings.TrimSpace(in.Status)
	if in.ProviderID <= 0 {
		return ProviderEndpoint{}, invalidArgument("provider_id", "provider_id must be positive")
	}
	if name == "" {
		return ProviderEndpoint{}, invalidArgument("name", "name is required")
	}
	if err := validateStatus(status); err != nil {
		return ProviderEndpoint{}, err
	}
	baseURL, err := NormalizeBaseURL(in.BaseURL)
	if err != nil {
		return ProviderEndpoint{}, err
	}
	providerRow, err := s.store.GetProvider(ctx, in.ProviderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderEndpoint{}, invalidArgument("provider_id", "provider not found")
		}
		return ProviderEndpoint{}, storeFailed(err, "load provider for endpoint")
	}

	var row sqlc.ProviderEndpoint
	if s.db != nil {
		tx, beginErr := s.db.Begin(ctx)
		if beginErr != nil {
			return ProviderEndpoint{}, storeFailed(beginErr, "begin provider endpoint create")
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := tx.QueryRow(ctx, `SELECT status FROM providers WHERE id=$1 FOR UPDATE`, in.ProviderID).Scan(&providerRow.Status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ProviderEndpoint{}, invalidArgument("provider_id", "provider not found")
			}
			return ProviderEndpoint{}, storeFailed(err, "lock provider for endpoint create")
		}
		if providerRow.Status == StatusArchived {
			return ProviderEndpoint{}, conflict("cannot create an endpoint under an archived provider")
		}
		row, err = sqlc.New(tx).CreateProviderEndpoint(ctx, sqlc.CreateProviderEndpointParams{
			ProviderID: in.ProviderID, Name: name, BaseUrl: baseURL, Status: status,
		})
		if err == nil {
			err = tx.Commit(ctx)
		}
	} else {
		row, err = s.store.CreateProviderEndpoint(ctx, sqlc.CreateProviderEndpointParams{
			ProviderID: in.ProviderID, Name: name, BaseUrl: baseURL, Status: status,
		})
	}
	if err != nil {
		if isUniqueViolation(err) {
			return ProviderEndpoint{}, conflict("provider endpoint base_url or name already exists")
		}
		if isForeignKeyViolation(err) {
			return ProviderEndpoint{}, invalidArgument("provider_id", "provider not found")
		}
		return ProviderEndpoint{}, storeFailed(err, "create provider endpoint")
	}

	ep := toEndpoint(row)
	// §4.2.18：初始化可恢复 control；失败时业务行已存在但标记 runtime_sync_pending（fail-closed，直到 reconciler 恢复）。
	if s.control != nil {
		effectiveStatus := runtimecontrol.EffectiveEndpointStatus(providerRow.Status, row.Status)
		if _, err := s.control.InitEndpointControl(ctx, row.ID, row.BaseUrlRevision, row.StatusRevision, effectiveStatus); err != nil {
			ep.RuntimeSyncPending = true
		}
	}
	return s.enrichProviderName(ctx, ep)
}

// List 分页列出 Endpoint（连带 Provider 名与 Channel 数）。
func (s *Service) List(ctx context.Context, params ListParams) (ListResult, error) {
	rows, err := s.store.ListProviderEndpointsPage(ctx, sqlc.ListProviderEndpointsPageParams{
		ProviderID: int8Param(params.ProviderID),
		Status:     textParam(params.Status),
		Q:          textParam(params.Query),
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return ListResult{}, storeFailed(err, "list provider endpoints")
	}
	total, err := s.store.CountProviderEndpoints(ctx, sqlc.CountProviderEndpointsParams{
		ProviderID: int8Param(params.ProviderID),
		Status:     textParam(params.Status),
		Q:          textParam(params.Query),
	})
	if err != nil {
		return ListResult{}, storeFailed(err, "count provider endpoints")
	}
	items := make([]ProviderEndpoint, 0, len(rows))
	for _, row := range rows {
		items = append(items, toEndpointRow(row))
	}
	return ListResult{Items: items, Total: total}, nil
}

// Get 读取单个 Endpoint（附 Channel 数与 Provider 名）。
func (s *Service) Get(ctx context.Context, id int64) (ProviderEndpoint, error) {
	if id <= 0 {
		return ProviderEndpoint{}, invalidArgument("id", "id must be positive")
	}
	row, err := s.store.GetProviderEndpoint(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderEndpoint{}, notFound("provider endpoint not found")
		}
		return ProviderEndpoint{}, storeFailed(err, "get provider endpoint")
	}
	ep := toEndpoint(row)
	if cnt, err := s.store.CountChannelsByProviderEndpoint(ctx, id); err == nil {
		ep.ChannelCount = cnt
	}
	return s.enrichProviderName(ctx, ep)
}

// UpdateName 仅更新展示名（不触碰 base_url/status/revision）。
func (s *Service) UpdateName(ctx context.Context, id int64, name string) (ProviderEndpoint, error) {
	if id <= 0 {
		return ProviderEndpoint{}, invalidArgument("id", "id must be positive")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ProviderEndpoint{}, invalidArgument("name", "name is required")
	}
	row, err := s.store.UpdateProviderEndpointName(ctx, sqlc.UpdateProviderEndpointNameParams{ID: id, Name: name})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderEndpoint{}, notFound("provider endpoint not found")
		}
		if isUniqueViolation(err) {
			return ProviderEndpoint{}, conflict("provider endpoint name already exists for this provider")
		}
		return ProviderEndpoint{}, storeFailed(err, "update provider endpoint name")
	}
	return s.enrichProviderName(ctx, toEndpoint(row))
}

// UpdateStatus 通过 status revision 围栏热更新 Endpoint 有效状态（enabled/disabled/archived，§2.9）。
// 同值幂等（不推进 revision、不动运行态）；archived 需 archive 语义（清子 Channel breaker 由 commit fence 处理）。
func (s *Service) UpdateStatus(ctx context.Context, id int64, newStatus string) (ProviderEndpoint, error) {
	if id <= 0 {
		return ProviderEndpoint{}, invalidArgument("id", "id must be positive")
	}
	newStatus = strings.TrimSpace(newStatus)
	switch newStatus {
	case StatusEnabled, StatusDisabled, StatusArchived:
	default:
		return ProviderEndpoint{}, invalidArgument("status", "status must be enabled/disabled/archived")
	}
	cur, err := s.store.GetProviderEndpoint(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderEndpoint{}, notFound("provider endpoint not found")
		}
		return ProviderEndpoint{}, storeFailed(err, "get provider endpoint")
	}
	if cur.Status == newStatus {
		return s.Get(ctx, id) // 同值幂等：不推进 revision。
	}
	// archived → 需先无未归档 Channel 依赖（护栏）。
	if newStatus == StatusArchived {
		cnt, countErr := s.store.CountChannelsByProviderEndpoint(ctx, id)
		if countErr != nil {
			return ProviderEndpoint{}, storeFailed(countErr, "check endpoint channel dependencies")
		}
		if cnt > 0 {
			return ProviderEndpoint{}, conflict("endpoint still has active channels; archive or move them first")
		}
	}
	if s.fencer == nil {
		return ProviderEndpoint{}, storeFailed(errFencerNotConfigured, "status fence not configured")
	}
	providerRow, err := s.store.GetProvider(ctx, cur.ProviderID)
	if err != nil {
		return ProviderEndpoint{}, storeFailed(err, "load provider for endpoint status")
	}
	fact := endpointFenceFact{
		EndpointID: id, ProviderID: cur.ProviderID, ProviderStatus: providerRow.Status,
		BaseURL: cur.BaseUrl, BaseURLRevision: cur.BaseUrlRevision,
		Status: cur.Status, StatusRevision: cur.StatusRevision,
		EffectiveStatus:     runtimecontrol.EffectiveEndpointStatus(providerRow.Status, cur.Status),
		NextEffectiveStatus: runtimecontrol.EffectiveEndpointStatus(providerRow.Status, newStatus),
	}
	if fact.EffectiveStatus == fact.NextEffectiveStatus {
		if err := s.fencer.updateStatusWithoutRevision(ctx, fact, newStatus); err != nil {
			return ProviderEndpoint{}, err
		}
		return s.Get(ctx, id)
	}
	res, err := s.fencer.updateStatus(ctx, fact, newStatus)
	if err != nil {
		return ProviderEndpoint{}, err
	}
	ep, gerr := s.Get(ctx, id)
	if gerr != nil {
		return ProviderEndpoint{}, gerr
	}
	ep.RuntimeSyncPending = res.State == runtimecontrol.PublishRuntimeSyncPending
	return ep, nil
}

// UpdateBaseURL 通过 base_url revision 围栏热更新 Endpoint 规范化地址（§2.9/§4.2）。同值幂等。
func (s *Service) UpdateBaseURL(ctx context.Context, id int64, rawBaseURL string) (ProviderEndpoint, error) {
	if id <= 0 {
		return ProviderEndpoint{}, invalidArgument("id", "id must be positive")
	}
	baseURL, err := NormalizeBaseURL(rawBaseURL)
	if err != nil {
		return ProviderEndpoint{}, err
	}
	cur, err := s.store.GetProviderEndpoint(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderEndpoint{}, notFound("provider endpoint not found")
		}
		return ProviderEndpoint{}, storeFailed(err, "get provider endpoint")
	}
	if cur.BaseUrl == baseURL {
		return s.Get(ctx, id) // 同值幂等：不推进 revision。
	}
	if s.fencer == nil {
		return ProviderEndpoint{}, storeFailed(errFencerNotConfigured, "base_url fence not configured")
	}
	providerRow, err := s.store.GetProvider(ctx, cur.ProviderID)
	if err != nil {
		return ProviderEndpoint{}, storeFailed(err, "load provider for endpoint BaseURL")
	}
	effective := runtimecontrol.EffectiveEndpointStatus(providerRow.Status, cur.Status)
	fact := endpointFenceFact{
		EndpointID: id, ProviderID: cur.ProviderID, ProviderStatus: providerRow.Status,
		BaseURL: cur.BaseUrl, BaseURLRevision: cur.BaseUrlRevision,
		Status: cur.Status, StatusRevision: cur.StatusRevision,
		EffectiveStatus: effective, NextEffectiveStatus: effective,
	}
	res, err := s.fencer.updateBaseURL(ctx, fact, baseURL)
	if err != nil {
		return ProviderEndpoint{}, err
	}
	ep, gerr := s.Get(ctx, id)
	if gerr != nil {
		return ProviderEndpoint{}, gerr
	}
	ep.RuntimeSyncPending = res.State == runtimecontrol.PublishRuntimeSyncPending
	return ep, nil
}

// UpdateRouting atomically changes BaseURL and effective status through one combined Redis fence.
// Callers that change only one field are delegated to the corresponding singular path.
func (s *Service) UpdateRouting(ctx context.Context, id int64, rawBaseURL, newStatus string) (ProviderEndpoint, error) {
	if id <= 0 {
		return ProviderEndpoint{}, invalidArgument("id", "id must be positive")
	}
	baseURL, err := NormalizeBaseURL(rawBaseURL)
	if err != nil {
		return ProviderEndpoint{}, err
	}
	newStatus = strings.TrimSpace(newStatus)
	if newStatus != StatusEnabled && newStatus != StatusDisabled && newStatus != StatusArchived {
		return ProviderEndpoint{}, invalidArgument("status", "status must be enabled/disabled/archived")
	}
	cur, err := s.store.GetProviderEndpoint(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderEndpoint{}, notFound("provider endpoint not found")
		}
		return ProviderEndpoint{}, storeFailed(err, "get provider endpoint")
	}
	if cur.BaseUrl == baseURL && cur.Status == newStatus {
		return s.Get(ctx, id)
	}
	if cur.BaseUrl == baseURL {
		return s.UpdateStatus(ctx, id, newStatus)
	}
	if cur.Status == newStatus {
		return s.UpdateBaseURL(ctx, id, baseURL)
	}
	if newStatus == StatusArchived {
		count, countErr := s.store.CountChannelsByProviderEndpoint(ctx, id)
		if countErr != nil {
			return ProviderEndpoint{}, storeFailed(countErr, "check endpoint channel dependencies")
		}
		if count > 0 {
			return ProviderEndpoint{}, conflict("endpoint still has active channels; archive or move them first")
		}
	}
	if s.fencer == nil {
		return ProviderEndpoint{}, storeFailed(errFencerNotConfigured, "combined endpoint fence not configured")
	}
	providerRow, err := s.store.GetProvider(ctx, cur.ProviderID)
	if err != nil {
		return ProviderEndpoint{}, storeFailed(err, "load provider for endpoint routing update")
	}
	fact := endpointFenceFact{
		EndpointID: id, ProviderID: cur.ProviderID, ProviderStatus: providerRow.Status,
		BaseURL: cur.BaseUrl, BaseURLRevision: cur.BaseUrlRevision,
		Status: cur.Status, StatusRevision: cur.StatusRevision,
		EffectiveStatus:     runtimecontrol.EffectiveEndpointStatus(providerRow.Status, cur.Status),
		NextEffectiveStatus: runtimecontrol.EffectiveEndpointStatus(providerRow.Status, newStatus),
	}
	if fact.EffectiveStatus == fact.NextEffectiveStatus {
		return ProviderEndpoint{}, conflict("combined BaseURL/status update does not change effective status; update the fields separately")
	}
	result, err := s.fencer.updateRouting(ctx, fact, baseURL, newStatus)
	if err != nil {
		return ProviderEndpoint{}, err
	}
	ep, err := s.Get(ctx, id)
	if err != nil {
		return ProviderEndpoint{}, err
	}
	ep.RuntimeSyncPending = result.State == runtimecontrol.PublishRuntimeSyncPending
	return ep, nil
}

func (s *Service) enrichProviderName(ctx context.Context, ep ProviderEndpoint) (ProviderEndpoint, error) {
	if ep.ProviderID <= 0 || ep.ProviderName != "" {
		return ep, nil
	}
	p, err := s.store.GetProvider(ctx, ep.ProviderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ep, nil
		}
		return ProviderEndpoint{}, storeFailed(err, "load provider for endpoint")
	}
	ep.ProviderName = p.Name
	return ep, nil
}

func toEndpoint(r sqlc.ProviderEndpoint) ProviderEndpoint {
	return ProviderEndpoint{
		ID:              r.ID,
		ProviderID:      r.ProviderID,
		Name:            r.Name,
		BaseURL:         r.BaseUrl,
		BaseURLRevision: r.BaseUrlRevision,
		Status:          r.Status,
		StatusRevision:  r.StatusRevision,
		ArchivedAt:      timestampResult(r.ArchivedAt),
		CreatedAt:       r.CreatedAt.Time,
		UpdatedAt:       r.UpdatedAt.Time,
	}
}

func toEndpointRow(r sqlc.ListProviderEndpointsPageRow) ProviderEndpoint {
	return ProviderEndpoint{
		ID:              r.ID,
		ProviderID:      r.ProviderID,
		ProviderName:    r.ProviderName,
		Name:            r.Name,
		BaseURL:         r.BaseUrl,
		BaseURLRevision: r.BaseUrlRevision,
		Status:          r.Status,
		StatusRevision:  r.StatusRevision,
		ChannelCount:    r.ChannelCount,
		ArchivedAt:      timestampResult(r.ArchivedAt),
		CreatedAt:       r.CreatedAt.Time,
		UpdatedAt:       r.UpdatedAt.Time,
	}
}

func validateStatus(status string) error {
	switch status {
	case StatusEnabled, StatusDisabled:
		return nil
	default:
		return invalidArgument("status", "status must be enabled or disabled")
	}
}

func timestampResult(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	out := t.Time
	return &out
}

func textParam(s string) pgtype.Text {
	if strings.TrimSpace(s) == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func int8Param(id int64) pgtype.Int8 {
	if id <= 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: id, Valid: true}
}

func invalidArgument(field, msg string) error {
	return failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage(msg), failure.WithField("field", field))
}

func notFound(msg string) error {
	return failure.New(failure.CodeAdminNotFound, failure.WithMessage(msg))
}

func conflict(msg string) error {
	return failure.New(failure.CodeAdminConflict, failure.WithMessage(msg))
}

func storeFailed(err error, msg string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage(msg))
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
