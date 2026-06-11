// Package costprice 编排 admin 管理端的 channel 成本价（channel_cost_prices）读写。
//
// 成本价是时间分片的：同一 channel/model 在不同时间窗口可有不同价格，settle 时按生效时间命中唯一一条。
// 设计约束（见 ADMIN_MODULES_DRAFT M4）：
//   - 金额只填明确数值、绝不用 float；DTO 层用十进制字符串承载，避免精度丢失。
//   - 价格不可删：账务（cost_snapshots）按 (id, channel_id, model_id) 外键引用历史价；改价靠「新建一条 + 关闭旧窗口」。
//   - 同一 channel/model 的启用窗口不可重叠，否则 settle 选价有歧义。
package costprice

import (
	"context"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

const (
	// StatusEnabled 表示成本价启用（参与 settle 选价）。
	StatusEnabled = "enabled"
	// StatusDisabled 表示成本价停用。
	StatusDisabled = "disabled"

	// PricingUnitPer1MTokens 是当前唯一支持的计价单位。
	PricingUnitPer1MTokens = "per_1m_tokens"
)

// moneyPattern 限定金额为非负十进制（不带符号即保证 >= 0），不接受科学计数法。
var moneyPattern = regexp.MustCompile(`^\d+(\.\d+)?$`)

// Store 定义成本价管理所需的存储能力。
type Store interface {
	GetChannel(ctx context.Context, id int64) (sqlc.Channel, error)
	GetChannelModel(ctx context.Context, arg sqlc.GetChannelModelParams) (sqlc.ChannelModel, error)
	GetChannelCostPrice(ctx context.Context, id int64) (sqlc.ChannelCostPrice, error)
	ListChannelCostPricesByChannel(ctx context.Context, channelID int64) ([]sqlc.ListChannelCostPricesByChannelRow, error)
	ListEnabledChannelCostPriceWindows(ctx context.Context, arg sqlc.ListEnabledChannelCostPriceWindowsParams) ([]sqlc.ListEnabledChannelCostPriceWindowsRow, error)
	CreateChannelCostPrice(ctx context.Context, arg sqlc.CreateChannelCostPriceParams) (sqlc.ChannelCostPrice, error)
	UpdateChannelCostPriceWindow(ctx context.Context, arg sqlc.UpdateChannelCostPriceWindowParams) (sqlc.ChannelCostPrice, error)
}

// CostPrice 是 admin 视角的成本价事实；金额以十进制字符串承载，可空项用 *string。
type CostPrice struct {
	ID                    int64
	ChannelID             int64
	ModelID               int64
	ModelExternalID       string
	ModelDisplayName      string
	Currency              string
	PricingUnit           string
	UncachedInputCost     string
	CacheReadInputCost    *string
	CacheWrite5mInputCost *string
	CacheWrite1hInputCost *string
	OutputCost            string
	ReasoningOutputCost   *string
	Status                string
	EffectiveFrom         time.Time
	EffectiveTo           *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// CreateInput 是创建成本价的入参；金额为十进制字符串，可空项传 nil 表示 SQL NULL。
type CreateInput struct {
	ChannelID             int64
	ModelID               int64
	Currency              string
	PricingUnit           string
	UncachedInputCost     string
	CacheReadInputCost    *string
	CacheWrite5mInputCost *string
	CacheWrite1hInputCost *string
	OutputCost            string
	ReasoningOutputCost   *string
	Status                string
	EffectiveFrom         time.Time
	EffectiveTo           *time.Time
}

// UpdateInput 是 PATCH 成本价的入参：只改启停状态与生效结束时间（关闭窗口）；金额不可改。
type UpdateInput struct {
	ID          int64
	Status      string
	EffectiveTo *time.Time
}

// Service 编排成本价读写。
type Service struct {
	store Store
}

// NewService 创建成本价管理服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// List 列出某 channel 下全部成本价（含历史与停用）；channel 不存在返回 not_found。
func (s *Service) List(ctx context.Context, channelID int64) ([]CostPrice, error) {
	if channelID <= 0 {
		return nil, invalidArgument("channel_id", "channel id must be positive")
	}
	if _, err := s.store.GetChannel(ctx, channelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound("channel not found")
		}
		return nil, storeFailed(err, "load channel")
	}

	rows, err := s.store.ListChannelCostPricesByChannel(ctx, channelID)
	if err != nil {
		return nil, storeFailed(err, "list channel cost prices")
	}

	prices := make([]CostPrice, 0, len(rows))
	for _, row := range rows {
		prices = append(prices, toCostPriceFromRow(row))
	}

	return prices, nil
}

// Create 创建一条成本价：校验绑定存在、金额合法、生效窗口与现有启用窗口不重叠。
func (s *Service) Create(ctx context.Context, in CreateInput) (CostPrice, error) {
	if in.ChannelID <= 0 {
		return CostPrice{}, invalidArgument("channel_id", "channel id must be positive")
	}
	if in.ModelID <= 0 {
		return CostPrice{}, invalidArgument("model_id", "model_id must be positive")
	}
	currency := strings.TrimSpace(in.Currency)
	if currency == "" {
		return CostPrice{}, invalidArgument("currency", "currency is required")
	}
	if in.PricingUnit != PricingUnitPer1MTokens {
		return CostPrice{}, invalidArgument("pricing_unit", "pricing_unit must be \"per_1m_tokens\"")
	}
	if err := validateStatus(in.Status); err != nil {
		return CostPrice{}, err
	}
	if in.EffectiveFrom.IsZero() {
		return CostPrice{}, invalidArgument("effective_from", "effective_from is required")
	}
	if in.EffectiveTo != nil && !in.EffectiveTo.After(in.EffectiveFrom) {
		return CostPrice{}, invalidArgument("effective_to", "effective_to must be after effective_from")
	}

	uncached, err := parseMoney("uncached_input_cost", in.UncachedInputCost)
	if err != nil {
		return CostPrice{}, err
	}
	output, err := parseMoney("output_cost", in.OutputCost)
	if err != nil {
		return CostPrice{}, err
	}
	cacheRead, err := parseOptionalMoney("cache_read_input_cost", in.CacheReadInputCost)
	if err != nil {
		return CostPrice{}, err
	}
	cacheWrite5m, err := parseOptionalMoney("cache_write_5m_input_cost", in.CacheWrite5mInputCost)
	if err != nil {
		return CostPrice{}, err
	}
	cacheWrite1h, err := parseOptionalMoney("cache_write_1h_input_cost", in.CacheWrite1hInputCost)
	if err != nil {
		return CostPrice{}, err
	}
	reasoning, err := parseOptionalMoney("reasoning_output_cost", in.ReasoningOutputCost)
	if err != nil {
		return CostPrice{}, err
	}

	// 成本价必须挂在已存在的 channel↔model 绑定上（DB 也有同名外键，这里给清晰 400）。
	if _, err := s.store.GetChannelModel(ctx, sqlc.GetChannelModelParams{
		ChannelID: in.ChannelID,
		ModelID:   in.ModelID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CostPrice{}, invalidArgument("model_id", "channel is not bound to this model")
		}
		return CostPrice{}, storeFailed(err, "load channel model binding")
	}

	if in.Status == StatusEnabled {
		if err := s.ensureNoOverlap(ctx, in.ChannelID, in.ModelID, 0, in.EffectiveFrom, in.EffectiveTo); err != nil {
			return CostPrice{}, err
		}
	}

	row, err := s.store.CreateChannelCostPrice(ctx, sqlc.CreateChannelCostPriceParams{
		ChannelID:             in.ChannelID,
		ModelID:               in.ModelID,
		Currency:              currency,
		PricingUnit:           in.PricingUnit,
		UncachedInputCost:     uncached,
		CacheReadInputCost:    cacheRead,
		CacheWrite5mInputCost: cacheWrite5m,
		CacheWrite1hInputCost: cacheWrite1h,
		OutputCost:            output,
		ReasoningOutputCost:   reasoning,
		Status:                in.Status,
		EffectiveFrom:         tsParam(&in.EffectiveFrom),
		EffectiveTo:           tsParam(in.EffectiveTo),
	})
	if err != nil {
		return CostPrice{}, storeFailed(err, "create channel cost price")
	}

	return toCostPrice(row), nil
}

// Update 调整窗口/启停：改 effective_to（关闭窗口）与 status；重新启用或延长窗口时复查重叠。
func (s *Service) Update(ctx context.Context, in UpdateInput) (CostPrice, error) {
	if in.ID <= 0 {
		return CostPrice{}, invalidArgument("id", "id must be positive")
	}
	if err := validateStatus(in.Status); err != nil {
		return CostPrice{}, err
	}

	existing, err := s.store.GetChannelCostPrice(ctx, in.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CostPrice{}, notFound("channel cost price not found")
		}
		return CostPrice{}, storeFailed(err, "load channel cost price")
	}

	if in.EffectiveTo != nil && !in.EffectiveTo.After(existing.EffectiveFrom.Time) {
		return CostPrice{}, invalidArgument("effective_to", "effective_to must be after effective_from")
	}

	// 启用态的窗口若被改动（结束时间），需排除自身复查重叠。停用的不参与选价，跳过。
	if in.Status == StatusEnabled {
		if err := s.ensureNoOverlap(ctx, existing.ChannelID, existing.ModelID, existing.ID, existing.EffectiveFrom.Time, in.EffectiveTo); err != nil {
			return CostPrice{}, err
		}
	}

	row, err := s.store.UpdateChannelCostPriceWindow(ctx, sqlc.UpdateChannelCostPriceWindowParams{
		ID:          in.ID,
		Status:      in.Status,
		EffectiveTo: tsParam(in.EffectiveTo),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CostPrice{}, notFound("channel cost price not found")
		}
		return CostPrice{}, storeFailed(err, "update channel cost price")
	}

	return toCostPrice(row), nil
}

