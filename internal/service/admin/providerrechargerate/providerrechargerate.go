// Package providerrechargerate 编排 admin 管理端的服务商充值汇率（provider_recharge_rates）读写。
//
// 充值汇率 = 实际支付的服务商币种金额 ÷ 到账的 USD 名义额度，归属服务商级，其下所有渠道共享。
// 渠道真实成本（倍率路径）= 模型基准价 × 渠道价格倍率 × 本充值汇率。
// 设计约束：数值不可改（改汇率靠「新建一条 + 关闭旧窗口」）；同一 provider 的启用窗口不可重叠；
// rate 仅允许 > 0（D-04）；provider_currency 由服务端从 providers.currency 快照写入。
package providerrechargerate

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
	// StatusEnabled 表示充值汇率启用（参与结算派生成本、渠道启用与路由候选闸门）。
	StatusEnabled = "enabled"
	// StatusDisabled 表示充值汇率停用。
	StatusDisabled = "disabled"

	// SourceManual 表示数值由管理员手工输入。
	SourceManual = "manual"
	// SourceCalculated 表示数值由「支付金额 ÷ USD 名义额度」助手算出（后端仍按同一规则校验）。
	SourceCalculated = "calculated"
)

// Store 定义服务商充值汇率管理所需的存储能力。
type Store interface {
	GetProvider(ctx context.Context, id int64) (sqlc.Provider, error)
	GetProviderRechargeRate(ctx context.Context, id int64) (sqlc.ProviderRechargeRate, error)
	ListProviderRechargeRatesByProvider(ctx context.Context, providerID int64) ([]sqlc.ProviderRechargeRate, error)
	ListEnabledProviderRechargeRateWindows(ctx context.Context, arg sqlc.ListEnabledProviderRechargeRateWindowsParams) ([]sqlc.ListEnabledProviderRechargeRateWindowsRow, error)
	CreateProviderRechargeRate(ctx context.Context, arg sqlc.CreateProviderRechargeRateParams) (sqlc.ProviderRechargeRate, error)
	UpdateProviderRechargeRateWindow(ctx context.Context, arg sqlc.UpdateProviderRechargeRateWindowParams) (sqlc.ProviderRechargeRate, error)
	FindActiveProviderRechargeRate(ctx context.Context, arg sqlc.FindActiveProviderRechargeRateParams) (sqlc.ProviderRechargeRate, error)
}

// ProviderRechargeRate 是 admin 视角的服务商充值汇率事实。
type ProviderRechargeRate struct {
	ID               int64
	ProviderID       int64
	ProviderCurrency string
	NominalCurrency  string
	Rate             string
	Status           string
	Source           string
	Reason           string
	CreatedBy        string
	EffectiveFrom    time.Time
	EffectiveTo      *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// CreateInput 是创建服务商充值汇率的入参。
type CreateInput struct {
	ProviderID    int64
	Rate          string
	Status        string
	Source        string
	Reason        string
	CreatedBy     string
	EffectiveFrom time.Time
	EffectiveTo   *time.Time
}

// UpdateInput 是 PATCH 服务商充值汇率的入参：只改启停状态与生效结束时间；数值不可改。
type UpdateInput struct {
	ID          int64
	Status      string
	EffectiveTo *time.Time
}

// Service 编排服务商充值汇率读写。
type Service struct {
	store Store
}

// NewService 创建服务商充值汇率管理服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// List 列出某 provider 下全部充值汇率版本（含历史与停用）；provider 不存在返回 not_found。
func (s *Service) List(ctx context.Context, providerID int64) ([]ProviderRechargeRate, error) {
	if providerID <= 0 {
		return nil, invalidArgument("provider_id", "provider id must be positive")
	}
	if _, err := s.store.GetProvider(ctx, providerID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound("provider not found")
		}
		return nil, storeFailed(err, "load provider")
	}

	rows, err := s.store.ListProviderRechargeRatesByProvider(ctx, providerID)
	if err != nil {
		return nil, storeFailed(err, "list provider recharge rates")
	}

	out := make([]ProviderRechargeRate, 0, len(rows))
	for _, row := range rows {
		out = append(out, toProviderRechargeRate(row))
	}

	return out, nil
}

// Create 创建一条服务商充值汇率：校验 provider 存在、汇率合法（> 0）、生效窗口不重叠；
// provider_currency 由服务端从 provider 主数据快照写入，客户端不能指定。
func (s *Service) Create(ctx context.Context, in CreateInput) (ProviderRechargeRate, error) {
	if in.ProviderID <= 0 {
		return ProviderRechargeRate{}, invalidArgument("provider_id", "provider id must be positive")
	}
	if err := validateStatus(in.Status); err != nil {
		return ProviderRechargeRate{}, err
	}
	source := in.Source
	if source == "" {
		source = SourceManual
	}
	if source != SourceManual && source != SourceCalculated {
		return ProviderRechargeRate{}, invalidArgument("source", "source must be \"manual\" or \"calculated\"")
	}
	if in.EffectiveFrom.IsZero() {
		return ProviderRechargeRate{}, invalidArgument("effective_from", "effective_from is required")
	}
	if in.EffectiveTo != nil && !in.EffectiveTo.After(in.EffectiveFrom) {
		return ProviderRechargeRate{}, invalidArgument("effective_to", "effective_to must be after effective_from")
	}

	rate, err := parseRate(in.Rate)
	if err != nil {
		return ProviderRechargeRate{}, err
	}

	providerRow, err := s.store.GetProvider(ctx, in.ProviderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderRechargeRate{}, notFound("provider not found")
		}
		return ProviderRechargeRate{}, storeFailed(err, "load provider")
	}

	if in.Status == StatusEnabled {
		if err := s.ensureNoOverlap(ctx, in.ProviderID, 0, in.EffectiveFrom, in.EffectiveTo); err != nil {
			return ProviderRechargeRate{}, err
		}
	}

	row, err := s.store.CreateProviderRechargeRate(ctx, sqlc.CreateProviderRechargeRateParams{
		ProviderID:       in.ProviderID,
		ProviderCurrency: providerRow.Currency,
		Rate:             rate,
		Status:           in.Status,
		Source:           source,
		Reason:           textNarg(in.Reason),
		CreatedBy:        textNarg(in.CreatedBy),
		EffectiveFrom:    tsParam(&in.EffectiveFrom),
		EffectiveTo:      tsParam(in.EffectiveTo),
	})
	if err != nil {
		return ProviderRechargeRate{}, storeFailed(err, "create provider recharge rate")
	}

	return toProviderRechargeRate(row), nil
}

