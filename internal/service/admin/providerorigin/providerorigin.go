// Package providerorigin 编排 admin 管理端的 ProviderOrigin 读写（P4 §4.2、§8.1）。
//
// ProviderOrigin 是「一个 API Root = 一个上游公共故障域」，唯一持有规范化 base_url。本服务负责
// URL 规范化/唯一冲突、Provider 归属校验、以及创建时初始化可恢复的 Origin control（§4.2.18）。
// BaseURL/status 的热更新走独立的 revision fence（OriginFencePublisher），不在本文件的 Create/List/Get/Rename。
package providerorigin

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
var errFencerNotConfigured = errors.New("providerorigin: origin fencer not configured")

const (
	StatusEnabled  = "enabled"
	StatusDisabled = "disabled"
	StatusArchived = "archived"
)

// Store 定义 ProviderOrigin 管理所需的存储能力（provider_origin.sql 生成查询子集）。
type Store interface {
	GetProvider(ctx context.Context, id int64) (sqlc.Provider, error)
	CreateProviderOrigin(ctx context.Context, arg sqlc.CreateProviderOriginParams) (sqlc.ProviderOrigin, error)
	GetProviderOrigin(ctx context.Context, id int64) (sqlc.ProviderOrigin, error)
	ListProviderOriginsPage(ctx context.Context, arg sqlc.ListProviderOriginsPageParams) ([]sqlc.ListProviderOriginsPageRow, error)
	CountProviderOrigins(ctx context.Context, arg sqlc.CountProviderOriginsParams) (int64, error)
	UpdateProviderOriginName(ctx context.Context, arg sqlc.UpdateProviderOriginNameParams) (sqlc.ProviderOrigin, error)
	CountChannelsByProviderOrigin(ctx context.Context, providerOriginID int64) (int64, error)
}

// ControlInitializer 在创建 Origin 后初始化可恢复的 Redis control（§4.2.18）。
type ControlInitializer interface {
	InitOriginControl(ctx context.Context, originID, baseURLRevision, statusRevision int64, effectiveStatus string) (bool, error)
}

type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ProviderOrigin 是 admin 视角的 Origin 业务事实。
type ProviderOrigin struct {
	ID              int64
	ProviderID      int64
	ProviderName    string
	Name            string
	BaseURL         string
	BaseURLRevision int64
	Status          string
	StatusRevision  int64
	ChannelCount    int64
	// RuntimeSyncPending 表示 Origin 业务行已写入，但 Redis control 初始化未确认（fail-closed，
	// 该 Origin 在 control 恢复前不可被准入）。
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
	Items []ProviderOrigin
	Total int64
}

// CreateInput 创建 Origin 入参。
type CreateInput struct {
	ProviderID int64
	Name       string
	BaseURL    string
	Status     string
}

// Service 编排 ProviderOrigin 管理。
type Service struct {
	store   Store
	control ControlInitializer
	fencer  *OriginFencer
	db      txBeginner
}

// NewService 创建服务。control 可为 nil（仅单测 CRUD 校验时）；生产必须注入以初始化 control。
func NewService(store Store, control ControlInitializer) *Service {
	if store == nil {
		panic("providerorigin: store is required")
	}
	return &Service{store: store, control: control}
}

// WithFencer 注入 status/base_url 可恢复围栏更新器（生产由 bootstrap 注入；nil 时状态/地址热更新返回未配置错误）。
func (s *Service) WithFencer(fencer *OriginFencer) *Service {
	s.fencer = fencer
	return s
}

// WithTransactionalDB enables the production Provider -> Origin lock order for Origin creation.
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

