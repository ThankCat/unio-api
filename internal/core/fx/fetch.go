package fx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"
)

// 多源拉取链（D7）：主源 ExchangeRate-API（需 key）→ open.er-api.com（免 key）→ Frankfurter（免 key）。
// 只被 worker 与 admin「验证 API Key」端点调用；解析用 json.Number 保留原始十进制文本，
// 全程不经过 float64，避免在入库前就丢precision。

const fetchResponseLimitBytes = 1 << 20 // 1MiB：汇率响应很小，限流防异常源撑爆内存。

// SourceResult 是一次外部源拉取的结果。
type SourceResult struct {
	Rate *big.Rat  // 1 USD 兑多少目标货币
	Date time.Time // 源标注的行情日期（UTC 日）
}

// Source 抽象一个外部汇率源。
type Source interface {
	Name() string
	FetchUSDRate(ctx context.Context, quote string) (SourceResult, error)
}

// Fetcher 按顺序尝试多个源，第一个成功即返回。
type Fetcher struct {
	Sources []Source
}

// Fetch 逐源拉取；全部失败时返回聚合错误。
func (f *Fetcher) Fetch(ctx context.Context, quote string) (SourceResult, string, error) {
	var errs []error
	for _, src := range f.Sources {
		result, err := src.FetchUSDRate(ctx, quote)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
			continue
		}
		return result, src.Name(), nil
	}
	return SourceResult{}, "", fmt.Errorf("fx: all sources failed: %w", errors.Join(errs...))
}

// ---- 主源：ExchangeRate-API（v6，需 API key，key 支持运行时热改） ----

// ExchangeRateAPISource 拉取 https://v6.exchangerate-api.com。
// APIKey 是函数而非字符串：key 走「app_settings 优先、环境变量兜底」双层解析（PLAN 5.1），
// 管理员运行时改 key 后下一次拉取即生效，无需重启。
type ExchangeRateAPISource struct {
	Client  *http.Client
	APIKey  func(ctx context.Context) string
	BaseURL string // 缺省 https://v6.exchangerate-api.com；测试注入用
}

func (s *ExchangeRateAPISource) Name() string { return "exchangerate-api" }

func (s *ExchangeRateAPISource) FetchUSDRate(ctx context.Context, quote string) (SourceResult, error) {
	key := ""
	if s.APIKey != nil {
		key = s.APIKey(ctx)
	}
	if key == "" {
		return SourceResult{}, errors.New("api key is not configured")
	}
	base := s.BaseURL
	if base == "" {
		base = "https://v6.exchangerate-api.com"
	}
	var payload struct {
		Result             string                 `json:"result"`
		ErrorType          string                 `json:"error-type"`
		TimeLastUpdateUnix int64                  `json:"time_last_update_unix"`
		ConversionRates    map[string]json.Number `json:"conversion_rates"`
	}
	if err := getJSON(ctx, s.Client, fmt.Sprintf("%s/v6/%s/latest/%s", base, key, BaseCurrency), &payload); err != nil {
		return SourceResult{}, err
	}
	if payload.Result != "success" {
		return SourceResult{}, fmt.Errorf("api result %q (error-type %q)", payload.Result, payload.ErrorType)
	}
	rate, err := rateFromJSONNumber(payload.ConversionRates[quote], quote)
	if err != nil {
		return SourceResult{}, err
	}
	date := time.Now().UTC()
	if payload.TimeLastUpdateUnix > 0 {
		date = time.Unix(payload.TimeLastUpdateUnix, 0).UTC()
	}
	return SourceResult{Rate: rate, Date: date.Truncate(24 * time.Hour)}, nil
}

// ---- 备源 1：open.er-api.com（同一供应商的免 key 端点） ----

type OpenERAPISource struct {
	Client  *http.Client
	BaseURL string
}

func (s *OpenERAPISource) Name() string { return "er-api" }

func (s *OpenERAPISource) FetchUSDRate(ctx context.Context, quote string) (SourceResult, error) {
	base := s.BaseURL
	if base == "" {
		base = "https://open.er-api.com"
	}
	var payload struct {
		Result             string                 `json:"result"`
		TimeLastUpdateUnix int64                  `json:"time_last_update_unix"`
		Rates              map[string]json.Number `json:"rates"`
	}
	if err := getJSON(ctx, s.Client, fmt.Sprintf("%s/v6/latest/%s", base, BaseCurrency), &payload); err != nil {
		return SourceResult{}, err
	}
	if payload.Result != "success" {
		return SourceResult{}, fmt.Errorf("api result %q", payload.Result)
	}
	rate, err := rateFromJSONNumber(payload.Rates[quote], quote)
	if err != nil {
		return SourceResult{}, err
	}
	date := time.Now().UTC()
	if payload.TimeLastUpdateUnix > 0 {
		date = time.Unix(payload.TimeLastUpdateUnix, 0).UTC()
	}
	return SourceResult{Rate: rate, Date: date.Truncate(24 * time.Hour)}, nil
}

// ---- 备源 2：Frankfurter（欧洲央行参考汇率，免 key） ----

type FrankfurterSource struct {
	Client  *http.Client
	BaseURL string
}

func (s *FrankfurterSource) Name() string { return "frankfurter" }

func (s *FrankfurterSource) FetchUSDRate(ctx context.Context, quote string) (SourceResult, error) {
	base := s.BaseURL
	if base == "" {
		base = "https://api.frankfurter.dev"
	}
	var payload struct {
		Date  string                 `json:"date"`
		Rates map[string]json.Number `json:"rates"`
	}
	url := fmt.Sprintf("%s/v1/latest?base=%s&symbols=%s", base, BaseCurrency, quote)
	if err := getJSON(ctx, s.Client, url, &payload); err != nil {
		return SourceResult{}, err
	}
	rate, err := rateFromJSONNumber(payload.Rates[quote], quote)
	if err != nil {
		return SourceResult{}, err
	}
	date := time.Now().UTC()
	if parsed, err := time.Parse("2006-01-02", payload.Date); err == nil {
		date = parsed
	}
	return SourceResult{Rate: rate, Date: date}, nil
}

// ---- 公共小工具 ----

func getJSON(ctx context.Context, client *http.Client, url string, out any) error {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, fetchResponseLimitBytes))
	decoder.UseNumber()
	return decoder.Decode(out)
}

// rateFromJSONNumber 把 JSON 数字的原始十进制文本直接转成 big.Rat（不经 float64）。
func rateFromJSONNumber(raw json.Number, quote string) (*big.Rat, error) {
	if raw == "" {
		return nil, fmt.Errorf("quote currency %s missing in response", quote)
	}
	rat, ok := new(big.Rat).SetString(raw.String())
	if !ok {
		return nil, fmt.Errorf("invalid rate value %q for %s", raw.String(), quote)
	}
	if rat.Sign() <= 0 {
		return nil, fmt.Errorf("non-positive rate %q for %s", raw.String(), quote)
	}
	return rat, nil
}
