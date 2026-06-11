// Package price 编排 admin 管理端的客户侧售价（prices）读写。
//
// 售价挂在 Unio 模型上（与渠道无关），时间分片：同一 model/currency/unit 在不同窗口可有不同价格。
// 设计约束（见 ADMIN_MODULES_DRAFT M4）：
//   - 金额只填明确数值、绝不用 float；DTO 层用十进制字符串承载。
//   - 价格不可删：账务（price_snapshots）复算依赖历史价；改价靠「新建一条 + 关闭旧窗口」。
//   - 启用窗口不可重叠：由 DB EXCLUDE 约束 ex_prices_enabled_effective_window 原子保证，
//     违反时 PostgreSQL 报 23P01，这里降级为 admin_pricing_window_overlap（422）。
package price

import (
	"context"
	"errors"
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
	// StatusEnabled 表示售价启用（参与计费选价）。
	StatusEnabled = "enabled"
	// StatusDisabled 表示售价停用。
	StatusDisabled = "disabled"

	// PricingUnitPer1MTokens 是当前唯一支持的计价单位。
	PricingUnitPer1MTokens = "per_1m_tokens"
)

// moneyPattern 限定金额为非负十进制（不带符号即保证 >= 0），不接受科学计数法。
var moneyPattern = regexp.MustCompile(`^\d+(\.\d+)?$`)

// Store 定义售价管理所需的存储能力。
type Store interface {
	LookupModelByID(ctx context.Context, id int64) (sqlc.Model, error)
	GetPrice(ctx context.Context, id int64) (sqlc.Price, error)
	ListPricesByModel(ctx context.Context, modelID int64) ([]sqlc.Price, error)
	CreatePrice(ctx context.Context, arg sqlc.CreatePriceParams) (sqlc.Price, error)
	UpdatePriceWindow(ctx context.Context, arg sqlc.UpdatePriceWindowParams) (sqlc.Price, error)
}

// Price 是 admin 视角的售价事实；金额以十进制字符串承载，可空项用 *string。
type Price struct {
	ID                     int64
	ModelID                int64
	Currency               string
	PricingUnit            string
	UncachedInputPrice     string
	CacheReadInputPrice    *string
	CacheWrite5mInputPrice *string
	CacheWrite1hInputPrice *string
	OutputPrice            string
	ReasoningOutputPrice   *string
	Status                 string
	EffectiveFrom          time.Time
	EffectiveTo            *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// CreateInput 是创建售价的入参；金额为十进制字符串，可空项传 nil 表示 SQL NULL。
type CreateInput struct {
	ModelID                int64
	Currency               string
	PricingUnit            string
	UncachedInputPrice     string
	CacheReadInputPrice    *string
	CacheWrite5mInputPrice *string
	CacheWrite1hInputPrice *string
	OutputPrice            string
	ReasoningOutputPrice   *string
	Status                 string
	EffectiveFrom          time.Time
	EffectiveTo            *time.Time
}

// UpdateInput 是 PATCH 售价的入参：只改启停状态与生效结束时间（关闭窗口）；金额不可改。
type UpdateInput struct {
	ID          int64
	Status      string
	EffectiveTo *time.Time
}

// Service 编排售价读写。
type Service struct {
	store Store
}

// NewService 创建售价管理服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// List 列出某模型全部售价（含历史与停用）；模型不存在返回 not_found。
func (s *Service) List(ctx context.Context, modelID int64) ([]Price, error) {
	if modelID <= 0 {
		return nil, invalidArgument("model_id", "model id must be positive")
	}
	if _, err := s.store.LookupModelByID(ctx, modelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound("model not found")
		}
		return nil, storeFailed(err, "load model")
	}

	rows, err := s.store.ListPricesByModel(ctx, modelID)
	if err != nil {
		return nil, storeFailed(err, "list prices")
	}

	prices := make([]Price, 0, len(rows))
	for _, row := range rows {
		prices = append(prices, toPrice(row))
	}

	return prices, nil
}

