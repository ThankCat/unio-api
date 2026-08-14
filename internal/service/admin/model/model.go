// Package model 编排 admin 管理端的 model 读写。
//
// 只做校验、存储编排与 sqlc row → 领域事实映射；不暴露 sqlc row 给上层。
// admin 手工创建的模型固定 source=manual，models.dev 同步永不覆盖（见 sql/queries/models.sql）。
package model

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/supply"
)

const (
	// StatusEnabled 表示 model 启用（对外可见、可路由）。
	StatusEnabled = "enabled"
	// StatusDisabled 表示 model 停用。
	StatusDisabled = "disabled"
)

// modelIDPattern 限定对外 model_id：字母数字开头，允许字母、数字、`.`、`_`、`:`、`-`，长度 1..128。
var modelIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// Store 定义 model 管理所需的存储能力。
type Store interface {
	ListModelsPage(ctx context.Context, arg sqlc.ListModelsPageParams) ([]sqlc.ListModelsPageRow, error)
	CountModels(ctx context.Context, arg sqlc.CountModelsParams) (int64, error)
	LookupModelByID(ctx context.Context, id int64) (sqlc.Model, error)
	GetModelCatalogState(ctx context.Context, modelID int64) (sqlc.GetModelCatalogStateRow, error)
	CreateModel(ctx context.Context, arg sqlc.CreateModelParams) (sqlc.Model, error)
	UpdateModel(ctx context.Context, arg sqlc.UpdateModelParams) (sqlc.Model, error)
	DeleteModelCascade(ctx context.Context, id int64) (int64, error)
}

// TxBeginner 提供事务能力（由 pgxpool 满足），用于 Model 手动停用时的
// ADR-0019 影响预览、显式联合操作与 Offering 恢复所需事务能力。
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ListParams 是分页/过滤列出 model 的入参；Status、Query 为空表示不过滤。
type ListParams struct {
	Status        string
	Query         string
	HasUpdateOnly bool
	Limit         int32
	Offset        int32
}

// ListResult 是分页列表结果：当前页条目 + 过滤后总数。
type ListResult struct {
	Items []Model
	Total int64
}