// Create 创建 Origin：校验 Provider 存在，规范化 base_url，写入业务行，再初始化 Redis control。
func (s *Service) Create(ctx context.Context, in CreateInput) (ProviderOrigin, error) {
	name := strings.TrimSpace(in.Name)
	status := strings.TrimSpace(in.Status)
	if in.ProviderID <= 0 {
		return ProviderOrigin{}, invalidArgument("provider_id", "provider_id must be positive")
	}
	if name == "" {
		return ProviderOrigin{}, invalidArgument("name", "name is required")
	}
	if err := validateStatus(status); err != nil {
		return ProviderOrigin{}, err
	}
	baseURL, err := NormalizeBaseURL(in.BaseURL)
	if err != nil {
		return ProviderOrigin{}, err
	}
	providerRow, err := s.store.GetProvider(ctx, in.ProviderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderOrigin{}, invalidArgument("provider_id", "provider not found")
		}
		return ProviderOrigin{}, storeFailed(err, "load provider for origin")
	}

	var row sqlc.ProviderOrigin
	if s.db != nil {
		tx, beginErr := s.db.Begin(ctx)
		if beginErr != nil {
			return ProviderOrigin{}, storeFailed(beginErr, "begin provider origin create")
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := tx.QueryRow(ctx, `SELECT status FROM providers WHERE id=$1 FOR UPDATE`, in.ProviderID).Scan(&providerRow.Status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ProviderOrigin{}, invalidArgument("provider_id", "provider not found")
			}
			return ProviderOrigin{}, storeFailed(err, "lock provider for origin create")
		}
		if providerRow.Status == StatusArchived {
			return ProviderOrigin{}, conflict("cannot create an origin under an archived provider")
		}
		row, err = sqlc.New(tx).CreateProviderOrigin(ctx, sqlc.CreateProviderOriginParams{
			ProviderID: in.ProviderID, Name: name, BaseUrl: baseURL, Status: status,
		})
		if err == nil {
			err = tx.Commit(ctx)
		}
	} else {
		row, err = s.store.CreateProviderOrigin(ctx, sqlc.CreateProviderOriginParams{
			ProviderID: in.ProviderID, Name: name, BaseUrl: baseURL, Status: status,
		})
	}
	if err != nil {
		if isUniqueViolation(err) {
			return ProviderOrigin{}, conflict("provider origin base_url or name already exists")
		}
		if isForeignKeyViolation(err) {
			return ProviderOrigin{}, invalidArgument("provider_id", "provider not found")
		}
		return ProviderOrigin{}, storeFailed(err, "create provider origin")
	}

	ep := toOrigin(row)
	// §4.2.18：初始化可恢复 control；失败时业务行已存在但标记 runtime_sync_pending（fail-closed，直到 reconciler 恢复）。
	if s.control != nil {
		effectiveStatus := runtimecontrol.EffectiveOriginStatus(providerRow.Status, row.Status)
		if _, err := s.control.InitOriginControl(ctx, row.ID, row.BaseUrlRevision, row.StatusRevision, effectiveStatus); err != nil {
			ep.RuntimeSyncPending = true
		}
	}
	return s.enrichProviderName(ctx, ep)
}

// List 分页列出 Origin（连带 Provider 名与 Channel 数）。
func (s *Service) List(ctx context.Context, params ListParams) (ListResult, error) {
	rows, err := s.store.ListProviderOriginsPage(ctx, sqlc.ListProviderOriginsPageParams{
		ProviderID: int8Param(params.ProviderID),
		Status:     textParam(params.Status),
		Q:          textParam(params.Query),
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return ListResult{}, storeFailed(err, "list provider origins")
	}
	total, err := s.store.CountProviderOrigins(ctx, sqlc.CountProviderOriginsParams{
		ProviderID: int8Param(params.ProviderID),
		Status:     textParam(params.Status),
		Q:          textParam(params.Query),
	})
	if err != nil {
		return ListResult{}, storeFailed(err, "count provider origins")
	}
	items := make([]ProviderOrigin, 0, len(rows))
	for _, row := range rows {
		items = append(items, toOriginRow(row))
	}
	return ListResult{Items: items, Total: total}, nil
}

// Get 读取单个 Origin（附 Channel 数与 Provider 名）。
func (s *Service) Get(ctx context.Context, id int64) (ProviderOrigin, error) {
	if id <= 0 {
		return ProviderOrigin{}, invalidArgument("id", "id must be positive")
	}
	row, err := s.store.GetProviderOrigin(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderOrigin{}, notFound("provider origin not found")
		}
		return ProviderOrigin{}, storeFailed(err, "get provider origin")
	}
	ep := toOrigin(row)
	if cnt, err := s.store.CountChannelsByProviderOrigin(ctx, id); err == nil {
		ep.ChannelCount = cnt
	}
	return s.enrichProviderName(ctx, ep)
}

// UpdateName 仅更新展示名（不触碰 base_url/status/revision）。
func (s *Service) UpdateName(ctx context.Context, id int64, name string) (ProviderOrigin, error) {
	if id <= 0 {
		return ProviderOrigin{}, invalidArgument("id", "id must be positive")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ProviderOrigin{}, invalidArgument("name", "name is required")
	}
	row, err := s.store.UpdateProviderOriginName(ctx, sqlc.UpdateProviderOriginNameParams{ID: id, Name: name})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderOrigin{}, notFound("provider origin not found")
		}
		if isUniqueViolation(err) {
			return ProviderOrigin{}, conflict("provider origin name already exists for this provider")
		}
		return ProviderOrigin{}, storeFailed(err, "update provider origin name")
	}
	return s.enrichProviderName(ctx, toOrigin(row))
}

