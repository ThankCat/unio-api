// Package channelcostmultiplier 编排 admin 管理端的渠道价格倍率（channel_cost_multipliers）读写（DEC-027）。
//
// 上游名义成本 = model_prices（模型基准价，DEC-031 成本基数）× 本倍率。model_id 为空=渠道默认倍率；非空=对该模型的覆盖。
// 设计约束：倍率不可改数值（改倍率靠「新建一条 + 关闭旧窗口」）；同一 channel + 同 model_key 的启用窗口不可重叠。
package channelcostmultiplier

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

const (
	// StatusEnabled 表示倍率启用（参与结算派生成本）。
	StatusEnabled = "enabled"
	// StatusDisabled 表示倍率停用。
	StatusDisabled = "disabled"
)

// Store 定义渠道价格倍率管理所需的存储能力。
type Store interface {
	GetChannel(ctx context.Context, id int64) (sqlc.Channel, error)
	GetChannelModel(ctx context.Context, arg sqlc.GetChannelModelParams) (sqlc.ChannelModel, error)
	GetChannelCostMultiplier(ctx context.Context, id int64) (sqlc.ChannelCostMultiplier, error)
	ListChannelCostMultipliersByChannel(ctx context.Context, channelID int64) ([]sqlc.ListChannelCostMultipliersByChannelRow, error)
	ListEnabledChannelCostMultiplierWindows(ctx context.Context, arg sqlc.ListEnabledChannelCostMultiplierWindowsParams) ([]sqlc.ListEnabledChannelCostMultiplierWindowsRow, error)
	CreateChannelCostMultiplier(ctx context.Context, arg sqlc.CreateChannelCostMultiplierParams) (sqlc.ChannelCostMultiplier, error)
	UpdateChannelCostMultiplierWindow(ctx context.Context, arg sqlc.UpdateChannelCostMultiplierWindowParams) (sqlc.ChannelCostMultiplier, error)
}

// ChannelCostMultiplier 是 admin 视角的渠道价格倍率事实。ModelID 为 nil 表示渠道默认倍率。
type ChannelCostMultiplier struct {
	ID               int64
	ChannelID        int64
	ModelID          *int64
	ModelExternalID  *string
	ModelDisplayName *string
	Multiplier       string
	Status           string
	EffectiveFrom    time.Time
	EffectiveTo      *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// CreateInput 是创建渠道价格倍率的入参。ModelID 为 nil 表示渠道默认倍率；非 nil 表示对该模型的覆盖。
type CreateInput struct {
	ChannelID     int64
	ModelID       *int64
	Multiplier    string
	Status        string
	EffectiveFrom time.Time
	EffectiveTo   *time.Time
}

// UpdateInput 是 PATCH 渠道价格倍率的入参：只改启停状态与生效结束时间；倍率数值不可改。
type UpdateInput struct {
	ID          int64
	Status      string
	EffectiveTo *time.Time
}

// Service 编排渠道价格倍率读写。
type Service struct {
	store Store
}

// NewService 创建渠道价格倍率管理服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// List 列出某 channel 下全部价格倍率（默认 + 逐模型覆盖，含历史与停用）；channel 不存在返回 not_found。
func (s *Service) List(ctx context.Context, channelID int64) ([]ChannelCostMultiplier, error) {
	if channelID <= 0 {
		return nil, invalidArgument("channel_id", "channel id must be positive")
	}
	if _, err := s.store.GetChannel(ctx, channelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound("channel not found")
		}
		return nil, storeFailed(err, "load channel")
	}

	rows, err := s.store.ListChannelCostMultipliersByChannel(ctx, channelID)
	if err != nil {
		return nil, storeFailed(err, "list channel cost multipliers")
	}

	out := make([]ChannelCostMultiplier, 0, len(rows))
	for _, row := range rows {
		out = append(out, toChannelCostMultiplierFromRow(row))
	}

	return out, nil
}

