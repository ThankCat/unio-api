// Package modelprice 编排 admin 管理端的模型基准售价（model_prices）读写（DEC-026 倍率定价）。
//
// model_prices 是「模型对外的基准售价」；客户最终售价 = 基准价 × 线路倍率（routes.price_ratio）。
// 设计约束（沿用 channelprice 口径）：
//   - 金额只填明确数值、绝不用 float；DTO 层用十进制字符串承载，避免精度丢失。
//   - 价格不可改金额：账务（price_snapshots）按事实快照引用历史价；改价靠「新建一条 + 关闭旧窗口」。
//   - 同一 model 的启用窗口不可重叠，否则结算取基准价有歧义。
//   - 仅售价，无成本、无毛利守卫（成本在渠道侧 channel_prices，毛利在结算时算）。
package modelprice

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
	// StatusEnabled 表示基准价启用（参与结算取价）。
	StatusEnabled = "enabled"
	// StatusDisabled 表示基准价停用。
	StatusDisabled = "disabled"

	// PricingUnitPer1MTokens 是当前唯一支持的计价单位。
	PricingUnitPer1MTokens = "per_1m_tokens"
)

// Store 定义模型基准价管理所需的存储能力。
type Store interface {
	LookupModelByID(ctx context.Context, id int64) (sqlc.Model, error)
	GetModelPrice(ctx context.Context, id int64) (sqlc.ModelPrice, error)
	ListModelPricesByModel(ctx context.Context, modelID int64) ([]sqlc.ListModelPricesByModelRow, error)
	ListEnabledModelPriceWindows(ctx context.Context, arg sqlc.ListEnabledModelPriceWindowsParams) ([]sqlc.ListEnabledModelPriceWindowsRow, error)
	CreateModelPrice(ctx context.Context, arg sqlc.CreateModelPriceParams) (sqlc.ModelPrice, error)
	UpdateModelPriceWindow(ctx context.Context, arg sqlc.UpdateModelPriceWindowParams) (sqlc.ModelPrice, error)
}

// ModelPrice 是 admin 视角的模型基准价事实；金额以十进制字符串承载，可空项用 *string。
type ModelPrice struct {
	ID                          int64
	ModelID                     int64
	ModelExternalID             string
	ModelDisplayName            string
	Currency                    string
	PricingUnit                 string
	UncachedInputPrice          string
	CacheReadInputPrice         *string
	CacheWrite5mInputPrice      *string
	CacheWrite1hInputPrice      *string
	CacheWrite30mInputPrice     *string
	OutputPrice                 string
	ReasoningOutputPrice        *string
	LongContextEnabled          bool
	LongContextThreshold        *int64
	LongContextInputMultiplier  *string
	LongContextOutputMultiplier *string
	Status                      string
	EffectiveFrom               time.Time
	EffectiveTo                 *time.Time
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
}

// CreateInput 是创建模型基准价的入参；uncached_input/output 必填，其余可空，金额为十进制字符串。
type CreateInput struct {
	ModelID                     int64
	Currency                    string
	PricingUnit                 string
	UncachedInputPrice          string
	CacheReadInputPrice         *string
	CacheWrite5mInputPrice      *string
	CacheWrite1hInputPrice      *string
	CacheWrite30mInputPrice     *string
	OutputPrice                 string
	ReasoningOutputPrice        *string
	LongContextEnabled          bool
	LongContextThreshold        *int64
	LongContextInputMultiplier  *string
	LongContextOutputMultiplier *string
	Status                      string
	EffectiveFrom               time.Time
	EffectiveTo                 *time.Time
}

// UpdateInput 是 PATCH 模型基准价的入参：只改启停状态与生效结束时间（关闭窗口）；金额不可改。
type UpdateInput struct {
	ID          int64
	Status      string
	EffectiveTo *time.Time
}

// Service 编排模型基准价读写。
type Service struct {
	store Store
}

// NewService 创建模型基准价管理服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// List 列出某 model 下全部基准价（含历史与停用）；model 不存在返回 not_found。
func (s *Service) List(ctx context.Context, modelID int64) ([]ModelPrice, error) {
	if modelID <= 0 {
		return nil, invalidArgument("model_id", "model id must be positive")
	}
	if _, err := s.store.LookupModelByID(ctx, modelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound("model not found")
		}
		return nil, storeFailed(err, "load model")
	}

	rows, err := s.store.ListModelPricesByModel(ctx, modelID)
	if err != nil {
		return nil, storeFailed(err, "list model prices")
	}

	prices := make([]ModelPrice, 0, len(rows))
	for _, row := range rows {
		prices = append(prices, toModelPriceFromRow(row))
	}

	return prices, nil
}