// ensureNoOverlap 校验目标窗口与同一 channel/model 现有启用窗口不重叠（半开区间 [from, to)）。
func (s *Service) ensureNoOverlap(ctx context.Context, channelID, modelID, excludeID int64, from time.Time, to *time.Time) error {
	windows, err := s.store.ListEnabledChannelCostPriceWindows(ctx, sqlc.ListEnabledChannelCostPriceWindowsParams{
		ChannelID: channelID,
		ModelID:   modelID,
		ExcludeID: excludeID,
	})
	if err != nil {
		return storeFailed(err, "list enabled cost price windows")
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
				failure.WithMessage("effective window overlaps an existing enabled cost price"),
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

func toCostPrice(c sqlc.ChannelCostPrice) CostPrice {
	return CostPrice{
		ID:                    c.ID,
		ChannelID:             c.ChannelID,
		ModelID:               c.ModelID,
		Currency:              c.Currency,
		PricingUnit:           c.PricingUnit,
		UncachedInputCost:     numericString(c.UncachedInputCost),
		CacheReadInputCost:    numericPtr(c.CacheReadInputCost),
		CacheWrite5mInputCost: numericPtr(c.CacheWrite5mInputCost),
		CacheWrite1hInputCost: numericPtr(c.CacheWrite1hInputCost),
		OutputCost:            numericString(c.OutputCost),
		ReasoningOutputCost:   numericPtr(c.ReasoningOutputCost),
		Status:                c.Status,
		EffectiveFrom:         c.EffectiveFrom.Time,
		EffectiveTo:           timePtr(c.EffectiveTo),
		CreatedAt:             c.CreatedAt.Time,
		UpdatedAt:             c.UpdatedAt.Time,
	}
}

func toCostPriceFromRow(c sqlc.ListChannelCostPricesByChannelRow) CostPrice {
	return CostPrice{
		ID:                    c.ID,
		ChannelID:             c.ChannelID,
		ModelID:               c.ModelID,
		ModelExternalID:       c.ModelExternalID,
		ModelDisplayName:      c.ModelDisplayName,
		Currency:              c.Currency,
		PricingUnit:           c.PricingUnit,
		UncachedInputCost:     numericString(c.UncachedInputCost),
		CacheReadInputCost:    numericPtr(c.CacheReadInputCost),
		CacheWrite5mInputCost: numericPtr(c.CacheWrite5mInputCost),
		CacheWrite1hInputCost: numericPtr(c.CacheWrite1hInputCost),
		OutputCost:            numericString(c.OutputCost),
		ReasoningOutputCost:   numericPtr(c.ReasoningOutputCost),
		Status:                c.Status,
		EffectiveFrom:         c.EffectiveFrom.Time,
		EffectiveTo:           timePtr(c.EffectiveTo),
		CreatedAt:             c.CreatedAt.Time,
		UpdatedAt:             c.UpdatedAt.Time,
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

// parseMoney 解析必填金额：非负十进制字符串 → pgtype.Numeric。
func parseMoney(field, raw string) (pgtype.Numeric, error) {
	s := strings.TrimSpace(raw)
	if !moneyPattern.MatchString(s) {
		return pgtype.Numeric{}, invalidArgument(field, "must be a non-negative decimal amount")
	}
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		return pgtype.Numeric{}, invalidArgument(field, "invalid decimal amount")
	}
	return n, nil
}

// parseOptionalMoney 解析可选金额：nil/空串 → SQL NULL；否则按必填规则解析。
func parseOptionalMoney(field string, raw *string) (pgtype.Numeric, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return pgtype.Numeric{Valid: false}, nil
	}
	return parseMoney(field, *raw)
}

// tsParam 把可选时间转成 pgtype.Timestamptz；nil → SQL NULL。
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
	if s := numericPtr(n); s != nil {
		return *s
	}
	return "0"
}

// numericPtr 把 NUMERIC 精确格式化为十进制字符串（不用 float）；NULL/NaN/Inf 返回 nil。
func numericPtr(n pgtype.Numeric) *string {
	if !n.Valid || n.NaN || n.InfinityModifier != pgtype.Finite {
		return nil
	}
	if n.Int == nil {
		zero := "0"
		return &zero
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

func storeFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}
