package publicmodels

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// DiscountPoint 是某一时刻的折扣采样（Discount = 售价 / 官方牌价，0.2 表示 2 折）。
type DiscountPoint struct {
	At time.Time
	// Ratio 为 nil 表示该时刻没有生效价格（模型当时尚未定价），前端应断开折线。
	Discount *float64
}

// DiscountHistory 是单个模型的折扣走势与区间统计。
type DiscountHistory struct {
	ModelID string
	Points  []DiscountPoint
	// Current / Average / Min 均为区间内有效采样的统计；无有效采样时为 nil。
	Current *float64
	Average *float64
	Min     *float64
}

// historySampleInterval 是折扣走势的采样粒度。
//
// 价格本身是阶跃的（调价瞬间生效），但按固定粒度重采样后，序列才有统一的时间轴，
// 前端也才能画成一条随时间读的曲线；一小时的粒度足以体现调价，又不会让点数失控。
const historySampleInterval = time.Hour

// ListDiscountHistory 重建各在售模型最近 window 时长内的折扣走势。
//
// 数据源是 model_prices 的生效窗口本身（改价 = 关旧窗口 + 开新窗口），因此无需任何
// 采样任务：把窗口按时间轴回放，就能还原每个采样点上客户当时会被按什么折扣计费。
func (s *Service) ListDiscountHistory(ctx context.Context, window time.Duration) ([]DiscountHistory, error) {
	if window <= 0 {
		window = 48 * time.Hour
	}
	// 整点网格保证时间轴均匀可读；末尾再补一个「此刻」的精确采样——
	// 折线右缘标注的是「现在」，Current 也取自最后一个有效点，若只截到整点，
	// 刚发生的调价要等到下个整点才可见，页面会在最长一小时里报着旧折扣。
	now := time.Now().UTC()
	gridEnd := now.Truncate(historySampleInterval)
	since := gridEnd.Add(-window)

	rows, err := s.store.ListPublicModelPriceWindows(ctx, pgtype.Timestamptz{Time: since, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list public model price windows: %w", err)
	}

	windowsByModel := make(map[string][]sqlc.ListPublicModelPriceWindowsRow, len(rows))
	for _, row := range rows {
		windowsByModel[row.ModelKey] = append(windowsByModel[row.ModelKey], row)
	}

	timeline := make([]time.Time, 0, int(window/historySampleInterval)+2)
	for at := since; !at.After(gridEnd); at = at.Add(historySampleInterval) {
		timeline = append(timeline, at)
	}
	if now.After(gridEnd) {
		timeline = append(timeline, now)
	}

	out := make([]DiscountHistory, 0, len(windowsByModel))
	for modelKey, windows := range windowsByModel {
		out = append(out, buildHistory(modelKey, windows, timeline))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModelID < out[j].ModelID })
	return out, nil
}

func buildHistory(
	modelKey string,
	windows []sqlc.ListPublicModelPriceWindowsRow,
	timeline []time.Time,
) DiscountHistory {
	history := DiscountHistory{ModelID: modelKey, Points: make([]DiscountPoint, 0, len(timeline))}

	var sum float64
	var count int
	for _, at := range timeline {
		discount := discountAt(windows, at)
		history.Points = append(history.Points, DiscountPoint{At: at, Discount: discount})
		if discount == nil {
			continue
		}
		sum += *discount
		count++
		if history.Min == nil || *discount < *history.Min {
			value := *discount
			history.Min = &value
		}
		history.Current = discount
	}
	if count > 0 {
		average := sum / float64(count)
		history.Average = &average
	}
	return history
}

// discountAt 取 at 时刻生效窗口的折扣。
//
// enabled 窗口优先（同一时刻至多一个，DB 排除约束保证）；停用窗口按其真实生效区间
// 参与回放——被替换/停用前它确实计费过，抹掉会让走势在每次调价时整段消失。
// 停用窗口的终点（effective_until）已在 SQL 里按停用时刻收口。
func discountAt(windows []sqlc.ListPublicModelPriceWindowsRow, at time.Time) *float64 {
	var fallback *float64
	for _, w := range windows {
		if !w.EffectiveFrom.Valid || w.EffectiveFrom.Time.After(at) {
			continue
		}
		if w.EffectiveUntil.Valid && !w.EffectiveUntil.Time.After(at) {
			continue
		}
		if w.WindowStatus == "enabled" {
			return windowDiscount(w)
		}
		if fallback == nil {
			fallback = windowDiscount(w)
		}
	}
	return fallback
}

// windowDiscount 求单个窗口的折扣：绝对售价整组配置时用 售价/牌价，否则直接取折扣系数。
// 与 billing.ResolveCustomerPrice 的两级解析同口径，保证走势与实际结算一致。
func windowDiscount(w sqlc.ListPublicModelPriceWindowsRow) *float64 {
	if w.SaleUncachedInputPrice.Valid && w.SaleOutputPrice.Valid {
		return divideNumeric(w.SaleUncachedInputPrice, w.UncachedInputPrice)
	}
	return numericFloat(w.SaleDiscount)
}

func divideNumeric(numerator, denominator pgtype.Numeric) *float64 {
	num := numericRat(numerator)
	den := numericRat(denominator)
	if num == nil || den == nil || den.Sign() <= 0 {
		return nil
	}
	value, _ := new(big.Rat).Quo(num, den).Float64()
	return &value
}

func numericFloat(n pgtype.Numeric) *float64 {
	rat := numericRat(n)
	if rat == nil {
		return nil
	}
	value, _ := rat.Float64()
	return &value
}

func numericRat(n pgtype.Numeric) *big.Rat {
	if !n.Valid || n.NaN || n.InfinityModifier != pgtype.Finite || n.Int == nil {
		return nil
	}
	rat := new(big.Rat).SetInt(n.Int)
	scale := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs32(n.Exp))), nil))
	if n.Exp < 0 {
		return rat.Quo(rat, scale)
	}
	return rat.Mul(rat, scale)
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