// Model 是 admin 视角的 model 业务事实。
// 元数据（上下文/价格基线/发布日期）纯展示，不参与计费；Catalog 为采纳目录追更状态（未采纳为 nil）。
type Model struct {
	ID                       int64
	ModelID                  string
	DisplayName              string
	OwnedBy                  string
	Status                   string
	MaxOutputTokens          *int64
	ContextWindowTokens      *int64
	InputPriceUSDPerMTokens  *string
	OutputPriceUSDPerMTokens *string
	ReleaseDate              *time.Time
	Source                   string
	Catalog                  *CatalogState
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// CatalogState 是采纳模型相对 models.dev 目录的追更状态（阶段 14）。
type CatalogState struct {
	CanonicalID     string
	UpdateAvailable bool
	RemovedUpstream bool
	ShouldRemind    bool
	ReminderMuted   bool
	SnoozeUntil     *time.Time
	DismissedSame   bool
}

// Metadata 是模型可选展示元数据（手建可填、采纳带入、刷新覆盖）。
type Metadata struct {
	ContextWindowTokens      *int64
	MaxOutputTokens          *int64
	InputPriceUSDPerMTokens  *string
	OutputPriceUSDPerMTokens *string
	ReleaseDate              *time.Time
}

// CreateInput 是创建 model 的入参；source 由服务层固定为 manual。
type CreateInput struct {
	ModelID     string
	DisplayName string
	OwnedBy     string
	Status      string
	Metadata
}

// UpdateInput 是更新 model 的入参；model_id 作为对外稳定标识不可变，不在此修改。
type UpdateInput struct {
	ID          int64
	DisplayName string
	OwnedBy     string
	Status      string
	Metadata
	// Confirmation 是全局暂停前的客户影响确认参数；不允许借此修改 Offering。
	Confirmation supply.Confirmation
}

// Service 编排 model 管理读写。
type Service struct {
	store   Store
	db      TxBeginner
	queries *sqlc.Queries
}

// NewService 创建 model 管理服务。db/queries 用于全局暂停、下架和 Offering 恢复事务；
// 其余读写继续走 store。
func NewService(store Store, db TxBeginner, queries *sqlc.Queries) *Service {
	return &Service{store: store, db: db, queries: queries}
}

// List 按 params 过滤分页列出 model，并返回过滤后的总数。
func (s *Service) List(ctx context.Context, params ListParams) (ListResult, error) {
	status := textParam(params.Status)
	q := textParam(params.Query)

	rows, err := s.store.ListModelsPage(ctx, sqlc.ListModelsPageParams{
		Status:        status,
		Q:             q,
		HasUpdateOnly: params.HasUpdateOnly,
		PageLimit:     params.Limit,
		PageOffset:    params.Offset,
	})
	if err != nil {
		return ListResult{}, storeFailed(err, "list models")
	}

	total, err := s.store.CountModels(ctx, sqlc.CountModelsParams{
		Status:        status,
		Q:             q,
		HasUpdateOnly: params.HasUpdateOnly,
	})
	if err != nil {
		return ListResult{}, storeFailed(err, "count models")
	}

	items := make([]Model, 0, len(rows))
	for _, row := range rows {
		items = append(items, modelFromListRow(row))
	}

	return ListResult{Items: items, Total: total}, nil
}

// Get 按内部主键读取单个 model。
func (s *Service) Get(ctx context.Context, id int64) (Model, error) {
	if id <= 0 {
		return Model{}, invalidArgument("id", "model id must be positive")
	}

	row, err := s.store.LookupModelByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Model{}, notFound("model not found")
		}
		return Model{}, storeFailed(err, "get model")
	}

	model := toModel(row)

	state, err := s.store.GetModelCatalogState(ctx, id)
	switch {
	case err == nil:
		model.Catalog = &CatalogState{
			CanonicalID:     state.CanonicalID,
			UpdateAvailable: state.UpdateAvailable,
			RemovedUpstream: state.CatalogRemovedUpstream,
			ShouldRemind:    state.ShouldRemind,
			ReminderMuted:   state.ReminderMuted,
			SnoozeUntil:     timePtr(state.ReminderSnoozeUntil),
			DismissedSame:   state.DismissedFingerprint.Valid && state.DismissedFingerprint.String == state.CatalogFingerprint,
		}
	case errors.Is(err, pgx.ErrNoRows):
		// 未采纳模型无目录关联，Catalog 保持 nil。
	default:
		return Model{}, storeFailed(err, "get model catalog state")
	}

	return model, nil
}

