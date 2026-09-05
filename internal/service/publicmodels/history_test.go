package publicmodels

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func ts(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func ratioValue(t *testing.T, p *float64, want float64, field string) {
	t.Helper()
	if p == nil {
		t.Fatalf("%s = nil, want %v", field, want)
	}
	if diff := *p - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("%s = %v, want %v", field, *p, want)
	}
}

func TestListDiscountHistoryReplaysPriceWindows(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	// 6 小时前从 2 折调到 3 折：替换流程会把旧窗口「收口 + 停用」，新窗口接上。
	// 停用的旧窗口在停用前真实计费过，必须照样参与回放——否则每次调价都会抹掉此前的走势。
	changeAt := now.Add(-6 * time.Hour)
	store := &fakeStore{windows: []sqlc.ListPublicModelPriceWindowsRow{
		{
			ModelID: 1, ModelKey: "gpt-5.6-sol",
			WindowStatus:       "disabled",
			EffectiveFrom:      ts(now.Add(-72 * time.Hour)),
			EffectiveUntil:     ts(changeAt),
			UncachedInputPrice: num(t, "4"), OutputPrice: num(t, "20"),
			SaleDiscount: num(t, "0.2"),
		},
		{
			ModelID: 1, ModelKey: "gpt-5.6-sol",
			WindowStatus:       "enabled",
			EffectiveFrom:      ts(changeAt),
			UncachedInputPrice: num(t, "4"), OutputPrice: num(t, "20"),
			SaleDiscount: num(t, "0.3"),
		},
	}}

	histories, err := NewService(store).ListDiscountHistory(context.Background(), 48*time.Hour)
	if err != nil {
		t.Fatalf("ListDiscountHistory: %v", err)
	}
	if len(histories) != 1 {
		t.Fatalf("want 1 history, got %d", len(histories))
	}
	h := histories[0]

	// 48 小时整点网格含两端点 49 个；now 不在整点上时末尾再补一个「此刻」采样。
	if n := len(h.Points); n != 49 && n != 50 {
		t.Fatalf("want 49 or 50 points, got %d", n)
	}
	// 最后一个采样必须落在「此刻」附近，而不是截断后的整点——右缘标的是「现在」。
	if last := h.Points[len(h.Points)-1].At; time.Since(last) > time.Minute {
		t.Fatalf("last sample must be at request time, got %v", last)
	}
	ratioValue(t, h.Points[0].Discount, 0.2, "oldest point")
	ratioValue(t, h.Points[len(h.Points)-1].Discount, 0.3, "newest point")
	ratioValue(t, h.Current, 0.3, "current")
	ratioValue(t, h.Min, 0.2, "min")
	if h.Average == nil || *h.Average <= 0.2 || *h.Average >= 0.3 {
		t.Fatalf("average must sit between the two ratios, got %v", h.Average)
	}
	// 时间轴必须单调递增，前端才能直接连线。
	for i := 1; i < len(h.Points); i++ {
		if !h.Points[i].At.After(h.Points[i-1].At) {
			t.Fatalf("timeline not increasing at %d", i)
		}
	}
}

func TestListDiscountHistoryAbsoluteSaleUsesSaleDiscount(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	// 绝对售价（$1 / $5）覆盖牌价（$4 / $20）：折扣按 售价/牌价 = 0.25 算，折扣列不参与。
	store := &fakeStore{windows: []sqlc.ListPublicModelPriceWindowsRow{{
		ModelID: 2, ModelKey: "claude-opus-4-6",
		WindowStatus:       "enabled",
		EffectiveFrom:      ts(now.Add(-24 * time.Hour)),
		UncachedInputPrice: num(t, "4"), OutputPrice: num(t, "20"),
		SaleDiscount:         num(t, "0.9"),
		SaleUncachedInputPrice: num(t, "1"), SaleOutputPrice: num(t, "5"),
	}}}

	histories, err := NewService(store).ListDiscountHistory(context.Background(), 48*time.Hour)
	if err != nil {
		t.Fatalf("ListDiscountHistory: %v", err)
	}
	ratioValue(t, histories[0].Current, 0.25, "current")
}

func TestListDiscountHistoryGapsBeforePricing(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	// 只在最近 2 小时内有价格：更早的采样点必须留空，而不是伪造一个折扣。
	store := &fakeStore{windows: []sqlc.ListPublicModelPriceWindowsRow{{
		ModelID: 3, ModelKey: "gpt-5.5",
		WindowStatus:       "enabled",
		EffectiveFrom:      ts(now.Add(-2 * time.Hour)),
		UncachedInputPrice: num(t, "5"), OutputPrice: num(t, "30"),
		SaleDiscount: num(t, "0.2"),
	}}}

	histories, err := NewService(store).ListDiscountHistory(context.Background(), 48*time.Hour)
	if err != nil {
		t.Fatalf("ListDiscountHistory: %v", err)
	}
	h := histories[0]
	if h.Points[0].Discount != nil {
		t.Fatalf("point before pricing must be nil, got %v", *h.Points[0].Discount)
	}
	ratioValue(t, h.Current, 0.2, "current")
}

func TestListDiscountHistoryMidHourChangeVisibleImmediately(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	// 回归：整点之后刚发生的调价必须立刻反映在 Current 与最后一个采样点上，
	// 不能等到下个整点（此前时间轴截断到整点，页面会在最长一小时里报旧折扣）。
	changeAt := now.Add(-time.Second)
	store := &fakeStore{windows: []sqlc.ListPublicModelPriceWindowsRow{
		{
			ModelID: 5, ModelKey: "gpt-5.6-sol",
			WindowStatus:       "disabled",
			EffectiveFrom:      ts(now.Add(-24 * time.Hour)),
			EffectiveUntil:     ts(changeAt),
			UncachedInputPrice: num(t, "5"), OutputPrice: num(t, "25"),
			SaleDiscount: num(t, "0.06"),
		},
		{
			ModelID: 5, ModelKey: "gpt-5.6-sol",
			WindowStatus:       "enabled",
			EffectiveFrom:      ts(changeAt),
			UncachedInputPrice: num(t, "5"), OutputPrice: num(t, "25"),
			SaleDiscount: num(t, "0.07"),
		},
	}}

	histories, err := NewService(store).ListDiscountHistory(context.Background(), 48*time.Hour)
	if err != nil {
		t.Fatalf("ListDiscountHistory: %v", err)
	}
	h := histories[0]
	ratioValue(t, h.Current, 0.07, "current")
	ratioValue(t, h.Points[len(h.Points)-1].Discount, 0.07, "newest point")
}

func TestListDiscountHistoryStoreError(t *testing.T) {
	t.Parallel()
	svc := NewService(&fakeStore{err: errors.New("boom")})
	if _, err := svc.ListDiscountHistory(context.Background(), 48*time.Hour); err == nil {
		t.Fatal("want error from store")
	}
}
