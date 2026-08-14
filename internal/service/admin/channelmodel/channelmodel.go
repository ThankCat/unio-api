// Package channelmodel 编排 admin 管理端的 channel↔model 绑定读写。
//
// 绑定是路由边：表示某条 channel 能服务哪个 Unio 模型、转发到上游时用什么模型名。
// 解绑（Delete）在同一条语句内先清掉该边自身的 channel_prices（追加式成本价配置，无删除接口），
// 再删绑定；只有当该边确有计费/审计历史（cost_snapshots/price_snapshots/settlement_recovery_jobs
// 以 NO ACTION 外键引用 channel_prices）时才被 DB 拒绝（23503），上层降级为 conflict，提示改用停用。
package channelmodel

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/supply"
)

const (
	// StatusEnabled 表示绑定启用（参与路由）。
	StatusEnabled = "enabled"
	// StatusDisabled 表示绑定停用。
	StatusDisabled = "disabled"
)

// Store 定义 channel↔model 绑定管理所需的存储能力。
type Store interface {
	GetChannel(ctx context.Context, id int64) (sqlc.Channel, error)
	LookupModelByID(ctx context.Context, id int64) (sqlc.Model, error)
	ListChannelModelsByChannel(ctx context.Context, channelID int64) ([]sqlc.ListChannelModelsByChannelRow, error)
	GetChannelModel(ctx context.Context, arg sqlc.GetChannelModelParams) (sqlc.ChannelModel, error)
	CreateChannelModel(ctx context.Context, arg sqlc.CreateChannelModelParams) (sqlc.ChannelModel, error)
	UpdateChannelModel(ctx context.Context, arg sqlc.UpdateChannelModelParams) (sqlc.ChannelModel, error)
	GetCurrentChannelModelVerificationEvidence(ctx context.Context, arg sqlc.GetCurrentChannelModelVerificationEvidenceParams) (sqlc.ChannelModelVerificationItem, error)
	DeleteChannelModel(ctx context.Context, arg sqlc.DeleteChannelModelParams) (int64, error)
}