// Create 创建一条渠道价格倍率：校验渠道存在、（逐模型覆盖时）绑定存在、倍率合法、生效窗口不重叠。
func (s *Service) Create(ctx context.Context, in CreateInput) (ChannelCostMultiplier, error) {
	if in.ChannelID <= 0 {
		return ChannelCostMultiplier{}, invalidArgument("channel_id", "channel id must be positive")
	}
	if in.ModelID != nil && *in.ModelID <= 0 {
		return ChannelCostMultiplier{}, invalidArgument("model_id", "model_id must be positive when provided")
	}
	if err := validateStatus(in.Status); err != nil {
		return ChannelCostMultiplier{}, err
	}
	if in.EffectiveFrom.IsZero() {
		return ChannelCostMultiplier{}, invalidArgument("effective_from", "effective_from is required")
	}
	if in.EffectiveTo != nil && !in.EffectiveTo.After(in.EffectiveFrom) {
		return ChannelCostMultiplier{}, invalidArgument("effective_to", "effective_to must be after effective_from")
	}

	multiplier, err := parseMultiplier(in.Multiplier)
	if err != nil {
		return ChannelCostMultiplier{}, err
	}

	if _, err := s.store.GetChannel(ctx, in.ChannelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelCostMultiplier{}, notFound("channel not found")
		}
		return ChannelCostMultiplier{}, storeFailed(err, "load channel")
	}

	// 逐模型覆盖：该 (channel, model) 绑定必须存在。
	if in.ModelID != nil {
		if _, err := s.store.GetChannelModel(ctx, sqlc.GetChannelModelParams{
			ChannelID: in.ChannelID,
			ModelID:   *in.ModelID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ChannelCostMultiplier{}, invalidArgument("model_id", "channel is not bound to this model")
			}
			return ChannelCostMultiplier{}, storeFailed(err, "load channel model binding")
		}
	}

	if in.Status == StatusEnabled {
		if err := s.ensureNoOverlap(ctx, in.ChannelID, modelIDParam(in.ModelID), 0, in.EffectiveFrom, in.EffectiveTo); err != nil {
			return ChannelCostMultiplier{}, err
		}
	}

	row, err := s.store.CreateChannelCostMultiplier(ctx, sqlc.CreateChannelCostMultiplierParams{
		ChannelID:     in.ChannelID,
		ModelID:       modelIDParam(in.ModelID),
		Multiplier:    multiplier,
		Status:        in.Status,
		EffectiveFrom: tsParam(&in.EffectiveFrom),
		EffectiveTo:   tsParam(in.EffectiveTo),
	})
	if err != nil {
		return ChannelCostMultiplier{}, storeFailed(err, "create channel cost multiplier")
	}

	return toChannelCostMultiplier(row), nil
}

// Update 调整窗口/启停：改 effective_to（关闭窗口）与 status；倍率数值不可改。重新启用或延长窗口时复查重叠。
func (s *Service) Update(ctx context.Context, in UpdateInput) (ChannelCostMultiplier, error) {
	if in.ID <= 0 {
		return ChannelCostMultiplier{}, invalidArgument("id", "id must be positive")
	}
	if err := validateStatus(in.Status); err != nil {
		return ChannelCostMultiplier{}, err
	}

	existing, err := s.store.GetChannelCostMultiplier(ctx, in.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelCostMultiplier{}, notFound("channel cost multiplier not found")
		}
		return ChannelCostMultiplier{}, storeFailed(err, "load channel cost multiplier")
	}

	if in.EffectiveTo != nil && !in.EffectiveTo.After(existing.EffectiveFrom.Time) {
		return ChannelCostMultiplier{}, invalidArgument("effective_to", "effective_to must be after effective_from")
	}

	if in.Status == StatusEnabled {
		if err := s.ensureNoOverlap(ctx, existing.ChannelID, existing.ModelID, existing.ID, existing.EffectiveFrom.Time, in.EffectiveTo); err != nil {
			return ChannelCostMultiplier{}, err
		}
	}

	row, err := s.store.UpdateChannelCostMultiplierWindow(ctx, sqlc.UpdateChannelCostMultiplierWindowParams{
		ID:          in.ID,
		Status:      in.Status,
		EffectiveTo: tsParam(in.EffectiveTo),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelCostMultiplier{}, notFound("channel cost multiplier not found")
		}
		return ChannelCostMultiplier{}, storeFailed(err, "update channel cost multiplier")
	}

	return toChannelCostMultiplier(row), nil
}

