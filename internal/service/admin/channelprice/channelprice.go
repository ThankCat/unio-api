// Package channelprice 编排 admin 管理端的渠道-模型价（channel_prices）读写（阶段 15）。
//
// channel_prices 一行同时含「客户售价（必填）+ 上游成本价（可空）」，毛利在录入期即被守卫保证非负。
// 设计约束：
//   - 金额只填明确数值、绝不用 float；DTO 层用十进制字符串承载，避免精度丢失。
//   - 价格不可删：账务（price_snapshots/cost_snapshots）按外键引用历史价；改价靠「新建一条 + 关闭旧窗口」。
//   - 同一 channel/model 的启用窗口不可重叠，否则结算选价有歧义。
//   - 录入守卫：任一分项「售价 < 成本」直接拦下（DB ck_channel_prices_margin 硬拦 + 本层可读报错）。
package channelprice

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

const (
	// StatusEnabled 表示价格启用（参与结算选价）。
	StatusEnabled = "enabled"
	// StatusDisabled 表示价格停用。
	StatusDisabled = "disabled"

	// PricingUnitPer1MTokens 是当前唯一支持的计价单位。
	PricingUnitPer1MTokens = "per_1m_tokens"
)

// Store 定义渠道-模型价管理所需的存储能力。
type Store interface {
	GetChannel(ctx context.Context, id int64) (sqlc.Channel, error)
	GetChannelModel(ctx context.Context, arg sqlc.GetChannelModelParams) (sqlc.ChannelModel, error)
	GetChannelPrice(ctx context.Context, id int64) (sqlc.ChannelPrice, error)
	ListChannelPricesByChannel(ctx context.Context, channelID int64) ([]sqlc.ListChannelPricesByChannelRow, error)
	ListEnabledChannelPriceWindows(ctx context.Context, arg sqlc.ListEnabledChannelPriceWindowsParams) ([]sqlc.ListEnabledChannelPriceWindowsRow, error)
	CreateChannelPrice(ctx context.Context, arg sqlc.CreateChannelPriceParams) (sqlc.ChannelPrice, error)
	UpdateChannelPriceWindow(ctx context.Context, arg sqlc.UpdateChannelPriceWindowParams) (sqlc.ChannelPrice, error)
}

// ChannelPrice 是 admin 视角的渠道-模型价事实；金额以十进制字符串承载，可空项用 *string。
type ChannelPrice struct {
	ID                     int64
	ChannelID              int64
	ModelID                int64
	ModelExternalID        string
	ModelDisplayName       string
	Currency               string
	PricingUnit            string
	UncachedInputPrice     string
	CacheReadInputPrice    *string
	CacheWrite5mInputPrice *string
	CacheWrite1hInputPrice *string
	OutputPrice            string
	ReasoningOutputPrice   *string
	UncachedInputCost      *string
	CacheReadInputCost     *string
	CacheWrite5mInputCost  *string
	CacheWrite1hInputCost  *string
	OutputCost             *string
	ReasoningOutputCost    *string
	Status                 string
	EffectiveFrom          time.Time
	EffectiveTo            *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// CreateInput 是创建渠道-模型价的入参；售价必填、成本可空，金额为十进制字符串。
type CreateInput struct {
	ChannelID              int64
	ModelID                int64
	Currency               string
	PricingUnit            string
	UncachedInputPrice     string
	CacheReadInputPrice    *string
	CacheWrite5mInputPrice *string
	CacheWrite1hInputPrice *string
	OutputPrice            string
	ReasoningOutputPrice   *string
	UncachedInputCost      *string
	CacheReadInputCost     *string
	CacheWrite5mInputCost  *string
	CacheWrite1hInputCost  *string
	OutputCost             *string
	ReasoningOutputCost    *string
	Status                 string
	EffectiveFrom          time.Time
	EffectiveTo            *time.Time
}

// UpdateInput 是 PATCH 渠道-模型价的入参：只改启停状态与生效结束时间（关闭窗口）；金额不可改。
type UpdateInput struct {
	ID          int64
	Status      string
	EffectiveTo *time.Time
}

// Service 编排渠道-模型价读写。
type Service struct {
	store Store
}

// NewService 创建渠道-模型价管理服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// List 列出某 channel 下全部渠道-模型价（含历史与停用）；channel 不存在返回 not_found。
func (s *Service) List(ctx context.Context, channelID int64) ([]ChannelPrice, error) {
	if channelID <= 0 {
		return nil, invalidArgument("channel_id", "channel id must be positive")
	}
	if _, err := s.store.GetChannel(ctx, channelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound("channel not found")
		}
		return nil, storeFailed(err, "load channel")
	}

	rows, err := s.store.ListChannelPricesByChannel(ctx, channelID)
	if err != nil {
		return nil, storeFailed(err, "list channel prices")
	}

	prices := make([]ChannelPrice, 0, len(rows))
	for _, row := range rows {
		prices = append(prices, toChannelPriceFromRow(row))
	}

	return prices, nil
}

