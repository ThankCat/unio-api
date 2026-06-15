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

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
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
}

// Service 编排 model 管理读写。
type Service struct {
	store Store
}

// NewService 创建 model 管理服务。
func NewService(store Store) *Service {
	return &Service{store: store}
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

	row, err := s.store.UpdateModel(ctx, sqlc.UpdateModelParams{
		ID:                             in.ID,
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
		if errors.Is(err, pgx.ErrNoRows) {
			return Model{}, notFound("model not found")
		}
		return Model{}, storeFailed(err, "update model")
	}

	return toModel(row), nil
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