// Create 创建 model；model_id 重复返回 conflict。
func (s *Service) Create(ctx context.Context, in CreateInput) (Model, error) {
	modelID := strings.TrimSpace(in.ModelID)
	displayName := strings.TrimSpace(in.DisplayName)
	ownedBy := strings.TrimSpace(in.OwnedBy)
	status := strings.TrimSpace(in.Status)

	if !modelIDPattern.MatchString(modelID) {
		return Model{}, invalidArgument("model_id", "model_id must match ^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
	}
	if displayName == "" {
		return Model{}, invalidArgument("display_name", "display_name is required")
	}
	if ownedBy == "" {
		return Model{}, invalidArgument("owned_by", "owned_by is required")
	}
	if err := validateStatus(status); err != nil {
		return Model{}, err
	}
	meta, err := buildMetadataParams(in.Metadata)
	if err != nil {
		return Model{}, err
	}

	row, err := s.store.CreateModel(ctx, sqlc.CreateModelParams{
		ModelID:                        modelID,
		DisplayName:                    displayName,
		OwnedBy:                        ownedBy,
		Status:                         status,
		MaxOutputTokens:                meta.MaxOutputTokens,
		ContextWindowTokens:            meta.ContextWindowTokens,
		InputPriceUsdPerMillionTokens:  meta.InputPrice,
		OutputPriceUsdPerMillionTokens: meta.OutputPrice,
		ReleaseDate:                    meta.ReleaseDate,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Model{}, conflict("model_id already exists")
		}
		return Model{}, storeFailed(err, "create model")
	}

	return toModel(row), nil
}

// Update 更新 model 的展示元数据与状态；目标不存在返回 not_found。
//
// enabled→disabled 是全局暂停：在一个事务内锁定 Model、计算影响并经指纹确认后只修改
// Model 自身。disabled→enabled 会让既有 enabled Binding/Offering 意图重新生效。
func (s *Service) Update(ctx context.Context, in UpdateInput) (Model, error) {
	if in.ID <= 0 {
		return Model{}, invalidArgument("id", "model id must be positive")
	}
	displayName := strings.TrimSpace(in.DisplayName)
	ownedBy := strings.TrimSpace(in.OwnedBy)
	status := strings.TrimSpace(in.Status)

	if displayName == "" {
		return Model{}, invalidArgument("display_name", "display_name is required")
	}
	if ownedBy == "" {
		return Model{}, invalidArgument("owned_by", "owned_by is required")
	}
	if err := validateStatus(status); err != nil {
		return Model{}, err
	}
	meta, err := buildMetadataParams(in.Metadata)
	if err != nil {
		return Model{}, err
	}

	current, err := s.store.LookupModelByID(ctx, in.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Model{}, notFound("model not found")
		}
		return Model{}, storeFailed(err, "load model for update")
	}

	params := sqlc.UpdateModelParams{
		ID:                             in.ID,
		DisplayName:                    displayName,
		OwnedBy:                        ownedBy,
		Status:                         status,
		MaxOutputTokens:                meta.MaxOutputTokens,
		ContextWindowTokens:            meta.ContextWindowTokens,
		InputPriceUsdPerMillionTokens:  meta.InputPrice,
		OutputPriceUsdPerMillionTokens: meta.OutputPrice,
		ReleaseDate:                    meta.ReleaseDate,
	}

	if current.Status == StatusEnabled && status == StatusDisabled {
		return s.pauseModel(ctx, in.ID, params, in.Confirmation)
	}

	row, err := s.store.UpdateModel(ctx, params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Model{}, notFound("model not found")
		}
		return Model{}, storeFailed(err, "update model")
	}

	return toModel(row), nil
}

// pauseModel 在一个事务内全局暂停 Model。Binding 与 Offering 配置状态保持不变。
func (s *Service) pauseModel(ctx context.Context, modelID int64, params sqlc.UpdateModelParams, confirmation supply.Confirmation) (Model, error) {
	if s.db == nil || s.queries == nil {
		return Model{}, storeFailed(errors.New("supply linkage dependencies are unavailable"), "disable model")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Model{}, storeFailed(err, "begin model disable transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	if err := supply.LockModels(ctx, q, []int64{modelID}); err != nil {
		return Model{}, storeFailed(err, "lock model for disable")
	}
	impact, err := supply.ModelImpact(ctx, q, modelID)
	if err != nil {
		return Model{}, storeFailed(err, "compute model disable impact")
	}
	if err := supply.Authorize(impact,
		"model_disable_confirmation_required",
		"pausing this model blocks the affected route offerings without changing their saved intent; confirm with the impact fingerprint",
		confirmation,
	); err != nil {
		return Model{}, err
	}
	if len(confirmation.SelectedOfferings) > 0 {
		return Model{}, invalidArgument("selected_offerings", "global model pause cannot modify route offerings; use the model delist action")
	}

	row, err := q.UpdateModel(ctx, params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Model{}, notFound("model not found")
		}
		return Model{}, storeFailed(err, "update model")
	}
	if err := tx.Commit(ctx); err != nil {
		return Model{}, storeFailed(err, "commit model disable transaction")
	}
	return toModel(row), nil
}