// Create 创建一条渠道-模型价：校验绑定存在、金额合法、毛利非负、生效窗口不重叠。
func (s *Service) Create(ctx context.Context, in CreateInput) (ChannelPrice, error) {
	if in.ChannelID <= 0 {
		return ChannelPrice{}, invalidArgument("channel_id", "channel id must be positive")
	}
	if in.ModelID <= 0 {
		return ChannelPrice{}, invalidArgument("model_id", "model_id must be positive")
	}
	currency := strings.TrimSpace(in.Currency)
	if currency == "" {
		return ChannelPrice{}, invalidArgument("currency", "currency is required")
	}
	if in.PricingUnit != PricingUnitPer1MTokens {
		return ChannelPrice{}, invalidArgument("pricing_unit", "pricing_unit must be \"per_1m_tokens\"")
	}
	if err := validateStatus(in.Status); err != nil {
		return ChannelPrice{}, err
	}
	if in.EffectiveFrom.IsZero() {
		return ChannelPrice{}, invalidArgument("effective_from", "effective_from is required")
	}
	if in.EffectiveTo != nil && !in.EffectiveTo.After(in.EffectiveFrom) {
		return ChannelPrice{}, invalidArgument("effective_to", "effective_to must be after effective_from")
	}

	amounts, err := parseChannelPriceAmounts(in)
	if err != nil {
		return ChannelPrice{}, err
	}
	if err := ensureNonNegativeMargin(in); err != nil {
		return ChannelPrice{}, err
	}

	// 价格必须挂在已存在的 channel↔model 绑定上（DB 也有同名外键，这里给清晰 400）。
	if _, err := s.store.GetChannelModel(ctx, sqlc.GetChannelModelParams{
		ChannelID: in.ChannelID,
		ModelID:   in.ModelID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelPrice{}, invalidArgument("model_id", "channel is not bound to this model")
		}
		return ChannelPrice{}, storeFailed(err, "load channel model binding")
	}

	if in.Status == StatusEnabled {
		if err := s.ensureNoOverlap(ctx, in.ChannelID, in.ModelID, 0, in.EffectiveFrom, in.EffectiveTo); err != nil {
			return ChannelPrice{}, err
		}
	}

	row, err := s.store.CreateChannelPrice(ctx, sqlc.CreateChannelPriceParams{
		ChannelID:              in.ChannelID,
		ModelID:                in.ModelID,
		Currency:               currency,
		PricingUnit:            in.PricingUnit,
		UncachedInputPrice:     amounts.uncachedInputPrice,
		CacheReadInputPrice:    amounts.cacheReadInputPrice,
		CacheWrite5mInputPrice: amounts.cacheWrite5mInputPrice,
		CacheWrite1hInputPrice: amounts.cacheWrite1hInputPrice,
		OutputPrice:            amounts.outputPrice,
		ReasoningOutputPrice:   amounts.reasoningOutputPrice,
		UncachedInputCost:      amounts.uncachedInputCost,
		CacheReadInputCost:     amounts.cacheReadInputCost,
		CacheWrite5mInputCost:  amounts.cacheWrite5mInputCost,
		CacheWrite1hInputCost:  amounts.cacheWrite1hInputCost,
		OutputCost:             amounts.outputCost,
		ReasoningOutputCost:    amounts.reasoningOutputCost,
		Status:                 in.Status,
		EffectiveFrom:          tsParam(&in.EffectiveFrom),
		EffectiveTo:            tsParam(in.EffectiveTo),
	})
	if err != nil {
		// DB 守卫兜底：极少数并发/绕过场景下 service 校验通过但 DB CHECK 仍拦下，给可读错误。
		if isCheckViolation(err, "ck_channel_prices_margin") {
			return ChannelPrice{}, marginViolation("sale price must not be below cost for any component")
		}
		return ChannelPrice{}, storeFailed(err, "create channel price")
	}

	return toChannelPrice(row), nil
}

