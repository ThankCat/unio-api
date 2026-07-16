// Package channelrechargefactor 编排 admin 管理端的渠道充值倍率（channel_recharge_factors）读写（DEC-027）。
//
// 渠道真实成本 = 上游名义成本 × 本充值倍率。factor = 每 1 单位上游名义额度折合多少结算币种真实钱
// （已把汇率 + 充值优惠折进去）。账户级、无 model 维度。设计约束：数值不可改（改倍率靠「新建一条 + 关闭旧窗口」）；
// 同一 channel 的启用窗口不可重叠。
package channelrechargefactor

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

const (
	// StatusEnabled 表示充值倍率启用（参与结算派生成本）。
	StatusEnabled = "enabled"
	// StatusDisabled 表示充值倍率停用。
	StatusDisabled = "disabled"
)

// Store 定义渠道充值倍率管理所需的存储能力。
type Store interface {
	GetChannel(ctx context.Context, id int64) (sqlc.Channel, error)
	GetChannelRechargeFactor(ctx context.Context, id int64) (sqlc.ChannelRechargeFactor, error)
	ListChannelRechargeFactorsByChannel(ctx context.Context, channelID int64) ([]sqlc.ChannelRechargeFactor, error)
	ListEnabledChannelRechargeFactorWindows(ctx context.Context, arg sqlc.ListEnabledChannelRechargeFactorWindowsParams) ([]sqlc.ListEnabledChannelRechargeFactorWindowsRow, error)
	CreateChannelRechargeFactor(ctx context.Context, arg sqlc.CreateChannelRechargeFactorParams) (sqlc.ChannelRechargeFactor, error)
	UpdateChannelRechargeFactorWindow(ctx context.Context, arg sqlc.UpdateChannelRechargeFactorWindowParams) (sqlc.ChannelRechargeFactor, error)
}

// ChannelRechargeFactor 是 admin 视角的渠道充值倍率事实。
type ChannelRechargeFactor struct {
	ID            int64
	ChannelID     int64
	Factor        string
	Status        string
	EffectiveFrom time.Time
	EffectiveTo   *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CreateInput 是创建渠道充值倍率的入参。
type CreateInput struct {
	ChannelID     int64
	Factor        string
	Status        string
	EffectiveFrom time.Time
	EffectiveTo   *time.Time
}

// UpdateInput 是 PATCH 渠道充值倍率的入参：只改启停状态与生效结束时间；数值不可改。
type UpdateInput struct {
	ID          int64
	Status      string
	EffectiveTo *time.Time
}

// Service 编排渠道充值倍率读写。
type Service struct {
	store Store
}

// NewService 创建渠道充值倍率管理服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// List 列出某 channel 下全部充值倍率（含历史与停用）；channel 不存在返回 not_found。
func (s *Service) List(ctx context.Context, channelID int64) ([]ChannelRechargeFactor, error) {
	if channelID <= 0 {
		return nil, invalidArgument("channel_id", "channel id must be positive")
	}
	if _, err := s.store.GetChannel(ctx, channelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound("channel not found")
		}
		return nil, storeFailed(err, "load channel")
	}

	rows, err := s.store.ListChannelRechargeFactorsByChannel(ctx, channelID)
	if err != nil {
		return nil, storeFailed(err, "list channel recharge factors")
	}

	out := make([]ChannelRechargeFactor, 0, len(rows))
	for _, row := range rows {
		out = append(out, toChannelRechargeFactor(row))
	}

	return out, nil
}

// Create 创建一条渠道充值倍率：校验渠道存在、倍率合法、生效窗口不重叠。
func (s *Service) Create(ctx context.Context, in CreateInput) (ChannelRechargeFactor, error) {
	if in.ChannelID <= 0 {
		return ChannelRechargeFactor{}, invalidArgument("channel_id", "channel id must be positive")
	}
	if err := validateStatus(in.Status); err != nil {
		return ChannelRechargeFactor{}, err
	}
	if in.EffectiveFrom.IsZero() {
		return ChannelRechargeFactor{}, invalidArgument("effective_from", "effective_from is required")
	}
	if in.EffectiveTo != nil && !in.EffectiveTo.After(in.EffectiveFrom) {
		return ChannelRechargeFactor{}, invalidArgument("effective_to", "effective_to must be after effective_from")
	}

	factor, err := parseFactor(in.Factor)
	if err != nil {
		return ChannelRechargeFactor{}, err
	}

	if _, err := s.store.GetChannel(ctx, in.ChannelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelRechargeFactor{}, notFound("channel not found")
		}
		return ChannelRechargeFactor{}, storeFailed(err, "load channel")
	}

	if in.Status == StatusEnabled {
		if err := s.ensureNoOverlap(ctx, in.ChannelID, 0, in.EffectiveFrom, in.EffectiveTo); err != nil {
			return ChannelRechargeFactor{}, err
		}
	}

	row, err := s.store.CreateChannelRechargeFactor(ctx, sqlc.CreateChannelRechargeFactorParams{
		ChannelID:     in.ChannelID,
		Factor:        factor,
		Status:        in.Status,
		EffectiveFrom: tsParam(&in.EffectiveFrom),
		EffectiveTo:   tsParam(in.EffectiveTo),
	})
	if err != nil {
		return ChannelRechargeFactor{}, storeFailed(err, "create channel recharge factor")
	}

	return toChannelRechargeFactor(row), nil
}