// Update 调整窗口/启停：改 effective_to（关闭窗口）与 status；数值不可改。重新启用或延长窗口时复查重叠。
func (s *Service) Update(ctx context.Context, in UpdateInput) (ProviderRechargeRate, error) {
	if in.ID <= 0 {
		return ProviderRechargeRate{}, invalidArgument("id", "id must be positive")
	}
	if err := validateStatus(in.Status); err != nil {
		return ProviderRechargeRate{}, err
	}

	existing, err := s.store.GetProviderRechargeRate(ctx, in.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderRechargeRate{}, notFound("provider recharge rate not found")
		}
		return ProviderRechargeRate{}, storeFailed(err, "load provider recharge rate")
	}

	if in.EffectiveTo != nil && !in.EffectiveTo.After(existing.EffectiveFrom.Time) {
		return ProviderRechargeRate{}, invalidArgument("effective_to", "effective_to must be after effective_from")
	}

	if in.Status == StatusEnabled {
		if err := s.ensureNoOverlap(ctx, existing.ProviderID, existing.ID, existing.EffectiveFrom.Time, in.EffectiveTo); err != nil {
			return ProviderRechargeRate{}, err
		}
	}

	row, err := s.store.UpdateProviderRechargeRateWindow(ctx, sqlc.UpdateProviderRechargeRateWindowParams{
		ID:          in.ID,
		Status:      in.Status,
		EffectiveTo: tsParam(in.EffectiveTo),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderRechargeRate{}, notFound("provider recharge rate not found")
		}
		return ProviderRechargeRate{}, storeFailed(err, "update provider recharge rate")
	}

	return toProviderRechargeRate(row), nil
}

// FindActive 返回 provider 在指定时刻生效的充值汇率；未配置时返回 ok=false（供渠道启用闸门等调用方判定）。
func (s *Service) FindActive(ctx context.Context, providerID int64, at time.Time) (ProviderRechargeRate, bool, error) {
	row, err := s.store.FindActiveProviderRechargeRate(ctx, sqlc.FindActiveProviderRechargeRateParams{
		ProviderID: providerID,
		AtTime:     pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderRechargeRate{}, false, nil
		}
		return ProviderRechargeRate{}, false, storeFailed(err, "find active provider recharge rate")
	}
	return toProviderRechargeRate(row), true, nil
}

// ensureNoOverlap 校验目标窗口与同一 provider 现有启用窗口不重叠（半开区间 [from, to)）。
func (s *Service) ensureNoOverlap(ctx context.Context, providerID, excludeID int64, from time.Time, to *time.Time) error {
	windows, err := s.store.ListEnabledProviderRechargeRateWindows(ctx, sqlc.ListEnabledProviderRechargeRateWindowsParams{
		ProviderID: providerID,
		ExcludeID:  excludeID,
	})
	if err != nil {
		return storeFailed(err, "list enabled provider recharge rate windows")
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
				failure.WithMessage("effective window overlaps an existing enabled provider recharge rate"),
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

func toProviderRechargeRate(r sqlc.ProviderRechargeRate) ProviderRechargeRate {
	return ProviderRechargeRate{
		ID:               r.ID,
		ProviderID:       r.ProviderID,
		ProviderCurrency: r.ProviderCurrency,
		NominalCurrency:  r.NominalCurrency,
		Rate:             numericString(r.Rate),
		Status:           r.Status,
		Source:           r.Source,
		Reason:           textOrEmpty(r.Reason),
		CreatedBy:        textOrEmpty(r.CreatedBy),
		EffectiveFrom:    r.EffectiveFrom.Time,
		EffectiveTo:      timePtr(r.EffectiveTo),
		CreatedAt:        r.CreatedAt.Time,
		UpdatedAt:        r.UpdatedAt.Time,
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

// parseRate 解析充值汇率：大于 0 的有限十进制字符串 → pgtype.Numeric（D-04：不用 0 表达赠送额度）。
func parseRate(raw string) (pgtype.Numeric, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return pgtype.Numeric{}, invalidArgument("rate", "is required")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || strings.ContainsAny(s, "eE") {
		return pgtype.Numeric{}, invalidArgument("rate", "must be a positive decimal")
	}
	if r.Sign() <= 0 {
		return pgtype.Numeric{}, invalidArgument("rate", "must be a positive decimal")
	}
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		return pgtype.Numeric{}, invalidArgument("rate", "invalid decimal")
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

func textNarg(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func textOrEmpty(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
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