// UpdateStatus 通过 status revision 围栏热更新 Origin 有效状态（enabled/disabled/archived，§2.9）。
// 同值幂等（不推进 revision、不动运行态）；archived 需 archive 语义（清子 Channel breaker 由 commit fence 处理）。
func (s *Service) UpdateStatus(ctx context.Context, id int64, newStatus string) (ProviderOrigin, error) {
	if id <= 0 {
		return ProviderOrigin{}, invalidArgument("id", "id must be positive")
	}
	newStatus = strings.TrimSpace(newStatus)
	switch newStatus {
	case StatusEnabled, StatusDisabled, StatusArchived:
	default:
		return ProviderOrigin{}, invalidArgument("status", "status must be enabled/disabled/archived")
	}
	cur, err := s.store.GetProviderOrigin(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderOrigin{}, notFound("provider origin not found")
		}
		return ProviderOrigin{}, storeFailed(err, "get provider origin")
	}
	if cur.Status == newStatus {
		return s.Get(ctx, id) // 同值幂等：不推进 revision。
	}
	// archived → 需先无未归档 Channel 依赖（护栏）。
	if newStatus == StatusArchived {
		cnt, countErr := s.store.CountChannelsByProviderOrigin(ctx, id)
		if countErr != nil {
			return ProviderOrigin{}, storeFailed(countErr, "check origin channel dependencies")
		}
		if cnt > 0 {
			return ProviderOrigin{}, conflict("origin still has active channels; archive or move them first")
		}
	}
	if s.fencer == nil {
		return ProviderOrigin{}, storeFailed(errFencerNotConfigured, "status fence not configured")
	}
	providerRow, err := s.store.GetProvider(ctx, cur.ProviderID)
	if err != nil {
		return ProviderOrigin{}, storeFailed(err, "load provider for origin status")
	}
	fact := originFenceFact{
		OriginID: id, ProviderID: cur.ProviderID, ProviderStatus: providerRow.Status,
		BaseURL: cur.BaseUrl, BaseURLRevision: cur.BaseUrlRevision,
		Status: cur.Status, StatusRevision: cur.StatusRevision,
		EffectiveStatus:     runtimecontrol.EffectiveOriginStatus(providerRow.Status, cur.Status),
		NextEffectiveStatus: runtimecontrol.EffectiveOriginStatus(providerRow.Status, newStatus),
	}
	if fact.EffectiveStatus == fact.NextEffectiveStatus {
		if err := s.fencer.updateStatusWithoutRevision(ctx, fact, newStatus); err != nil {
			return ProviderOrigin{}, err
		}
		return s.Get(ctx, id)
	}
	res, err := s.fencer.updateStatus(ctx, fact, newStatus)
	if err != nil {
		return ProviderOrigin{}, err
	}
	ep, gerr := s.Get(ctx, id)
	if gerr != nil {
		return ProviderOrigin{}, gerr
	}
	ep.RuntimeSyncPending = res.State == runtimecontrol.PublishRuntimeSyncPending
	return ep, nil
}

// UpdateBaseURL 通过 base_url revision 围栏热更新 Origin 规范化地址（§2.9/§4.2）。同值幂等。
func (s *Service) UpdateBaseURL(ctx context.Context, id int64, rawBaseURL string) (ProviderOrigin, error) {
	if id <= 0 {
		return ProviderOrigin{}, invalidArgument("id", "id must be positive")
	}
	baseURL, err := NormalizeBaseURL(rawBaseURL)
	if err != nil {
		return ProviderOrigin{}, err
	}
	cur, err := s.store.GetProviderOrigin(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderOrigin{}, notFound("provider origin not found")
		}
		return ProviderOrigin{}, storeFailed(err, "get provider origin")
	}
	if cur.BaseUrl == baseURL {
		return s.Get(ctx, id) // 同值幂等：不推进 revision。
	}
	if s.fencer == nil {
		return ProviderOrigin{}, storeFailed(errFencerNotConfigured, "base_url fence not configured")
	}
	providerRow, err := s.store.GetProvider(ctx, cur.ProviderID)
	if err != nil {
		return ProviderOrigin{}, storeFailed(err, "load provider for origin BaseURL")
	}
	effective := runtimecontrol.EffectiveOriginStatus(providerRow.Status, cur.Status)
	fact := originFenceFact{
		OriginID: id, ProviderID: cur.ProviderID, ProviderStatus: providerRow.Status,
		BaseURL: cur.BaseUrl, BaseURLRevision: cur.BaseUrlRevision,
		Status: cur.Status, StatusRevision: cur.StatusRevision,
		EffectiveStatus: effective, NextEffectiveStatus: effective,
	}
	res, err := s.fencer.updateBaseURL(ctx, fact, baseURL)
	if err != nil {
		return ProviderOrigin{}, err
	}
	ep, gerr := s.Get(ctx, id)
	if gerr != nil {
		return ProviderOrigin{}, gerr
	}
	ep.RuntimeSyncPending = res.State == runtimecontrol.PublishRuntimeSyncPending
	return ep, nil
}

