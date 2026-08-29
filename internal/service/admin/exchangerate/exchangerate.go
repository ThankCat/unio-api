// Package exchangerate 编排 Admin 汇率管理：最新/历史查询、手工录入兜底、API Key 运行时验证。
package exchangerate

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/fx"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

// SupportedQuotes 是当前维护汇率的目标币种（扩币种时与 providers CHECK、fx 拉取 quotes 一起扩）。
var SupportedQuotes = []string{"CNY"}

// Store 定义汇率管理所需存储能力，由 *sqlc.Queries 满足。
type Store interface {
	LatestExchangeRate(ctx context.Context, arg sqlc.LatestExchangeRateParams) (sqlc.ExchangeRate, error)
	ListExchangeRatesPage(ctx context.Context, arg sqlc.ListExchangeRatesPageParams) ([]sqlc.ExchangeRate, error)
	CountExchangeRates(ctx context.Context, quoteCurrency pgtype.Text) (int64, error)
	UpsertExchangeRate(ctx context.Context, arg sqlc.UpsertExchangeRateParams) (sqlc.ExchangeRate, error)
}

type Service struct {
	store       Store
	settings    *appsettings.SettingsStore
	envAPIKey   string
	probeClient *http.Client
}

// NewService 创建汇率管理 service；envAPIKey 是 EXCHANGE_RATE_API_KEY 的环境变量兜底值。
func NewService(store Store, settings *appsettings.SettingsStore, envAPIKey string) *Service {
	if store == nil {
		panic("exchangerate: store is required")
	}
	return &Service{store: store, settings: settings, envAPIKey: envAPIKey, probeClient: &http.Client{Timeout: 10 * time.Second}}
}

// Rate 是对外的汇率 DTO。
type Rate struct {
	ID            int64
	BaseCurrency  string
	QuoteCurrency string
	Rate          string
	RateDate      time.Time
	Source        string
	FetchedAt     time.Time
}

// LatestRate 是「最新汇率卡片」条目；Found=false 表示该币种对还没有任何汇率行。
type LatestRate struct {
	Rate
	Found    bool
	AgeHours float64
}