// Delist 全局下架 Model，并只停止管理员在最新影响预览中明确选择的 Offering。
// Binding 配置保持不变；未选择的 Offering 继续保存 enabled 售卖意图，但受 Model 暂停阻断。
func (s *Service) Delist(ctx context.Context, modelID int64, confirmation supply.Confirmation) (int, error) {
	if modelID <= 0 {
		return 0, invalidArgument("id", "model id must be positive")
	}
	if s.db == nil || s.queries == nil {
		return 0, storeFailed(errors.New("supply linkage dependencies are unavailable"), "delist model")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, storeFailed(err, "begin model delist transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	if err := supply.LockModels(ctx, q, []int64{modelID}); err != nil {
		return 0, storeFailed(err, "lock model for delist")
	}
	if _, err := q.LookupModelByID(ctx, modelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, notFound("model not found")
		}
		return 0, storeFailed(err, "load model for delist")
	}
	impact, err := supply.ModelImpact(ctx, q, modelID)
	if err != nil {
		return 0, storeFailed(err, "compute model delist impact")
	}
	impact.Kind = "model_delist"
	if err := supply.Authorize(impact,
		"model_delist_confirmation_required",
		"delisting pauses the model and stops only the route offerings selected below; confirm with the latest impact fingerprint",
		confirmation,
	); err != nil {
		return 0, err
	}
	if len(confirmation.SelectedOfferings) == 0 {
		return 0, invalidArgument("selected_offerings", "select at least one route offering to delist")
	}
	if _, err := q.DisableModelSupply(ctx, modelID); err != nil {
		return 0, storeFailed(err, "pause model during delist")
	}
	if err := supply.DisableSelectedOfferings(ctx, q, impact, confirmation, supply.ReasonModelDelisted); err != nil {
		return 0, storeFailed(err, "disable selected model offerings")
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, storeFailed(err, "commit model delist transaction")
	}
	return len(confirmation.SelectedOfferings), nil
}

// ModelOffering 是该 Model 在某条 Route 上的一条 disabled 售卖组合（批量恢复入口）。
type ModelOffering struct {
	RouteID         int64
	RouteName       string
	RouteStatus     string
	IngressProtocol string
	DisabledReason  *string
	DisabledAt      *time.Time
	// SupportAvailable 表示该 Route 当前池内结构支撑是否已恢复（可勾选恢复的前提）。
	SupportAvailable bool
	Restorable       bool
	RestoreBlockers  []string
	RestoreWarnings  []string
}

// OfferingRestoreItem 是批量恢复请求中勾选的一条组合。
type OfferingRestoreItem struct {
	RouteID         int64
	IngressProtocol string
}

// ListDisabledOfferings 按 Model 聚合列出 disabled Offering（可按协议过滤），
// 附配置支撑与恢复状态，供批量恢复入口展示（ADR-0019）。
func (s *Service) ListDisabledOfferings(ctx context.Context, modelID int64, ingressProtocol string) ([]ModelOffering, error) {
	if modelID <= 0 {
		return nil, invalidArgument("id", "model id must be positive")
	}
	m, err := s.store.LookupModelByID(ctx, modelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound("model not found")
		}
		return nil, storeFailed(err, "load model")
	}
	protocol := pgtype.Text{}
	if strings.TrimSpace(ingressProtocol) != "" {
		protocol = pgtype.Text{String: strings.TrimSpace(ingressProtocol), Valid: true}
	}
	rows, err := s.queries.ListDisabledOfferingsForModel(ctx, sqlc.ListDisabledOfferingsForModelParams{
		ModelID:         modelID,
		IngressProtocol: protocol,
	})
	if err != nil {
		return nil, storeFailed(err, "list disabled offerings")
	}
	out := make([]ModelOffering, 0, len(rows))
	for _, row := range rows {
		o := ModelOffering{
			RouteID:          row.RouteID,
			RouteName:        row.RouteName,
			RouteStatus:      row.RouteStatus,
			IngressProtocol:  row.IngressProtocol,
			SupportAvailable: row.SupportAvailable,
			Restorable:       row.RouteStatus != "archived",
		}
		if row.RouteStatus == "archived" {
			o.RestoreBlockers = append(o.RestoreBlockers, "route_archived")
		}
		if m.Status != StatusEnabled {
			o.RestoreWarnings = append(o.RestoreWarnings, "model_disabled")
		}
		if !row.SupportAvailable {
			o.RestoreWarnings = append(o.RestoreWarnings, "no_configured_support")
		}
		if row.DisabledReason.Valid {
			reason := row.DisabledReason.String
			o.DisabledReason = &reason
		}
		if row.DisabledAt.Valid {
			at := row.DisabledAt.Time
			o.DisabledAt = &at
		}
		out = append(out, o)
	}
	return out, nil
}