// UpdateRouting atomically changes BaseURL and effective status through one combined Redis fence.
// Callers that change only one field are delegated to the corresponding singular path.
func (s *Service) UpdateRouting(ctx context.Context, id int64, rawBaseURL, newStatus string) (ProviderOrigin, error) {
	if id <= 0 {
		return ProviderOrigin{}, invalidArgument("id", "id must be positive")
	}
	baseURL, err := NormalizeBaseURL(rawBaseURL)
	if err != nil {
		return ProviderOrigin{}, err
	}
	newStatus = strings.TrimSpace(newStatus)
	if newStatus != StatusEnabled && newStatus != StatusDisabled && newStatus != StatusArchived {
		return ProviderOrigin{}, invalidArgument("status", "status must be enabled/disabled/archived")
	}
	cur, err := s.store.GetProviderOrigin(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderOrigin{}, notFound("provider origin not found")
		}
		return ProviderOrigin{}, storeFailed(err, "get provider origin")
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
		count, countErr := s.store.CountChannelsByProviderOrigin(ctx, id)
		if countErr != nil {
			return ProviderOrigin{}, storeFailed(countErr, "check origin channel dependencies")
		}
		if count > 0 {
			return ProviderOrigin{}, conflict("origin still has active channels; archive or move them first")
		}
	}
	if s.fencer == nil {
		return ProviderOrigin{}, storeFailed(errFencerNotConfigured, "combined origin fence not configured")
	}
	providerRow, err := s.store.GetProvider(ctx, cur.ProviderID)
	if err != nil {
		return ProviderOrigin{}, storeFailed(err, "load provider for origin routing update")
	}
	fact := originFenceFact{
		OriginID: id, ProviderID: cur.ProviderID, ProviderStatus: providerRow.Status,
		BaseURL: cur.BaseUrl, BaseURLRevision: cur.BaseUrlRevision,
		Status: cur.Status, StatusRevision: cur.StatusRevision,
		EffectiveStatus:     runtimecontrol.EffectiveOriginStatus(providerRow.Status, cur.Status),
		NextEffectiveStatus: runtimecontrol.EffectiveOriginStatus(providerRow.Status, newStatus),
	}
	if fact.EffectiveStatus == fact.NextEffectiveStatus {
		return ProviderOrigin{}, conflict("combined BaseURL/status update does not change effective status; update the fields separately")
	}
	result, err := s.fencer.updateRouting(ctx, fact, baseURL, newStatus)
	if err != nil {
		return ProviderOrigin{}, err
	}
	ep, err := s.Get(ctx, id)
	if err != nil {
		return ProviderOrigin{}, err
	}
	ep.RuntimeSyncPending = result.State == runtimecontrol.PublishRuntimeSyncPending
	return ep, nil
}

func (s *Service) enrichProviderName(ctx context.Context, ep ProviderOrigin) (ProviderOrigin, error) {
	if ep.ProviderID <= 0 || ep.ProviderName != "" {
		return ep, nil
	}
	p, err := s.store.GetProvider(ctx, ep.ProviderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ep, nil
		}
		return ProviderOrigin{}, storeFailed(err, "load provider for origin")
	}
	ep.ProviderName = p.Name
	return ep, nil
}

func toOrigin(r sqlc.ProviderOrigin) ProviderOrigin {
	return ProviderOrigin{
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

func toOriginRow(r sqlc.ListProviderOriginsPageRow) ProviderOrigin {
	return ProviderOrigin{
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
