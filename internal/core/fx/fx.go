// Package fx 提供多货币支持的汇率读取与换算（PLAN D2/D7/D10）。
//
// 职责边界：本包只「读本地 exchange_rates 表 + 精确换算」；拉取外部 API 由 fetch.go 的
// Fetcher 承担且仅被 worker / admin 验证端点调用——任何请求路径都不同步访问外部源。
// 换算全程 big.Rat 精确运算，只在最终落库时由调用方单次舍入到 NUMERIC(20,10)。
package fx

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// BaseCurrency 是平台结算基准货币；exchange_rates 里 rate = 1 BaseCurrency 兑多少目标货币。
const BaseCurrency = "USD"

// ErrRateNotFound 表示 exchange_rates 里没有该币种对的任何汇率行。
// 守卫语义（D5/D11）：配置写入时缺汇率 = 拒绝；路由候选缺汇率 = 剔除；结算路径理论上到不了这里
// （守卫保证 CNY 渠道存在时汇率必已存在，且汇率行只追加不删除）。
var ErrRateNotFound = errors.New("fx: exchange rate not found")

// Rate 是一条可用于比较/换算/落库的汇率。
type Rate struct {
	Quote     string
	Value     *big.Rat       // 1 USD 兑多少 Quote（精确值）
	Numeric   pgtype.Numeric // 与 DB 行一致的 NUMERIC(20,10)，结算钉快照直接用
	Date      time.Time      // 汇率所属日（rate_date）
	Source    string
	FetchedAt time.Time
}

// Store 定义汇率读取所需的存储能力，由 *sqlc.Queries 满足。
type Store interface {
	LatestExchangeRate(ctx context.Context, arg sqlc.LatestExchangeRateParams) (sqlc.ExchangeRate, error)
}

// Service 是进程内的汇率读取器：短 TTL 缓存去抖热路径（路由每请求都要查），
// 缓存过期后现读 DB。TTL 内新汇率不可见——可接受，钉进快照的值才是最终口径（D10）。
type Service struct {
	store Store
	ttl   time.Duration

	mu    sync.Mutex
	cache map[string]cachedRate
	now   func() time.Time
}

type cachedRate struct {
	rate      Rate
	expiresAt time.Time
}

// NewService 创建汇率读取器；ttl <= 0 时用默认 1 分钟。
func NewService(store Store, ttl time.Duration) *Service {
	if store == nil {
		panic("fx: store is required")
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	return &Service{store: store, ttl: ttl, cache: make(map[string]cachedRate), now: time.Now}
}

// LatestRate 返回 USD 对 quote 的当前生效汇率；无任何汇率行时返回 ErrRateNotFound。
func (s *Service) LatestRate(ctx context.Context, quote string) (Rate, error) {
	s.mu.Lock()
	if entry, ok := s.cache[quote]; ok && s.now().Before(entry.expiresAt) {
		s.mu.Unlock()
		return entry.rate, nil
	}
	s.mu.Unlock()

	row, err := s.store.LatestExchangeRate(ctx, sqlc.LatestExchangeRateParams{
		BaseCurrency:  BaseCurrency,
		QuoteCurrency: quote,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Rate{}, ErrRateNotFound
	}
	if err != nil {
		return Rate{}, fmt.Errorf("fx: load latest rate: %w", err)
	}
	rate, err := rateFromRow(row)
	if err != nil {
		return Rate{}, err
	}

	s.mu.Lock()
	s.cache[quote] = cachedRate{rate: rate, expiresAt: s.now().Add(s.ttl)}
	s.mu.Unlock()
	return rate, nil
}

func rateFromRow(row sqlc.ExchangeRate) (Rate, error) {
	value, err := RatFromNumeric(row.Rate)
	if err != nil {
		return Rate{}, fmt.Errorf("fx: invalid rate value in row %d: %w", row.ID, err)
	}
	if value.Sign() <= 0 {
		return Rate{}, fmt.Errorf("fx: non-positive rate in row %d", row.ID)
	}
	return Rate{
		Quote:     row.QuoteCurrency,
		Value:     value,
		Numeric:   row.Rate,
		Date:      row.RateDate.Time,
		Source:    row.Source,
		FetchedAt: row.FetchedAt.Time,
	}, nil
}

// UsdFromOriginal 把原币金额按汇率换算为 USD（精确值，不舍入）：usd = amount ÷ rate。
func UsdFromOriginal(amount *big.Rat, rate *big.Rat) *big.Rat {
	return new(big.Rat).Quo(new(big.Rat).Set(amount), rate)
}

// ---- pgtype.Numeric ↔ big.Rat（与 billing/numeric.go 同一套语义，本包自持避免跨包导出内部工具）----

// RatFromNumeric 将 pgtype.Numeric 转成 big.Rat；无效/NaN/Inf 报错，避免 float64 精度污染。
func RatFromNumeric(value pgtype.Numeric) (*big.Rat, error) {
	if !value.Valid || value.NaN || value.InfinityModifier != pgtype.Finite {
		return nil, errors.New("fx: numeric value is not finite")
	}
	if value.Int == nil {
		return new(big.Rat), nil
	}
	rat := new(big.Rat).SetInt(new(big.Int).Set(value.Int))
	if value.Exp > 0 {
		rat.Mul(rat, new(big.Rat).SetInt(pow10(value.Exp)))
	}
	if value.Exp < 0 {
		rat.Quo(rat, new(big.Rat).SetInt(pow10(-value.Exp)))
	}
	return rat, nil
}

// NumericFromRat 将有理数四舍五入到固定小数位（单次舍入，匹配 NUMERIC(20,10) 时 scale=10）。
//
// 这是全仓金额「有理数 → NUMERIC」的唯一 half-up 实现：billing 的分项金额、providerledger 的
// 余额校正差额都经由此处舍入，避免三处各自演化出不同的舍入边界。
// 只服务非负值（价格、汇率、金额）；负数按绝对值舍入后保留符号，方向仍为 half-up（远离零）。
func NumericFromRat(value *big.Rat, scale int32) pgtype.Numeric {
	multiplier := pow10(scale)
	scaled := new(big.Rat).Mul(value, new(big.Rat).SetInt(multiplier))
	return pgtype.Numeric{Int: roundHalfUp(scaled), Exp: -scale, Valid: true}
}

func roundHalfUp(value *big.Rat) *big.Int {
	negative := value.Sign() < 0
	abs := new(big.Rat).Abs(value)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(abs.Num(), abs.Denom(), remainder)
	if new(big.Int).Mul(remainder, big.NewInt(2)).Cmp(abs.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if negative {
		quotient.Neg(quotient)
	}
	return quotient
}

func pow10(exp int32) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exp)), nil)
}