// Create 创建一条模型基准价：校验模型存在、金额合法、生效窗口不重叠。
func (s *Service) Create(ctx context.Context, in CreateInput) (ModelPrice, error) {
	if in.ModelID <= 0 {
		return ModelPrice{}, invalidArgument("model_id", "model_id must be positive")
	}
	currency := strings.TrimSpace(in.Currency)
	if currency == "" {
		return ModelPrice{}, invalidArgument("currency", "currency is required")
	}
	if in.PricingUnit != PricingUnitPer1MTokens {
		return ModelPrice{}, invalidArgument("pricing_unit", "pricing_unit must be \"per_1m_tokens\"")
	}
	if err := validateStatus(in.Status); err != nil {
		return ModelPrice{}, err
	}
	if in.EffectiveFrom.IsZero() {
		return ModelPrice{}, invalidArgument("effective_from", "effective_from is required")
	}
	if in.EffectiveTo != nil && !in.EffectiveTo.After(in.EffectiveFrom) {
		return ModelPrice{}, invalidArgument("effective_to", "effective_to must be after effective_from")
	}

	amounts, err := parseModelPriceAmounts(in)
	if err != nil {
		return ModelPrice{}, err
	}
	longContext, err := parseLongContextConfig(in)
	if err != nil {
		return ModelPrice{}, err
	}

	// 基准价必须挂在已存在的 model 上（DB 也有同名外键，这里给清晰 400）。
	if _, err := s.store.LookupModelByID(ctx, in.ModelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ModelPrice{}, invalidArgument("model_id", "model not found")
		}
		return ModelPrice{}, storeFailed(err, "load model")
	}

	if in.Status == StatusEnabled {
		if err := s.ensureNoOverlap(ctx, in.ModelID, 0, in.EffectiveFrom, in.EffectiveTo); err != nil {
			return ModelPrice{}, err
		}
	}

	row, err := s.store.CreateModelPrice(ctx, sqlc.CreateModelPriceParams{
		ModelID:                     in.ModelID,
		Currency:                    currency,
		PricingUnit:                 in.PricingUnit,
		UncachedInputPrice:          amounts.uncachedInputPrice,
		CacheReadInputPrice:         amounts.cacheReadInputPrice,
		CacheWrite5mInputPrice:      amounts.cacheWrite5mInputPrice,
		CacheWrite1hInputPrice:      amounts.cacheWrite1hInputPrice,
		CacheWrite30mInputPrice:     amounts.cacheWrite30mInputPrice,
		OutputPrice:                 amounts.outputPrice,
		ReasoningOutputPrice:        amounts.reasoningOutputPrice,
		LongContextEnabled:          longContext.enabled,
		LongContextThreshold:        longContext.threshold,
		LongContextInputMultiplier:  longContext.inputMultiplier,
		LongContextOutputMultiplier: longContext.outputMultiplier,
		Status:                      in.Status,
		EffectiveFrom:               tsParam(&in.EffectiveFrom),
		EffectiveTo:                 tsParam(in.EffectiveTo),
	})
	if err != nil {
		return ModelPrice{}, storeFailed(err, "create model price")
	}

	return toModelPrice(row), nil
}

// Update 调整窗口/启停：改 effective_to（关闭窗口）与 status；金额不可改。重新启用或延长窗口时复查重叠。
func (s *Service) Update(ctx context.Context, in UpdateInput) (ModelPrice, error) {
	if in.ID <= 0 {
		return ModelPrice{}, invalidArgument("id", "id must be positive")
	}
	if err := validateStatus(in.Status); err != nil {
		return ModelPrice{}, err
	}

	existing, err := s.store.GetModelPrice(ctx, in.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ModelPrice{}, notFound("model price not found")
		}
		return ModelPrice{}, storeFailed(err, "load model price")
	}

	if in.EffectiveTo != nil && !in.EffectiveTo.After(existing.EffectiveFrom.Time) {
		return ModelPrice{}, invalidArgument("effective_to", "effective_to must be after effective_from")
	}

	if in.Status == StatusEnabled {
		if err := s.ensureNoOverlap(ctx, existing.ModelID, existing.ID, existing.EffectiveFrom.Time, in.EffectiveTo); err != nil {
			return ModelPrice{}, err
		}
	}

	row, err := s.store.UpdateModelPriceWindow(ctx, sqlc.UpdateModelPriceWindowParams{
		ID:          in.ID,
		Status:      in.Status,
		EffectiveTo: tsParam(in.EffectiveTo),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ModelPrice{}, notFound("model price not found")
		}
		return ModelPrice{}, storeFailed(err, "update model price")
	}

	return toModelPrice(row), nil
}