// Update 调整窗口/启停：改 effective_to（关闭窗口）与 status；数值不可改。重新启用或延长窗口时复查重叠。
func (s *Service) Update(ctx context.Context, in UpdateInput) (ChannelRechargeFactor, error) {
	if in.ID <= 0 {
		return ChannelRechargeFactor{}, invalidArgument("id", "id must be positive")
	}
	if err := validateStatus(in.Status); err != nil {
		return ChannelRechargeFactor{}, err
	}

	existing, err := s.store.GetChannelRechargeFactor(ctx, in.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelRechargeFactor{}, notFound("channel recharge factor not found")
		}
		return ChannelRechargeFactor{}, storeFailed(err, "load channel recharge factor")
	}

	if in.EffectiveTo != nil && !in.EffectiveTo.After(existing.EffectiveFrom.Time) {
		return ChannelRechargeFactor{}, invalidArgument("effective_to", "effective_to must be after effective_from")
	}

	if in.Status == StatusEnabled {
		if err := s.ensureNoOverlap(ctx, existing.ChannelID, existing.ID, existing.EffectiveFrom.Time, in.EffectiveTo); err != nil {
			return ChannelRechargeFactor{}, err
		}
	}

	row, err := s.store.UpdateChannelRechargeFactorWindow(ctx, sqlc.UpdateChannelRechargeFactorWindowParams{
		ID:          in.ID,
		Status:      in.Status,
		EffectiveTo: tsParam(in.EffectiveTo),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelRechargeFactor{}, notFound("channel recharge factor not found")
		}
		return ChannelRechargeFactor{}, storeFailed(err, "update channel recharge factor")
	}

	return toChannelRechargeFactor(row), nil
}

// ensureNoOverlap 校验目标窗口与同一 channel 现有启用窗口不重叠（半开区间 [from, to)）。
func (s *Service) ensureNoOverlap(ctx context.Context, channelID, excludeID int64, from time.Time, to *time.Time) error {
	windows, err := s.store.ListEnabledChannelRechargeFactorWindows(ctx, sqlc.ListEnabledChannelRechargeFactorWindowsParams{
		ChannelID: channelID,
		ExcludeID: excludeID,
	})
	if err != nil {
		return storeFailed(err, "list enabled channel recharge factor windows")
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
				failure.WithMessage("effective window overlaps an existing enabled channel recharge factor"),
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

func toChannelRechargeFactor(c sqlc.ChannelRechargeFactor) ChannelRechargeFactor {
	return ChannelRechargeFactor{
		ID:            c.ID,
		ChannelID:     c.ChannelID,
		Factor:        numericString(c.Factor),
		Status:        c.Status,
		EffectiveFrom: c.EffectiveFrom.Time,
		EffectiveTo:   timePtr(c.EffectiveTo),
		CreatedAt:     c.CreatedAt.Time,
		UpdatedAt:     c.UpdatedAt.Time,
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

// parseFactor 解析充值倍率：非负十进制字符串 → pgtype.Numeric。
func parseFactor(raw string) (pgtype.Numeric, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return pgtype.Numeric{}, invalidArgument("factor", "is required")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || strings.ContainsAny(s, "eE") {
		return pgtype.Numeric{}, invalidArgument("factor", "must be a non-negative decimal")
	}
	if r.Sign() < 0 {
		return pgtype.Numeric{}, invalidArgument("factor", "must be a non-negative decimal")
	}
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		return pgtype.Numeric{}, invalidArgument("factor", "invalid decimal")
	}
	return n, nil
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
	if !n.Valid || n.NaN || n.InfinityModifier != pgtype.Finite || n.Int == nil {
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
