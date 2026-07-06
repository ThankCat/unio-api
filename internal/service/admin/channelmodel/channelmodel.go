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

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
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
	DeleteChannelModel(ctx context.Context, arg sqlc.DeleteChannelModelParams) (int64, error)
}

// Binding 是 admin 视角的 channel↔model 绑定事实；连带 Unio 侧模型的对外 ID 与展示名（列表场景）。
type Binding struct {
	ID               int64
	ChannelID        int64
	ModelID          int64
	ModelExternalID  string
	ModelDisplayName string
	UpstreamModel    string
	Status           string
	CreatedAt        time.Time
	UpdatedAt        time.Time
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
}

// Service 编排 channel↔model 绑定读写。
type Service struct {
	store Store
}

// NewService 创建绑定管理服务。
func NewService(store Store) *Service {
	return &Service{store: store}
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

// Delete 删除绑定，并级联清掉该边自身的 channel_prices（追加式成本价配置）；仅当该边确有计费/审计历史
// 引用时才被 DB 拒绝（23503），降级为 conflict 提示改用停用。
func (s *Service) Delete(ctx context.Context, channelID, modelID int64) error {
	if channelID <= 0 {
		return invalidArgument("channel_id", "channel id must be positive")
	}
	if modelID <= 0 {
		return invalidArgument("model_id", "model_id must be positive")
	}

	affected, err := s.store.DeleteChannelModel(ctx, sqlc.DeleteChannelModelParams{
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
	return Binding{
		ID:               c.ID,
		ChannelID:        c.ChannelID,
		ModelID:          c.ModelID,
		ModelExternalID:  c.ModelExternalID,
		ModelDisplayName: c.ModelDisplayName,
		UpstreamModel:    c.UpstreamModel,
		Status:           c.Status,
		CreatedAt:        c.CreatedAt.Time,
		UpdatedAt:        c.UpdatedAt.Time,
	}
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