// ensureNoOverlap 校验目标窗口与同一 channel + model_key 现有启用窗口不重叠（半开区间 [from, to)）。
func (s *Service) ensureNoOverlap(ctx context.Context, channelID int64, modelID pgtype.Int8, excludeID int64, from time.Time, to *time.Time) error {
	windows, err := s.store.ListEnabledChannelCostMultiplierWindows(ctx, sqlc.ListEnabledChannelCostMultiplierWindowsParams{
		ChannelID: channelID,
		ModelID:   modelID,
		ExcludeID: excludeID,
	})
	if err != nil {
		return storeFailed(err, "list enabled channel cost multiplier windows")
	}

	for _, w := range windows {
		var existingTo *time.Time
		if w.EffectiveTo.Valid {
			t := w.EffectiveTo.Time
			existingTo = &t
		}
		if windowsOverlap(from, to, w.EffectiveFrom.Time, existingTo) {
			return failure.New(
				failure.CodeAdminPricingWindowOverlap,
				failure.WithMessage("effective window overlaps an existing enabled channel cost multiplier"),
			)
		}
	}

	return nil
}

// windowsOverlap 判断两个半开区间 [aFrom, aTo) 与 [bFrom, bTo) 是否相交；nil 结束时间表示 +∞。
func windowsOverlap(aFrom time.Time, aTo *time.Time, bFrom time.Time, bTo *time.Time) bool {
	aStartsBeforeBEnds := bTo == nil || aFrom.Before(*bTo)
	bStartsBeforeAEnds := aTo == nil || bFrom.Before(*aTo)
	return aStartsBeforeBEnds && bStartsBeforeAEnds
}

func toChannelCostMultiplier(c sqlc.ChannelCostMultiplier) ChannelCostMultiplier {
	return ChannelCostMultiplier{
		ID:            c.ID,
		ChannelID:     c.ChannelID,
		ModelID:       int8Ptr(c.ModelID),
		Multiplier:    numericString(c.Multiplier),
		Status:        c.Status,
		EffectiveFrom: c.EffectiveFrom.Time,
		EffectiveTo:   timePtr(c.EffectiveTo),
		CreatedAt:     c.CreatedAt.Time,
		UpdatedAt:     c.UpdatedAt.Time,
	}
}

func toChannelCostMultiplierFromRow(c sqlc.ListChannelCostMultipliersByChannelRow) ChannelCostMultiplier {
	return ChannelCostMultiplier{
		ID:               c.ID,
		ChannelID:        c.ChannelID,
		ModelID:          int8Ptr(c.ModelID),
		ModelExternalID:  textPtr(c.ModelExternalID),
		ModelDisplayName: textPtr(c.ModelDisplayName),
		Multiplier:       numericString(c.Multiplier),
		Status:           c.Status,
		EffectiveFrom:    c.EffectiveFrom.Time,
		EffectiveTo:      timePtr(c.EffectiveTo),
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

// parseMultiplier 解析倍率：非负十进制字符串 → pgtype.Numeric。
func parseMultiplier(raw string) (pgtype.Numeric, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return pgtype.Numeric{}, invalidArgument("multiplier", "is required")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || strings.ContainsAny(s, "eE") {
		return pgtype.Numeric{}, invalidArgument("multiplier", "must be a non-negative decimal")
	}
	if r.Sign() < 0 {
		return pgtype.Numeric{}, invalidArgument("multiplier", "must be a non-negative decimal")
	}
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		return pgtype.Numeric{}, invalidArgument("multiplier", "invalid decimal")
	}
	return n, nil
}

// modelIDParam 把可选 model id 转成 pgtype.Int8；nil → SQL NULL（渠道默认倍率）。
func modelIDParam(modelID *int64) pgtype.Int8 {
	if modelID == nil {
		return pgtype.Int8{Valid: false}
	}
	return pgtype.Int8{Int64: *modelID, Valid: true}
}

func int8Ptr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	out := v.Int64
	return &out
}

func textPtr(v pgtype.Text) *string {
	if !v.Valid {
		return nil
	}
	out := v.String
	return &out
}

func tsParam(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{Valid: false}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	out := t.Time
	return &out
}

// numericString 把 NUMERIC 精确格式化为十进制字符串（不用 float）；NULL/NaN/Inf → "0"。
func numericString(n pgtype.Numeric) string {
	if !n.Valid || n.NaN || n.InfinityModifier != pgtype.Finite {
		return "0"
	}
	if n.Int == nil {
		return "0"
	}

	negative := n.Int.Sign() < 0
	digits := new(big.Int).Abs(n.Int).String()
	exp := int(n.Exp)

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
	return formatted
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

func storeFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}