// Update 调整窗口/启停：改 effective_to（关闭窗口）与 status；金额不可改。重新启用或延长窗口时复查重叠。
func (s *Service) Update(ctx context.Context, in UpdateInput) (ChannelPrice, error) {
	if in.ID <= 0 {
		return ChannelPrice{}, invalidArgument("id", "id must be positive")
	}
	if err := validateStatus(in.Status); err != nil {
		return ChannelPrice{}, err
	}

	existing, err := s.store.GetChannelPrice(ctx, in.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelPrice{}, notFound("channel price not found")
		}
		return ChannelPrice{}, storeFailed(err, "load channel price")
	}

	if in.EffectiveTo != nil && !in.EffectiveTo.After(existing.EffectiveFrom.Time) {
		return ChannelPrice{}, invalidArgument("effective_to", "effective_to must be after effective_from")
	}

	if in.Status == StatusEnabled {
		if err := s.ensureNoOverlap(ctx, existing.ChannelID, existing.ModelID, existing.ID, existing.EffectiveFrom.Time, in.EffectiveTo); err != nil {
			return ChannelPrice{}, err
		}
	}

	row, err := s.store.UpdateChannelPriceWindow(ctx, sqlc.UpdateChannelPriceWindowParams{
		ID:          in.ID,
		Status:      in.Status,
		EffectiveTo: tsParam(in.EffectiveTo),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelPrice{}, notFound("channel price not found")
		}
		return ChannelPrice{}, storeFailed(err, "update channel price")
	}

	return toChannelPrice(row), nil
}