// RestoreOfferings 批量恢复该 Model 的 disabled Offering（ADR-0019）：
// 在一个事务内锁定 Model，逐条重新校验（Offering 存在且 disabled、Route 未归档），
// 经影响指纹确认后把全部通过的条目置 enabled 并清空停用原因和时间；
// 任一条目校验失败则整批拒绝，不部分提交。
func (s *Service) RestoreOfferings(ctx context.Context, modelID int64, items []OfferingRestoreItem, confirmation supply.Confirmation) (int, error) {
	if modelID <= 0 {
		return 0, invalidArgument("id", "model id must be positive")
	}
	if len(items) == 0 {
		return 0, invalidArgument("items", "at least one offering must be selected")
	}
	if s.db == nil || s.queries == nil {
		return 0, storeFailed(errors.New("supply linkage dependencies are unavailable"), "restore offerings")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, storeFailed(err, "begin offering restore transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	// 扩张侧串行化：锁定 Model 后在锁内校验一切事实。
	if err := supply.LockModels(ctx, q, []int64{modelID}); err != nil {
		return 0, storeFailed(err, "lock model for offering restore")
	}
	m, err := q.LookupModelByID(ctx, modelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, notFound("model not found")
		}
		return 0, storeFailed(err, "load model for offering restore")
	}
	disabled, err := q.ListDisabledOfferingsForModel(ctx, sqlc.ListDisabledOfferingsForModelParams{ModelID: modelID})
	if err != nil {
		return 0, storeFailed(err, "list disabled offerings in lock")
	}
	type restoreKey struct {
		routeID  int64
		protocol string
	}
	disabledByKey := make(map[restoreKey]sqlc.ListDisabledOfferingsForModelRow, len(disabled))
	for _, row := range disabled {
		disabledByKey[restoreKey{row.RouteID, row.IngressProtocol}] = row
	}

	affected := make([]supply.AffectedOffering, 0, len(items))
	seen := make(map[restoreKey]struct{}, len(items))
	for _, item := range items {
		if item.RouteID <= 0 {
			return 0, invalidArgument("items", "route id must be positive")
		}
		key := restoreKey{item.RouteID, item.IngressProtocol}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		row, ok := disabledByKey[key]
		if !ok {
			return 0, conflict("selected offering is missing or already enabled; refresh and retry")
		}
		if row.RouteStatus == "archived" {
			return 0, conflict("selected offering belongs to an archived route; restore the route first")
		}
		result := "available"
		if m.Status != StatusEnabled {
			result = "404"
		} else if !row.SupportAvailable {
			result = "503"
		}
		affected = append(affected, supply.AffectedOffering{
			RouteID:          row.RouteID,
			RouteName:        row.RouteName,
			RouteStatus:      row.RouteStatus,
			ModelID:          modelID,
			PublicModelID:    m.ModelID,
			ModelDisplayName: m.DisplayName,
			IngressProtocol:  row.IngressProtocol,
			KeptResult:       "404",
			SelectedResult:   result,
		})
	}

	impact := supply.Impact{Kind: "offering_restore", AffectedOfferings: affected}
	if err := supply.Authorize(impact,
		"offering_restore_confirmation_required",
		"restoring these offerings makes the routes sell the model again; confirm with the impact fingerprint",
		confirmation,
	); err != nil {
		return 0, err
	}

	for _, ao := range affected {
		if err := q.EnableRouteModelOffering(ctx, sqlc.EnableRouteModelOfferingParams{
			RouteID:         ao.RouteID,
			ModelID:         modelID,
			IngressProtocol: ao.IngressProtocol,
		}); err != nil {
			return 0, storeFailed(err, "enable route model offering")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, storeFailed(err, "commit offering restore transaction")
	}
	return len(affected), nil
}

// Delete 物理删除 model，用于清理录错的脏数据，并级联清理它自身的配置子表
// （售价、模型绑定、成本价、能力声明、项目可见性策略）。model_id 随之释放，可重新录入同名。
//
// 一旦 model 或其子配置已被请求/账务历史（NO ACTION 外键）引用，DB 拒绝删除（23503），
// 降级为 conflict，提示改用停用——保住计费/审计链路。
func (s *Service) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return invalidArgument("id", "model id must be positive")
	}

	affected, err := s.store.DeleteModelCascade(ctx, id)
	if err != nil {
		if isForeignKeyViolation(err) {
			return conflict("model is referenced by request/billing history; disable it instead of deleting")
		}
		return storeFailed(err, "delete model")
	}
	if affected == 0 {
		return notFound("model not found")
	}

	return nil
}