// Latest 返回每个受支持币种对的最新汇率（缺失时 Found=false，供前端提示先手工录入）。
func (s *Service) Latest(ctx context.Context) ([]LatestRate, error) {
	out := make([]LatestRate, 0, len(SupportedQuotes))
	for _, quote := range SupportedQuotes {
		row, err := s.store.LatestExchangeRate(ctx, sqlc.LatestExchangeRateParams{
			BaseCurrency:  fx.BaseCurrency,
			QuoteCurrency: quote,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			out = append(out, LatestRate{Rate: Rate{BaseCurrency: fx.BaseCurrency, QuoteCurrency: quote}, Found: false})
			continue
		}
		if err != nil {
			return nil, storeFailed(err, "load latest exchange rate")
		}
		out = append(out, LatestRate{
			Rate:     rateFrom(row),
			Found:    true,
			AgeHours: time.Since(row.FetchedAt.Time).Hours(),
		})
	}
	return out, nil
}

// ListParams 是汇率历史查询参数。
type ListParams struct {
	Quote  string
	Limit  int32
	Offset int32
}

// List 分页返回汇率历史。
func (s *Service) List(ctx context.Context, params ListParams) ([]Rate, int64, error) {
	if params.Limit <= 0 {
		params.Limit = 20
	}
	quote := opsutil.TextNarg(strings.ToUpper(strings.TrimSpace(params.Quote)))
	rows, err := s.store.ListExchangeRatesPage(ctx, sqlc.ListExchangeRatesPageParams{
		QuoteCurrency: quote,
		PageLimit:     params.Limit,
		PageOffset:    params.Offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "list exchange rates")
	}
	total, err := s.store.CountExchangeRates(ctx, quote)
	if err != nil {
		return nil, 0, storeFailed(err, "count exchange rates")
	}
	out := make([]Rate, 0, len(rows))
	for _, row := range rows {
		out = append(out, rateFrom(row))
	}
	return out, total, nil
}

// CreateManualParams 是手工录入参数（D7 第四层兜底：外部源全挂时运营查牌价手工录）。
type CreateManualParams struct {
	Quote string
	Rate  string
	// RateDate 缺省今天（UTC）。手工行只要日期不早于现有行即自然生效（消费口径按 rate_date 排序）。
	RateDate time.Time
}

// CreateManual 手工录入一条汇率。做区间合理性校验；刻意不做跳变校验——手工录入本身就是
// 人工判断，用于覆盖异常行情或修正错数据。
func (s *Service) CreateManual(ctx context.Context, params CreateManualParams) (Rate, error) {
	quote := strings.ToUpper(strings.TrimSpace(params.Quote))
	if !isSupportedQuote(quote) {
		return Rate{}, invalidArgument("quote_currency", "quote currency must be one of "+strings.Join(SupportedQuotes, "/"))
	}
	rateRat, ok := new(big.Rat).SetString(strings.TrimSpace(params.Rate))
	if !ok || rateRat.Sign() <= 0 {
		return Rate{}, invalidArgument("rate", "rate must be a positive decimal")
	}
	if err := fx.ValidateRate(quote, rateRat, nil); err != nil {
		return Rate{}, invalidArgument("rate", err.Error())
	}
	rateDate := params.RateDate
	if rateDate.IsZero() {
		rateDate = time.Now().UTC()
	}
	row, err := s.store.UpsertExchangeRate(ctx, sqlc.UpsertExchangeRateParams{
		BaseCurrency:  fx.BaseCurrency,
		QuoteCurrency: quote,
		Rate:          fx.NumericFromRat(rateRat, 10),
		RateDate:      pgtype.Date{Time: rateDate, Valid: true},
		Source:        "manual",
	})
	if err != nil {
		return Rate{}, storeFailed(err, "upsert manual exchange rate")
	}
	return rateFrom(row), nil
}

// ValidateKeyResult 是「验证 API Key」的返回：实时调用主源拿到的当前汇率。
type ValidateKeyResult struct {
	Quote    string
	Rate     string
	RateDate time.Time
	Source   string
}

// ValidateKey 实时调用 ExchangeRate-API 验证 key 可用性并回显当前汇率（PLAN 5.1 验证按钮）。
// overrideKey 非空时验证给定 key（改 key 前先验）；否则验证当前生效 key（设置表优先、环境变量兜底）。
// 只探测不落库——落库仍由 worker / 手工录入走合理性校验路径。
func (s *Service) ValidateKey(ctx context.Context, overrideKey string) (ValidateKeyResult, error) {
	key := strings.TrimSpace(overrideKey)
	if key == "" {
		key = s.effectiveKey(ctx)
	}
	if key == "" {
		return ValidateKeyResult{}, invalidArgument("api_key", "没有可验证的 API Key：系统设置与环境变量均为空")
	}
	source := &fx.ExchangeRateAPISource{
		Client: s.probeClient,
		APIKey: func(context.Context) string { return key },
	}
	quote := SupportedQuotes[0]
	result, err := source.FetchUSDRate(ctx, quote)
	if err != nil {
		return ValidateKeyResult{}, failure.Wrap(
			failure.CodeAdminInvalidArgument, err,
			failure.WithMessage("API Key 验证失败："+err.Error()),
			failure.WithField("field", "api_key"),
		)
	}
	return ValidateKeyResult{
		Quote:    quote,
		Rate:     result.Rate.FloatString(6),
		RateDate: result.Date,
		Source:   source.Name(),
	}, nil
}

func (s *Service) effectiveKey(ctx context.Context) string {
	if s.settings == nil {
		return s.envAPIKey
	}
	return appsettings.GatewayExchangeRateAPIKey(ctx, s.settings, s.envAPIKey)
}

func isSupportedQuote(quote string) bool {
	for _, q := range SupportedQuotes {
		if q == quote {
			return true
		}
	}
	return false
}

func rateFrom(row sqlc.ExchangeRate) Rate {
	return Rate{
		ID:            row.ID,
		BaseCurrency:  row.BaseCurrency,
		QuoteCurrency: row.QuoteCurrency,
		Rate:          opsutil.NumericString(row.Rate),
		RateDate:      row.RateDate.Time,
		Source:        row.Source,
		FetchedAt:     row.FetchedAt.Time,
	}
}

func invalidArgument(field, message string) error {
	return failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage(message), failure.WithField("field", field))
}

func storeFailed(err error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage(message))
}