// ensureNoOverlap 校验目标窗口与同一 channel/model 现有启用窗口不重叠（半开区间 [from, to)）。
func (s *Service) ensureNoOverlap(ctx context.Context, channelID, modelID, excludeID int64, from time.Time, to *time.Time) error {
	windows, err := s.store.ListEnabledChannelPriceWindows(ctx, sqlc.ListEnabledChannelPriceWindowsParams{
		ChannelID: channelID,
		ModelID:   modelID,
		ExcludeID: excludeID,
	})
	if err != nil {
		return storeFailed(err, "list enabled channel price windows")
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
				failure.WithMessage("effective window overlaps an existing enabled channel price"),
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

// channelPriceAmounts 持有解析后的 NUMERIC 售价 + 成本。
type channelPriceAmounts struct {
	uncachedInputPrice     pgtype.Numeric
	cacheReadInputPrice    pgtype.Numeric
	cacheWrite5mInputPrice pgtype.Numeric
	cacheWrite1hInputPrice pgtype.Numeric
	outputPrice            pgtype.Numeric
	reasoningOutputPrice   pgtype.Numeric
	uncachedInputCost      pgtype.Numeric
	cacheReadInputCost     pgtype.Numeric
	cacheWrite5mInputCost  pgtype.Numeric
	cacheWrite1hInputCost  pgtype.Numeric
	outputCost             pgtype.Numeric
	reasoningOutputCost    pgtype.Numeric
}

func parseChannelPriceAmounts(in CreateInput) (channelPriceAmounts, error) {
	var out channelPriceAmounts
	var err error

	if out.uncachedInputPrice, err = parseMoney("uncached_input_price", in.UncachedInputPrice); err != nil {
		return channelPriceAmounts{}, err
	}
	if out.outputPrice, err = parseMoney("output_price", in.OutputPrice); err != nil {
		return channelPriceAmounts{}, err
	}
	if out.cacheReadInputPrice, err = parseOptionalMoney("cache_read_input_price", in.CacheReadInputPrice); err != nil {
		return channelPriceAmounts{}, err
	}
	if out.cacheWrite5mInputPrice, err = parseOptionalMoney("cache_write_5m_input_price", in.CacheWrite5mInputPrice); err != nil {
		return channelPriceAmounts{}, err
	}
	if out.cacheWrite1hInputPrice, err = parseOptionalMoney("cache_write_1h_input_price", in.CacheWrite1hInputPrice); err != nil {
		return channelPriceAmounts{}, err
	}
	if out.reasoningOutputPrice, err = parseOptionalMoney("reasoning_output_price", in.ReasoningOutputPrice); err != nil {
		return channelPriceAmounts{}, err
	}
	if out.uncachedInputCost, err = parseOptionalMoney("uncached_input_cost", in.UncachedInputCost); err != nil {
		return channelPriceAmounts{}, err
	}
	if out.cacheReadInputCost, err = parseOptionalMoney("cache_read_input_cost", in.CacheReadInputCost); err != nil {
		return channelPriceAmounts{}, err
	}
	if out.cacheWrite5mInputCost, err = parseOptionalMoney("cache_write_5m_input_cost", in.CacheWrite5mInputCost); err != nil {
		return channelPriceAmounts{}, err
	}
	if out.cacheWrite1hInputCost, err = parseOptionalMoney("cache_write_1h_input_cost", in.CacheWrite1hInputCost); err != nil {
		return channelPriceAmounts{}, err
	}
	if out.outputCost, err = parseOptionalMoney("output_cost", in.OutputCost); err != nil {
		return channelPriceAmounts{}, err
	}
	if out.reasoningOutputCost, err = parseOptionalMoney("reasoning_output_cost", in.ReasoningOutputCost); err != nil {
		return channelPriceAmounts{}, err
	}

	return out, nil
}

// ensureNonNegativeMargin 录入守卫：任一分项「售价 < 成本」直接返回可读错误（哪个分项亏、差多少）。
func ensureNonNegativeMargin(in CreateInput) error {
	checks := []struct {
		field string
		sale  *string
		cost  *string
	}{
		{"uncached_input", &in.UncachedInputPrice, in.UncachedInputCost},
		{"output", &in.OutputPrice, in.OutputCost},
		{"cache_read_input", in.CacheReadInputPrice, in.CacheReadInputCost},
		{"cache_write_5m_input", in.CacheWrite5mInputPrice, in.CacheWrite5mInputCost},
		{"cache_write_1h_input", in.CacheWrite1hInputPrice, in.CacheWrite1hInputCost},
		{"reasoning_output", in.ReasoningOutputPrice, in.ReasoningOutputCost},
	}

	for _, c := range checks {
		if c.sale == nil || c.cost == nil {
			continue
		}
		saleStr := strings.TrimSpace(*c.sale)
		costStr := strings.TrimSpace(*c.cost)
		if saleStr == "" || costStr == "" {
			continue
		}
		sale, okSale := new(big.Rat).SetString(saleStr)
		cost, okCost := new(big.Rat).SetString(costStr)
		if !okSale || !okCost {
			continue // 非法金额由 parseMoney 给出更准确的错误。
		}
		if sale.Cmp(cost) < 0 {
			return marginViolation(c.field + "_price (" + saleStr + ") must not be below " + c.field + "_cost (" + costStr + ")")
		}
	}

	return nil
}

func toChannelPrice(c sqlc.ChannelPrice) ChannelPrice {
	return ChannelPrice{
		ID:                     c.ID,
		ChannelID:              c.ChannelID,
		ModelID:                c.ModelID,
		Currency:               c.Currency,
		PricingUnit:            c.PricingUnit,
		UncachedInputPrice:     numericString(c.UncachedInputPrice),
		CacheReadInputPrice:    numericPtr(c.CacheReadInputPrice),
		CacheWrite5mInputPrice: numericPtr(c.CacheWrite5mInputPrice),
		CacheWrite1hInputPrice: numericPtr(c.CacheWrite1hInputPrice),
		OutputPrice:            numericString(c.OutputPrice),
		ReasoningOutputPrice:   numericPtr(c.ReasoningOutputPrice),
		UncachedInputCost:      numericPtr(c.UncachedInputCost),
		CacheReadInputCost:     numericPtr(c.CacheReadInputCost),
		CacheWrite5mInputCost:  numericPtr(c.CacheWrite5mInputCost),
		CacheWrite1hInputCost:  numericPtr(c.CacheWrite1hInputCost),
		OutputCost:             numericPtr(c.OutputCost),
		ReasoningOutputCost:    numericPtr(c.ReasoningOutputCost),
		Status:                 c.Status,
		EffectiveFrom:          c.EffectiveFrom.Time,
		EffectiveTo:            timePtr(c.EffectiveTo),
		CreatedAt:              c.CreatedAt.Time,
		UpdatedAt:              c.UpdatedAt.Time,
	}
}

func toChannelPriceFromRow(c sqlc.ListChannelPricesByChannelRow) ChannelPrice {
	return ChannelPrice{
		ID:                     c.ID,
		ChannelID:              c.ChannelID,
		ModelID:                c.ModelID,
		ModelExternalID:        c.ModelExternalID,
		ModelDisplayName:       c.ModelDisplayName,
		Currency:               c.Currency,
		PricingUnit:            c.PricingUnit,
		UncachedInputPrice:     numericString(c.UncachedInputPrice),
		CacheReadInputPrice:    numericPtr(c.CacheReadInputPrice),
		CacheWrite5mInputPrice: numericPtr(c.CacheWrite5mInputPrice),
		CacheWrite1hInputPrice: numericPtr(c.CacheWrite1hInputPrice),
		OutputPrice:            numericString(c.OutputPrice),
		ReasoningOutputPrice:   numericPtr(c.ReasoningOutputPrice),
		UncachedInputCost:      numericPtr(c.UncachedInputCost),
		CacheReadInputCost:     numericPtr(c.CacheReadInputCost),
		CacheWrite5mInputCost:  numericPtr(c.CacheWrite5mInputCost),
		CacheWrite1hInputCost:  numericPtr(c.CacheWrite1hInputCost),
		OutputCost:             numericPtr(c.OutputCost),
		ReasoningOutputCost:    numericPtr(c.ReasoningOutputCost),
		Status:                 c.Status,
		EffectiveFrom:          c.EffectiveFrom.Time,
		EffectiveTo:            timePtr(c.EffectiveTo),
		CreatedAt:              c.CreatedAt.Time,
		UpdatedAt:              c.UpdatedAt.Time,
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
	if s == "" {
		return pgtype.Numeric{}, invalidArgument(field, "is required")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || strings.ContainsAny(s, "eE") {
		return pgtype.Numeric{}, invalidArgument(field, "must be a non-negative decimal amount")
	}
	if r.Sign() < 0 {
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

func isCheckViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == constraint
}

func invalidArgument(field, message string) error {
	return failure.New(
		failure.CodeAdminInvalidArgument,
		failure.WithMessage(message),
		failure.WithField("field", field),
	)
}

func marginViolation(message string) error {
	return failure.New(
		failure.CodeAdminInvalidArgument,
		failure.WithMessage(message),
		failure.WithField("field", "margin"),
	)
}

func notFound(message string) error {
	return failure.New(failure.CodeAdminNotFound, failure.WithMessage(message))
}

func storeFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}