func toModel(m sqlc.Model) Model {
	return Model{
		ID:                       m.ID,
		ModelID:                  m.ModelID,
		DisplayName:              m.DisplayName,
		OwnedBy:                  m.OwnedBy,
		Status:                   m.Status,
		MaxOutputTokens:          int64Ptr(m.MaxOutputTokens),
		ContextWindowTokens:      int64Ptr(m.ContextWindowTokens),
		InputPriceUSDPerMTokens:  numericString(m.InputPriceUsdPerMillionTokens),
		OutputPriceUSDPerMTokens: numericString(m.OutputPriceUsdPerMillionTokens),
		ReleaseDate:              datePtr(m.ReleaseDate),
		Source:                   m.Source,
		CreatedAt:                m.CreatedAt.Time,
		UpdatedAt:                m.UpdatedAt.Time,
	}
}

// modelFromListRow 把列表行（含采纳目录追更状态）映射为领域 Model。
func modelFromListRow(m sqlc.ListModelsPageRow) Model {
	out := Model{
		ID:                       m.ID,
		ModelID:                  m.ModelID,
		DisplayName:              m.DisplayName,
		OwnedBy:                  m.OwnedBy,
		Status:                   m.Status,
		MaxOutputTokens:          int64Ptr(m.MaxOutputTokens),
		ContextWindowTokens:      int64Ptr(m.ContextWindowTokens),
		InputPriceUSDPerMTokens:  numericString(m.InputPriceUsdPerMillionTokens),
		OutputPriceUSDPerMTokens: numericString(m.OutputPriceUsdPerMillionTokens),
		ReleaseDate:              datePtr(m.ReleaseDate),
		Source:                   m.Source,
		CreatedAt:                m.CreatedAt.Time,
		UpdatedAt:                m.UpdatedAt.Time,
	}
	if m.CatalogCanonicalID.Valid {
		out.Catalog = &CatalogState{
			CanonicalID:     m.CatalogCanonicalID.String,
			UpdateAvailable: m.UpdateAvailable,
			RemovedUpstream: m.CatalogRemovedUpstream,
			ShouldRemind:    m.ShouldRemind,
			ReminderMuted:   m.ReminderMuted.Bool,
			SnoozeUntil:     timePtr(m.ReminderSnoozeUntil),
			DismissedSame:   m.DismissedFingerprint.Valid && m.CatalogFingerprint.Valid && m.DismissedFingerprint.String == m.CatalogFingerprint.String,
		}
	}
	return out
}

// metadataParams 是元数据列的 sqlc 入参集合。
type metadataParams struct {
	ContextWindowTokens pgtype.Int8
	MaxOutputTokens     pgtype.Int8
	InputPrice          pgtype.Numeric
	OutputPrice         pgtype.Numeric
	ReleaseDate         pgtype.Date
}

