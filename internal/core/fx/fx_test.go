package fx

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func ratFromString(t *testing.T, s string) *big.Rat {
	t.Helper()
	rat, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("invalid rat literal %q", s)
	}
	return rat
}

// 精确换算：42 CNY ÷ 7.0 = 6，落库到 scale 10 必须是精确的 6.0000000000。
func TestUsdFromOriginalExactDivision(t *testing.T) {
	usd := UsdFromOriginal(ratFromString(t, "42"), ratFromString(t, "7"))
	if usd.Cmp(big.NewRat(6, 1)) != 0 {
		t.Fatalf("42/7 = %s, want 6", usd.RatString())
	}
	numeric := NumericFromRat(usd, 10)
	if numeric.Int.String() != "60000000000" || numeric.Exp != -10 {
		t.Fatalf("numeric = %s * 10^%d, want 60000000000 * 10^-10", numeric.Int, numeric.Exp)
	}
}

// 单次舍入：1 ÷ 7.17 的 scale-10 值必须等于精确有理数 half-up 的结果（与 float64 路径无关）。
func TestNumericFromRatSingleRounding(t *testing.T) {
	usd := UsdFromOriginal(ratFromString(t, "1"), ratFromString(t, "7.17"))
	numeric := NumericFromRat(usd, 10)
	// 1/7.17 = 100/717 = 0.13947001394700139...，第 11 位是 0 → 截断即 0.1394700139。
	if got := numeric.Int.String(); got != "1394700139" {
		t.Fatalf("rounded numeric int = %s, want 1394700139", got)
	}

	roundTrip, err := RatFromNumeric(numeric)
	if err != nil {
		t.Fatalf("rat from numeric: %v", err)
	}
	diff := new(big.Rat).Sub(roundTrip, usd)
	diff.Abs(diff)
	// 单次舍入误差必须 < 0.5 * 10^-10。
	if diff.Cmp(big.NewRat(1, 2e10)) >= 0 {
		t.Fatalf("rounding error too large: %s", diff.RatString())
	}
}

// 乘除等价：sale × rate ≥ cost 与 sale ≥ cost ÷ rate 在 big.Rat 下判定必然一致。
func TestMultiplyDivideEquivalence(t *testing.T) {
	cases := []struct{ sale, cost, rate string }{
		{"2.5", "7", "7.0"},
		{"2.5", "17.5", "7.0"},          // 恰好相等
		{"2.5", "17.5000000001", "7.0"}, // 差一丝
		{"0.0000000001", "0.0000000007", "7.17"},
		{"12.5", "35", "2.8"},
	}
	for _, c := range cases {
		sale, cost, rate := ratFromString(t, c.sale), ratFromString(t, c.cost), ratFromString(t, c.rate)
		mulVerdict := new(big.Rat).Mul(sale, rate).Cmp(cost) >= 0
		divVerdict := sale.Cmp(UsdFromOriginal(cost, rate)) >= 0
		if mulVerdict != divVerdict {
			t.Fatalf("verdict mismatch for %+v: mul=%v div=%v", c, mulVerdict, divVerdict)
		}
	}
}

// 合理性校验：跳变阈值 5% 的两侧、区间越界、非法值。
func TestValidateRate(t *testing.T) {
	prev := ratFromString(t, "7.0")
	if err := ValidateRate("CNY", ratFromString(t, "7.34"), prev); err != nil {
		t.Fatalf("4.857%% jump should pass: %v", err)
	}
	if err := ValidateRate("CNY", ratFromString(t, "7.36"), prev); err == nil {
		t.Fatal("5.14% jump should be rejected")
	}
	if err := ValidateRate("CNY", ratFromString(t, "4.9"), nil); err == nil {
		t.Fatal("below band should be rejected")
	}
	if err := ValidateRate("CNY", ratFromString(t, "10.1"), nil); err == nil {
		t.Fatal("above band should be rejected")
	}
	if err := ValidateRate("CNY", new(big.Rat), nil); err == nil {
		t.Fatal("zero rate should be rejected")
	}
	// 未登记区间的币种只做跳变检查。
	if err := ValidateRate("JPY", ratFromString(t, "150"), nil); err != nil {
		t.Fatalf("unbanded currency should pass band check: %v", err)
	}
}

// 拉取链：主源失败自动落到备源；全失败返回聚合错误；JSON 数字不经 float64。
func TestFetcherFallbackChain(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result":                "success",
			"time_last_update_unix": time.Now().Unix(),
			// 使用刻意超出 float64 精度的十进制文本，验证 json.Number 直通 big.Rat。
			"rates": map[string]json.RawMessage{"CNY": json.RawMessage(`7.1234567890123456789`)},
		})
	}))
	defer ok.Close()

	fetcher := &Fetcher{Sources: []Source{
		&ExchangeRateAPISource{BaseURL: failing.URL, APIKey: func(context.Context) string { return "k" }},
		&OpenERAPISource{BaseURL: ok.URL},
	}}
	result, source, err := fetcher.Fetch(context.Background(), "CNY")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if source != "er-api" {
		t.Fatalf("source = %s, want er-api (fallback)", source)
	}
	want := ratFromString(t, "7.1234567890123456789")
	if result.Rate.Cmp(want) != 0 {
		t.Fatalf("rate = %s, want exact %s", result.Rate.RatString(), want.RatString())
	}

	allFail := &Fetcher{Sources: []Source{
		&OpenERAPISource{BaseURL: failing.URL},
		&FrankfurterSource{BaseURL: failing.URL},
	}}
	if _, _, err := allFail.Fetch(context.Background(), "CNY"); err == nil {
		t.Fatal("all sources failing must return error")
	}
}

// 缺 key 时主源直接失败（不发请求），链路落到备源。
func TestExchangeRateAPISourceRequiresKey(t *testing.T) {
	src := &ExchangeRateAPISource{APIKey: func(context.Context) string { return "" }}
	if _, err := src.FetchUSDRate(context.Background(), "CNY"); err == nil {
		t.Fatal("missing key must fail")
	}
}

// Service 缓存：TTL 内读缓存，过期后重新读库。
func TestServiceCachesWithinTTL(t *testing.T) {
	store := &stubRateStore{}
	svc := NewService(store, time.Minute)
	current := time.Unix(1000, 0)
	svc.now = func() time.Time { return current }

	if _, err := svc.LatestRate(context.Background(), "CNY"); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if _, err := svc.LatestRate(context.Background(), "CNY"); err != nil {
		t.Fatalf("cached read: %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1 (cached)", store.calls)
	}
	current = current.Add(2 * time.Minute)
	if _, err := svc.LatestRate(context.Background(), "CNY"); err != nil {
		t.Fatalf("expired read: %v", err)
	}
	if store.calls != 2 {
		t.Fatalf("store calls = %d, want 2 (ttl expired)", store.calls)
	}
}

type stubRateStore struct{ calls int }

func (s *stubRateStore) LatestExchangeRate(_ context.Context, _ sqlc.LatestExchangeRateParams) (sqlc.ExchangeRate, error) {
	s.calls++
	return sqlc.ExchangeRate{
		ID:            1,
		BaseCurrency:  BaseCurrency,
		QuoteCurrency: "CNY",
		Rate:          pgtype.Numeric{Int: big.NewInt(71700000000), Exp: -10, Valid: true},
		RateDate:      pgtype.Date{Time: time.Now().UTC(), Valid: true},
		Source:        "manual",
		FetchedAt:     pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}, nil
}