// TxBeginner 提供事务能力（由 pgxpool 满足），用于停用/解除/启用 Binding 时的
// ADR-0019 配置支撑串行化、影响预览与显式 Offering 联合操作。
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Binding 是 admin 视角的 channel↔model 绑定事实；连带 Unio 侧模型的对外 ID 与展示名（列表场景）。
type Binding struct {
	ID                int64
	ChannelID         int64
	ModelID           int64
	ModelExternalID   string
	ModelDisplayName  string
	ModelStatus       string
	UpstreamModel     string
	Status            string
	EffectiveBlockers []string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// CreateInput 是创建绑定的入参。
type CreateInput struct {
	ChannelID     int64
	ModelID       int64
	UpstreamModel string
	Status        string
}

// UpdateInput 是更新绑定的入参；按 (channel_id, model_id) 定位。
type UpdateInput struct {
	ChannelID     int64
	ModelID       int64
	UpstreamModel string
	Status        string
	// VerificationItemID 是启用绑定或替换 upstream_model 时必须提交的当前成功验证证据。
	VerificationItemID *int64
	// Confirmation 是停用或解绑前的影响确认与显式 Offering 选择（ADR-0019）。
	Confirmation supply.Confirmation
}

// Service 编排 channel↔model 绑定读写。
type Service struct {
	store   Store
	db      TxBeginner
	queries *sqlc.Queries
}

// NewService 创建绑定管理服务。db/queries 用于停用、解除与启用路径的
// 结构支撑串行化事务；只读与创建路径继续走 store。
func NewService(store Store, db TxBeginner, queries *sqlc.Queries) *Service {
	return &Service{store: store, db: db, queries: queries}
}

// List 列出某 channel 的全部模型绑定；channel 不存在返回 not_found。
func (s *Service) List(ctx context.Context, channelID int64) ([]Binding, error) {
	if channelID <= 0 {
		return nil, invalidArgument("channel_id", "channel id must be positive")
	}
	if err := s.ensureChannel(ctx, channelID); err != nil {
		return nil, err
	}

	rows, err := s.store.ListChannelModelsByChannel(ctx, channelID)
	if err != nil {
		return nil, storeFailed(err, "list channel models")
	}

	bindings := make([]Binding, 0, len(rows))
	for _, row := range rows {
		bindings = append(bindings, toBindingFromRow(row))
	}

	return bindings, nil
}

// Create 创建绑定：校验 channel、model 存在，再写入；重复绑定返回 conflict。
func (s *Service) Create(ctx context.Context, in CreateInput) (Binding, error) {
	if in.ChannelID <= 0 {
		return Binding{}, invalidArgument("channel_id", "channel id must be positive")
	}
	if in.ModelID <= 0 {
		return Binding{}, invalidArgument("model_id", "model_id must be positive")
	}
	upstreamModel := strings.TrimSpace(in.UpstreamModel)
	if upstreamModel == "" {
		return Binding{}, invalidArgument("upstream_model", "upstream_model is required")
	}
	if err := validateStatus(in.Status); err != nil {
		return Binding{}, err
	}
	// 新绑定一律先进入“已绑定、未启用”，验证成功后再由 Update 显式启用。
	in.Status = StatusDisabled

	if err := s.ensureChannel(ctx, in.ChannelID); err != nil {
		return Binding{}, err
	}
	if err := s.ensureModel(ctx, in.ModelID); err != nil {
		return Binding{}, err
	}

	row, err := s.store.CreateChannelModel(ctx, sqlc.CreateChannelModelParams{
		ChannelID:     in.ChannelID,
		ModelID:       in.ModelID,
		UpstreamModel: upstreamModel,
		Status:        in.Status,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Binding{}, conflict("this channel is already bound to the model")
		}
		if isForeignKeyViolation(err) {
			return Binding{}, invalidArgument("model_id", "channel or model not found")
		}
		return Binding{}, storeFailed(err, "create channel model")
	}

	return toBinding(row), nil
}

// Update 更新绑定的上游模型名与状态；目标不存在返回 not_found。
//
// 状态转换按 ADR-0019：enabled→disabled 在事务内锁定 Model、反查可能失去配置支撑的
// Offering（需影响指纹确认），只停止管理员明确选择的 Offering；Model 永不自动停用。
// disabled→enabled 允许在 Model 全局暂停时保存供给意图，运行时由 Model 状态阻断。
func (s *Service) Update(ctx context.Context, in UpdateInput) (Binding, error) {
	if in.ChannelID <= 0 {
		return Binding{}, invalidArgument("channel_id", "channel id must be positive")
	}
	if in.ModelID <= 0 {
		return Binding{}, invalidArgument("model_id", "model_id must be positive")
	}
	upstreamModel := strings.TrimSpace(in.UpstreamModel)
	if upstreamModel == "" {
		return Binding{}, invalidArgument("upstream_model", "upstream_model is required")
	}
	if err := validateStatus(in.Status); err != nil {
		return Binding{}, err
	}
	current, err := s.store.GetChannelModel(ctx, sqlc.GetChannelModelParams{ChannelID: in.ChannelID, ModelID: in.ModelID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Binding{}, notFound("channel model binding not found")
		}
		return Binding{}, storeFailed(err, "get channel model binding")
	}
	requiresVerification := current.UpstreamModel != upstreamModel ||
		(current.Status == StatusDisabled && in.Status == StatusEnabled)
	if requiresVerification {
		if in.VerificationItemID == nil || *in.VerificationItemID <= 0 {
			return Binding{}, conflict("a current successful model verification is required before enabling or remapping the binding")
		}
		if _, err := s.store.GetCurrentChannelModelVerificationEvidence(ctx, sqlc.GetCurrentChannelModelVerificationEvidenceParams{
			ItemID: *in.VerificationItemID, ChannelID: in.ChannelID, ModelID: in.ModelID, UpstreamModel: upstreamModel,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Binding{}, conflict("model verification is missing, failed, stale, or belongs to another binding")
			}
			return Binding{}, storeFailed(err, "validate channel model verification evidence")
		}
	}

	switch {
	case current.Status == StatusEnabled && in.Status == StatusDisabled:
		return s.disableBinding(ctx, in, upstreamModel)
	case current.Status == StatusDisabled && in.Status == StatusEnabled:
		return s.enableBinding(ctx, in, upstreamModel)
	}

	// 无状态转换（同状态改 upstream 或幂等重放）：不改变结构支撑，不触发联动。
	row, err := s.store.UpdateChannelModel(ctx, sqlc.UpdateChannelModelParams{
		ChannelID:     in.ChannelID,
		ModelID:       in.ModelID,
		UpstreamModel: upstreamModel,
		Status:        in.Status,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Binding{}, notFound("channel model binding not found")
		}
		return Binding{}, storeFailed(err, "update channel model")
	}

	return toBinding(row), nil
}

// disableBinding 在一个事务内停用 Binding，并可选停止管理员明确选择的 Offering。
func (s *Service) disableBinding(ctx context.Context, in UpdateInput, upstreamModel string) (Binding, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Binding{}, storeFailed(err, "begin binding disable transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	// 结构支撑串行化：先锁 Model，锁内重读绑定并计算影响，不复用锁外预检结果。
	if err := supply.LockModels(ctx, q, []int64{in.ModelID}); err != nil {
		return Binding{}, storeFailed(err, "lock model for binding disable")
	}
	current, err := q.GetChannelModel(ctx, sqlc.GetChannelModelParams{ChannelID: in.ChannelID, ModelID: in.ModelID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Binding{}, notFound("channel model binding not found")
		}
		return Binding{}, storeFailed(err, "get channel model binding in transaction")
	}

	var impact supply.Impact
	if current.Status == StatusEnabled {
		impact, err = supply.BindingImpact(ctx, q, in.ChannelID, in.ModelID)
		if err != nil {
			return Binding{}, storeFailed(err, "compute binding disable impact")
		}
		if err := supply.Authorize(impact,
			"channel_model_disable_confirmation_required",
			"disabling this binding may leave route offerings without configured support; confirm whether to keep or explicitly stop each offering",
			in.Confirmation,
		); err != nil {
			return Binding{}, err
		}
	}

	row, err := q.UpdateChannelModel(ctx, sqlc.UpdateChannelModelParams{
		ChannelID:     in.ChannelID,
		ModelID:       in.ModelID,
		UpstreamModel: upstreamModel,
		Status:        StatusDisabled,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Binding{}, notFound("channel model binding not found")
		}
		return Binding{}, storeFailed(err, "update channel model")
	}

	if current.Status == StatusEnabled {
		if err := supply.DisableSelectedOfferings(ctx, q, impact, in.Confirmation, supply.ReasonBindingDisabled); err != nil {
			return Binding{}, storeFailed(err, "disable selected offerings")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Binding{}, storeFailed(err, "commit binding disable transaction")
	}
	return toBinding(row), nil
}

// enableBinding 在一个事务内启用 Binding。Model 可以处于全局暂停状态；该状态只阻断
// Binding 生效，不改写 Binding 的配置意图。启用 Binding 不自动修改 Offering。
func (s *Service) enableBinding(ctx context.Context, in UpdateInput, upstreamModel string) (Binding, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Binding{}, storeFailed(err, "begin binding enable transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	// 与 Model/Offering 联合操作使用同一锁顺序，避免影响预览与配置写入漂移。
	if err := supply.LockModels(ctx, q, []int64{in.ModelID}); err != nil {
		return Binding{}, storeFailed(err, "lock model for binding enable")
	}
	_, err = q.LookupModelByID(ctx, in.ModelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Binding{}, invalidArgument("model_id", "model not found")
		}
		return Binding{}, storeFailed(err, "load model for binding enable")
	}
	row, err := q.UpdateChannelModel(ctx, sqlc.UpdateChannelModelParams{
		ChannelID:     in.ChannelID,
		ModelID:       in.ModelID,
		UpstreamModel: upstreamModel,
		Status:        StatusEnabled,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Binding{}, notFound("channel model binding not found")
		}
		return Binding{}, storeFailed(err, "update channel model")
	}

	if err := tx.Commit(ctx); err != nil {
		return Binding{}, storeFailed(err, "commit binding enable transaction")
	}
	return toBinding(row), nil
}

// Delete 删除绑定，并级联清掉该边自身的 channel_prices（追加式成本价配置）；仅当该边确有计费/审计历史
// 引用时才被 DB 拒绝（23503），降级为 conflict 提示改用停用。
//
// 解除 enabled Binding 与停用使用相同影响预览：只修改 Binding，自选 Offering 才会
// 在同一事务中停止售卖；Model 永不自动停用。
func (s *Service) Delete(ctx context.Context, channelID, modelID int64, confirmation supply.Confirmation) error {
	if channelID <= 0 {
		return invalidArgument("channel_id", "channel id must be positive")
	}
	if modelID <= 0 {
		return invalidArgument("model_id", "model_id must be positive")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return storeFailed(err, "begin binding unbind transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	// 结构支撑串行化：先锁 Model，锁内读取绑定状态并计算影响。
	if err := supply.LockModels(ctx, q, []int64{modelID}); err != nil {
		return storeFailed(err, "lock model for binding unbind")
	}
	current, err := q.GetChannelModel(ctx, sqlc.GetChannelModelParams{ChannelID: channelID, ModelID: modelID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("channel model binding not found")
		}
		return storeFailed(err, "get channel model binding in transaction")
	}

	var impact supply.Impact
	if current.Status == StatusEnabled {
		impact, err = supply.BindingImpact(ctx, q, channelID, modelID)
		if err != nil {
			return storeFailed(err, "compute binding unbind impact")
		}
		if err := supply.Authorize(impact,
			"channel_model_disable_confirmation_required",
			"unbinding may leave route offerings without configured support; confirm whether to keep or explicitly stop each offering",
			confirmation,
		); err != nil {
			return err
		}
	}

	affected, err := q.DeleteChannelModel(ctx, sqlc.DeleteChannelModelParams{
		ChannelID: channelID,
		ModelID:   modelID,
	})
	if err != nil {
		if isForeignKeyViolation(err) {
			return conflict("binding is referenced by billing history; disable it instead of deleting")
		}
		return storeFailed(err, "delete channel model")
	}
	if affected == 0 {
		return notFound("channel model binding not found")
	}

	if current.Status == StatusEnabled {
		if err := supply.DisableSelectedOfferings(ctx, q, impact, confirmation, supply.ReasonBindingDisabled); err != nil {
			return storeFailed(err, "disable selected offerings")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return storeFailed(err, "commit binding unbind transaction")
	}
	return nil
}

func (s *Service) ensureChannel(ctx context.Context, channelID int64) error {
	if _, err := s.store.GetChannel(ctx, channelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("channel not found")
		}
		return storeFailed(err, "load channel")
	}
	return nil
}

func (s *Service) ensureModel(ctx context.Context, modelID int64) error {
	if _, err := s.store.LookupModelByID(ctx, modelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return invalidArgument("model_id", "model not found")
		}
		return storeFailed(err, "load model")
	}
	return nil
}

func toBinding(c sqlc.ChannelModel) Binding {
	return Binding{
		ID:            c.ID,
		ChannelID:     c.ChannelID,
		ModelID:       c.ModelID,
		UpstreamModel: c.UpstreamModel,
		Status:        c.Status,
		CreatedAt:     c.CreatedAt.Time,
		UpdatedAt:     c.UpdatedAt.Time,
	}
}

func toBindingFromRow(c sqlc.ListChannelModelsByChannelRow) Binding {
	b := Binding{
		ID:               c.ID,
		ChannelID:        c.ChannelID,
		ModelID:          c.ModelID,
		ModelExternalID:  c.ModelExternalID,
		ModelDisplayName: c.ModelDisplayName,
		ModelStatus:      c.ModelStatus,
		UpstreamModel:    c.UpstreamModel,
		Status:           c.Status,
		CreatedAt:        c.CreatedAt.Time,
		UpdatedAt:        c.UpdatedAt.Time,
	}
	if c.Status != StatusEnabled {
		b.EffectiveBlockers = append(b.EffectiveBlockers, "binding_disabled")
	}
	if c.ModelStatus != "enabled" {
		b.EffectiveBlockers = append(b.EffectiveBlockers, "model_disabled")
	}
	return b
}

func validateStatus(status string) error {
	switch status {
	case StatusEnabled, StatusDisabled:
		return nil
	default:
		return invalidArgument("status", "status must be \"enabled\" or \"disabled\"")
	}
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