// ensureNoOverlap 校验目标窗口与同一 model 现有启用窗口不重叠（半开区间 [from, to)）。
func (s *Service) ensureNoOverlap(ctx context.Context, modelID, excludeID int64, from time.Time, to *time.Time) error {
	windows, err := s.store.ListEnabledModelPriceWindows(ctx, sqlc.ListEnabledModelPriceWindowsParams{
		ModelID:   modelID,
		ExcludeID: excludeID,
	})
	if err != nil {
		return storeFailed(err, "list enabled model price windows")
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
				failure.WithMessage("effective window overlaps an existing enabled model price"),
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

// modelPriceAmounts 持有解析后的 NUMERIC 基准售价。
type modelPriceAmounts struct {
	uncachedInputPrice      pgtype.Numeric
	cacheReadInputPrice     pgtype.Numeric
	cacheWrite5mInputPrice  pgtype.Numeric
	cacheWrite1hInputPrice  pgtype.Numeric
	cacheWrite30mInputPrice pgtype.Numeric
	outputPrice             pgtype.Numeric
	reasoningOutputPrice    pgtype.Numeric
}

func parseModelPriceAmounts(in CreateInput) (modelPriceAmounts, error) {
	var out modelPriceAmounts
	var err error

	if out.uncachedInputPrice, err = parseMoney("uncached_input_price", in.UncachedInputPrice); err != nil {
		return modelPriceAmounts{}, err
	}
	if out.outputPrice, err = parseMoney("output_price", in.OutputPrice); err != nil {
		return modelPriceAmounts{}, err
	}
	if out.cacheReadInputPrice, err = parseOptionalMoney("cache_read_input_price", in.CacheReadInputPrice); err != nil {
		return modelPriceAmounts{}, err
	}
	if out.cacheWrite5mInputPrice, err = parseOptionalMoney("cache_write_5m_input_price", in.CacheWrite5mInputPrice); err != nil {
		return modelPriceAmounts{}, err
	}
	if out.cacheWrite1hInputPrice, err = parseOptionalMoney("cache_write_1h_input_price", in.CacheWrite1hInputPrice); err != nil {
		return modelPriceAmounts{}, err
	}
	if out.cacheWrite30mInputPrice, err = parseOptionalMoney("cache_write_30m_input_price", in.CacheWrite30mInputPrice); err != nil {
		return modelPriceAmounts{}, err
	}
	if out.reasoningOutputPrice, err = parseOptionalMoney("reasoning_output_price", in.ReasoningOutputPrice); err != nil {
		return modelPriceAmounts{}, err
	}

	return out, nil
}

func toModelPrice(c sqlc.ModelPrice) ModelPrice {
	return ModelPrice{
		ID:                          c.ID,
		ModelID:                     c.ModelID,
		Currency:                    c.Currency,
		PricingUnit:                 c.PricingUnit,
		UncachedInputPrice:          numericString(c.UncachedInputPrice),
		CacheReadInputPrice:         numericPtr(c.CacheReadInputPrice),
		CacheWrite5mInputPrice:      numericPtr(c.CacheWrite5mInputPrice),
		CacheWrite1hInputPrice:      numericPtr(c.CacheWrite1hInputPrice),
		CacheWrite30mInputPrice:     numericPtr(c.CacheWrite30mInputPrice),
		OutputPrice:                 numericString(c.OutputPrice),
		ReasoningOutputPrice:        numericPtr(c.ReasoningOutputPrice),
		LongContextEnabled:          c.LongContextEnabled,
		LongContextThreshold:        int64Ptr(c.LongContextThreshold),
		LongContextInputMultiplier:  numericPtr(c.LongContextInputMultiplier),
		LongContextOutputMultiplier: numericPtr(c.LongContextOutputMultiplier),
		Status:                      c.Status,
		EffectiveFrom:               c.EffectiveFrom.Time,
		EffectiveTo:                 timePtr(c.EffectiveTo),
		CreatedAt:                   c.CreatedAt.Time,
		UpdatedAt:                   c.UpdatedAt.Time,
	}
}

func toModelPriceFromRow(c sqlc.ListModelPricesByModelRow) ModelPrice {
	return ModelPrice{
		ID:                          c.ID,
		ModelID:                     c.ModelID,
		ModelExternalID:             c.ModelExternalID,
		ModelDisplayName:            c.ModelDisplayName,
		Currency:                    c.Currency,
		PricingUnit:                 c.PricingUnit,
		UncachedInputPrice:          numericString(c.UncachedInputPrice),
		CacheReadInputPrice:         numericPtr(c.CacheReadInputPrice),
		CacheWrite5mInputPrice:      numericPtr(c.CacheWrite5mInputPrice),
		CacheWrite1hInputPrice:      numericPtr(c.CacheWrite1hInputPrice),
		CacheWrite30mInputPrice:     numericPtr(c.CacheWrite30mInputPrice),
		OutputPrice:                 numericString(c.OutputPrice),
		ReasoningOutputPrice:        numericPtr(c.ReasoningOutputPrice),
		LongContextEnabled:          c.LongContextEnabled,
		LongContextThreshold:        int64Ptr(c.LongContextThreshold),
		LongContextInputMultiplier:  numericPtr(c.LongContextInputMultiplier),
		LongContextOutputMultiplier: numericPtr(c.LongContextOutputMultiplier),
		Status:                      c.Status,
		EffectiveFrom:               c.EffectiveFrom.Time,
		EffectiveTo:                 timePtr(c.EffectiveTo),
		CreatedAt:                   c.CreatedAt.Time,
		UpdatedAt:                   c.UpdatedAt.Time,
	}
}

// longContextConfig 是解析后的长上下文阶梯配置（对应 model_prices 四列）。
type longContextConfig struct {
	enabled          bool
	threshold        pgtype.Int8
	inputMultiplier  pgtype.Numeric
	outputMultiplier pgtype.Numeric
}

// parseLongContextConfig 解析长上下文配置：启用时 threshold/倍率必填且 >0；关闭时可保留可选值供展示，或全空。
func parseLongContextConfig(in CreateInput) (longContextConfig, error) {
	var out longContextConfig
	out.enabled = in.LongContextEnabled

	if in.LongContextThreshold != nil {
		if *in.LongContextThreshold <= 0 {
			return longContextConfig{}, invalidArgument("long_context_threshold", "must be a positive integer")
		}
		out.threshold = pgtype.Int8{Int64: *in.LongContextThreshold, Valid: true}
	}
	var err error
	if out.inputMultiplier, err = parseOptionalPositiveMultiplier("long_context_input_multiplier", in.LongContextInputMultiplier); err != nil {
		return longContextConfig{}, err
	}
	if out.outputMultiplier, err = parseOptionalPositiveMultiplier("long_context_output_multiplier", in.LongContextOutputMultiplier); err != nil {
		return longContextConfig{}, err
	}

	if !out.enabled {
		return out, nil
	}
	if !out.threshold.Valid {
		return longContextConfig{}, invalidArgument("long_context_threshold", "is required when long_context_enabled is true")
	}
	if !out.inputMultiplier.Valid {
		return longContextConfig{}, invalidArgument("long_context_input_multiplier", "is required when long_context_enabled is true")
	}
	if !out.outputMultiplier.Valid {
		return longContextConfig{}, invalidArgument("long_context_output_multiplier", "is required when long_context_enabled is true")
	}
	return out, nil
}

// parseOptionalPositiveMultiplier 解析可选正倍率：nil/空 → NULL；否则须为 >0 的十进制。
func parseOptionalPositiveMultiplier(field string, raw *string) (pgtype.Numeric, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return pgtype.Numeric{Valid: false}, nil
	}
	n, err := parseMoney(field, *raw)
	if err != nil {
		return pgtype.Numeric{}, err
	}
	r, ok := new(big.Rat).SetString(strings.TrimSpace(*raw))
	if !ok || r.Sign() <= 0 {
		return pgtype.Numeric{}, invalidArgument(field, "must be a positive decimal amount")
	}
	return n, nil
}

func int64Ptr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	out := v.Int64
	return &out
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