// buildMetadataParams 校验并转换可选元数据为 sqlc 入参。
func buildMetadataParams(in Metadata) (metadataParams, error) {
	if in.MaxOutputTokens != nil && *in.MaxOutputTokens <= 0 {
		return metadataParams{}, invalidArgument("max_output_tokens", "max_output_tokens must be > 0 when set")
	}
	if in.ContextWindowTokens != nil && *in.ContextWindowTokens <= 0 {
		return metadataParams{}, invalidArgument("context_window_tokens", "context_window_tokens must be > 0 when set")
	}
	inputPrice, err := numericParam(in.InputPriceUSDPerMTokens)
	if err != nil {
		return metadataParams{}, invalidArgument("input_price_usd_per_million_tokens", "input price must be a non-negative decimal")
	}
	outputPrice, err := numericParam(in.OutputPriceUSDPerMTokens)
	if err != nil {
		return metadataParams{}, invalidArgument("output_price_usd_per_million_tokens", "output price must be a non-negative decimal")
	}
	return metadataParams{
		ContextWindowTokens: int8Param(in.ContextWindowTokens),
		MaxOutputTokens:     int8Param(in.MaxOutputTokens),
		InputPrice:          inputPrice,
		OutputPrice:         outputPrice,
		ReleaseDate:         dateParam(in.ReleaseDate),
	}, nil
}

func validateStatus(status string) error {
	switch status {
	case StatusEnabled, StatusDisabled:
		return nil
	default:
		return invalidArgument("status", fmt.Sprintf("status must be %q or %q", StatusEnabled, StatusDisabled))
	}
}

// textParam 把空串转成 NULL（不写值），非空转成有值 pgtype.Text。
func textParam(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// int8Param 把 nil 转成 NULL，非 nil 转成有值 pgtype.Int8。
func int8Param(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

// numericParam 把可选十进制字符串转成 pgtype.Numeric；nil/空写 NULL，非法或负值返回错误。
func numericParam(value *string) (pgtype.Numeric, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return pgtype.Numeric{}, nil
	}
	var n pgtype.Numeric
	if err := n.Scan(strings.TrimSpace(*value)); err != nil {
		return pgtype.Numeric{}, err
	}
	if n.Valid && n.Int != nil && n.Int.Sign() < 0 {
		return pgtype.Numeric{}, fmt.Errorf("price must be non-negative")
	}
	return n, nil
}

// dateParam 把可选日期转成 pgtype.Date，nil 写 NULL。
func dateParam(value *time.Time) pgtype.Date {
	if value == nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: *value, Valid: true}
}

// int64Ptr 把 pgtype.Int8 转成可选 int64。
func int64Ptr(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	out := value.Int64
	return &out
}

// timePtr 把 pgtype.Timestamptz 转成可选 time.Time。
func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time
	return &out
}

// datePtr 把 pgtype.Date 转成可选 time.Time。
func datePtr(value pgtype.Date) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time
	return &out
}

// numericString 把 NUMERIC 精确格式化为十进制字符串（不用 float），NULL/NaN/Inf 返回 nil。
func numericString(value pgtype.Numeric) *string {
	if !value.Valid || value.NaN || value.InfinityModifier != pgtype.Finite {
		return nil
	}
	if value.Int == nil {
		zero := "0"
		return &zero
	}
	negative := value.Int.Sign() < 0
	digits := new(big.Int).Abs(value.Int).String()
	exp := int(value.Exp)

	var formatted string
	switch {
	case exp == 0:
		formatted = digits
	case exp > 0:
		formatted = digits + strings.Repeat("0", exp)
	default:
		scale := -exp
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		point := len(digits) - scale
		formatted = digits[:point] + "." + digits[point:]
	}
	if negative {
		formatted = "-" + formatted
	}
	return &formatted
}

func invalidArgument(field, message string) error {
	return failure.New(
		failure.CodeAdminInvalidArgument,
		failure.WithMessage(message),
		failure.WithField("field", field),
	)
}

func notFound(message string) error {
	return failure.New(failure.CodeAdminNotFound, failure.WithMessage(message))
}

func conflict(message string) error {
	return failure.New(failure.CodeAdminConflict, failure.WithMessage(message))
}

func storeFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