// Create 创建一条售价：校验模型存在、金额合法；启用窗口重叠由 DB 约束保证（→422）。
func (s *Service) Create(ctx context.Context, in CreateInput) (Price, error) {
	if in.ModelID <= 0 {
		return Price{}, invalidArgument("model_id", "model id must be positive")
	}
	currency := strings.TrimSpace(in.Currency)
	if currency == "" {
		return Price{}, invalidArgument("currency", "currency is required")
	}
	if in.PricingUnit != PricingUnitPer1MTokens {
		return Price{}, invalidArgument("pricing_unit", "pricing_unit must be \"per_1m_tokens\"")
	}
	if err := validateStatus(in.Status); err != nil {
		return Price{}, err
	}
	if in.EffectiveFrom.IsZero() {
		return Price{}, invalidArgument("effective_from", "effective_from is required")
	}
	if in.EffectiveTo != nil && !in.EffectiveTo.After(in.EffectiveFrom) {
		return Price{}, invalidArgument("effective_to", "effective_to must be after effective_from")
	}

	uncached, err := parseMoney("uncached_input_price", in.UncachedInputPrice)
	if err != nil {
		return Price{}, err
	}
	output, err := parseMoney("output_price", in.OutputPrice)
	if err != nil {
		return Price{}, err
	}
	cacheRead, err := parseOptionalMoney("cache_read_input_price", in.CacheReadInputPrice)
	if err != nil {
		return Price{}, err
	}
	cacheWrite5m, err := parseOptionalMoney("cache_write_5m_input_price", in.CacheWrite5mInputPrice)
	if err != nil {
		return Price{}, err
	}
	cacheWrite1h, err := parseOptionalMoney("cache_write_1h_input_price", in.CacheWrite1hInputPrice)
	if err != nil {
		return Price{}, err
	}
	reasoning, err := parseOptionalMoney("reasoning_output_price", in.ReasoningOutputPrice)
	if err != nil {
		return Price{}, err
	}

	if _, err := s.store.LookupModelByID(ctx, in.ModelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Price{}, notFound("model not found")
		}
		return Price{}, storeFailed(err, "load model")
	}

	row, err := s.store.CreatePrice(ctx, sqlc.CreatePriceParams{
		ModelID:                in.ModelID,
		Currency:               currency,
		PricingUnit:            in.PricingUnit,
		UncachedInputPrice:     uncached,
		CacheReadInputPrice:    cacheRead,
		CacheWrite5mInputPrice: cacheWrite5m,
		CacheWrite1hInputPrice: cacheWrite1h,
		OutputPrice:            output,
		ReasoningOutputPrice:   reasoning,
		Status:                 in.Status,
		EffectiveFrom:          tsParam(&in.EffectiveFrom),
		EffectiveTo:            tsParam(in.EffectiveTo),
	})
	if err != nil {
		if isExclusionViolation(err) {
			return Price{}, overlap()
		}
		return Price{}, storeFailed(err, "create price")
	}

	return toPrice(row), nil
}

// Update 调整窗口/启停：改 effective_to（关闭窗口）与 status；窗口重叠由 DB 约束保证（→422）。
func (s *Service) Update(ctx context.Context, in UpdateInput) (Price, error) {
	if in.ID <= 0 {
		return Price{}, invalidArgument("id", "id must be positive")
	}
	if err := validateStatus(in.Status); err != nil {
		return Price{}, err
	}

	existing, err := s.store.GetPrice(ctx, in.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Price{}, notFound("price not found")
		}
		return Price{}, storeFailed(err, "load price")
	}

	if in.EffectiveTo != nil && !in.EffectiveTo.After(existing.EffectiveFrom.Time) {
		return Price{}, invalidArgument("effective_to", "effective_to must be after effective_from")
	}

	row, err := s.store.UpdatePriceWindow(ctx, sqlc.UpdatePriceWindowParams{
		ID:          in.ID,
		Status:      in.Status,
		EffectiveTo: tsParam(in.EffectiveTo),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Price{}, notFound("price not found")
		}
		if isExclusionViolation(err) {
			return Price{}, overlap()
		}
		return Price{}, storeFailed(err, "update price")
	}

	return toPrice(row), nil
}

func toPrice(p sqlc.Price) Price {
	return Price{
		ID:                     p.ID,
		ModelID:                p.ModelID,
		Currency:               p.Currency,
		PricingUnit:            p.PricingUnit,
		UncachedInputPrice:     numericString(p.UncachedInputPrice),
		CacheReadInputPrice:    numericPtr(p.CacheReadInputPrice),
		CacheWrite5mInputPrice: numericPtr(p.CacheWrite5mInputPrice),
		CacheWrite1hInputPrice: numericPtr(p.CacheWrite1hInputPrice),
		OutputPrice:            numericString(p.OutputPrice),
		ReasoningOutputPrice:   numericPtr(p.ReasoningOutputPrice),
		Status:                 p.Status,
		EffectiveFrom:          p.EffectiveFrom.Time,
		EffectiveTo:            timePtr(p.EffectiveTo),
		CreatedAt:              p.CreatedAt.Time,
		UpdatedAt:              p.UpdatedAt.Time,
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

func overlap() error {
	return failure.New(
		failure.CodeAdminPricingWindowOverlap,
		failure.WithMessage("effective window overlaps an existing enabled price"),
	)
}

func storeFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}

func isExclusionViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23P01"
}
