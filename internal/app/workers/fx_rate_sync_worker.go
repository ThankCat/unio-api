package workers

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/fx"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/adminmessage"
)

const (
	// fxRateSyncDefaultPollInterval 限制 due 检查频率，空闲时不高频查库。
	fxRateSyncDefaultPollInterval = time.Minute
	// fxRateSyncFailureAlertThreshold 连续拉取失败达到该次数后写站内告警。
	fxRateSyncFailureAlertThreshold = 2
	fxRateSyncBaseRetryBackoff      = 5 * time.Minute
	fxRateSyncMaxRetryBackoff       = time.Hour
	// 陈旧分级阈值（D7）：>24h 提醒、>72h 需人工介入（手工录入兜底）。
	fxRateStaleWarnAge     = 24 * time.Hour
	fxRateStaleCriticalAge = 72 * time.Hour
)

// FxRateSyncStore 定义汇率同步所需的存储能力，由 *sqlc.Queries 满足。
type FxRateSyncStore interface {
	LatestExchangeRate(ctx context.Context, arg sqlc.LatestExchangeRateParams) (sqlc.ExchangeRate, error)
	UpsertExchangeRate(ctx context.Context, arg sqlc.UpsertExchangeRateParams) (sqlc.ExchangeRate, error)
}

// FxMessagePublisher 把告警写进 admin 消息中心（§12.C.3 告警通道）。
type FxMessagePublisher interface {
	Publish(ctx context.Context, params adminmessage.PublishParams) (bool, error)
}

// FxRateSyncWorker 定时从外部源拉取汇率（D7 四层容错的「多源 + 合理性校验」两层在此实现）：
// 到期才拉、主源失败走备源、垃圾数据入库前拒收、连续失败与陈旧汇率写站内告警。
// 任何请求路径都不依赖本 worker 的实时性——结算取最近可用值，最坏是陈旧（可接受）。
type FxRateSyncWorker struct {
	store    FxRateSyncStore
	fetcher  *fx.Fetcher
	messages FxMessagePublisher
	logger   *zap.Logger
	interval time.Duration
	quotes   []string
	now      func() time.Time

	nextPollAt          time.Time
	retryNotBefore      time.Time
	consecutiveFailures int
}

// NewFxRateSyncWorker 创建汇率同步 worker；quotes 是需要维护的目标币种（当前 ["CNY"]）。
func NewFxRateSyncWorker(store FxRateSyncStore, fetcher *fx.Fetcher, messages FxMessagePublisher, logger *zap.Logger, interval time.Duration, quotes []string) *FxRateSyncWorker {
	if store == nil {
		panic("workers: fx rate sync store is required")
	}
	if fetcher == nil {
		panic("workers: fx fetcher is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	if len(quotes) == 0 {
		quotes = []string{"CNY"}
	}
	return &FxRateSyncWorker{
		store:    store,
		fetcher:  fetcher,
		messages: messages,
		logger:   logger,
		interval: interval,
		quotes:   quotes,
		now:      time.Now,
	}
}

// Name 返回 worker 名称。
func (w *FxRateSyncWorker) Name() string { return "fx_rate_sync" }

// RunOnce 到期时逐币种拉取一次汇率。
func (w *FxRateSyncWorker) RunOnce(ctx context.Context) (bool, error) {
	now := w.now()
	if now.Before(w.nextPollAt) {
		return false, nil
	}
	w.nextPollAt = now.Add(fxRateSyncDefaultPollInterval)
	if now.Before(w.retryNotBefore) {
		return false, nil
	}

	worked := false
	for _, quote := range w.quotes {
		didWork := w.syncQuote(ctx, quote, now)
		worked = worked || didWork
	}
	return worked, nil
}

func (w *FxRateSyncWorker) syncQuote(ctx context.Context, quote string, now time.Time) bool {
	latest, err := w.store.LatestExchangeRate(ctx, sqlc.LatestExchangeRateParams{
		BaseCurrency:  fx.BaseCurrency,
		QuoteCurrency: quote,
	})
	hasLatest := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		w.logger.Error("fx rate sync: load latest rate failed", append([]zap.Field{zap.String("quote", quote)}, failure.LogFields(err)...)...)
		return false
	}

	// 陈旧分级告警（D7）：与拉取是否到期无关，只要最新行太旧就提醒（dedupe 防轰炸，读掉即可再触发）。
	if hasLatest {
		w.alertStaleness(ctx, quote, now.Sub(latest.FetchedAt.Time))
		if now.Sub(latest.FetchedAt.Time) < w.interval {
			return false // 未到期（手工录入也计入新鲜度，天然充当兜底）。
		}
	}

	result, source, err := w.fetcher.Fetch(ctx, quote)
	if err != nil {
		w.consecutiveFailures++
		w.retryNotBefore = now.Add(w.retryBackoff())
		fields := append([]zap.Field{
			zap.String("worker", w.Name()), zap.String("quote", quote),
			zap.Int("consecutive_failures", w.consecutiveFailures),
		}, failure.LogFields(err)...)
		if w.consecutiveFailures >= fxRateSyncFailureAlertThreshold {
			fields = append(fields, zap.String("alert", "fx_rate_sync_consecutive_failures"))
			w.logger.Error("fx rate sync repeated failure", fields...)
			w.publish(ctx, adminmessage.PublishParams{
				Severity:  adminmessage.SeverityWarning,
				Topic:     "fx_rate",
				Title:     fmt.Sprintf("汇率拉取连续 %d 次失败（USD/%s）", w.consecutiveFailures, quote),
				Body:      fmt.Sprintf("全部汇率源拉取失败，当前沿用最近一次可用汇率。最后错误：%v", err),
				Source:    w.Name(),
				DedupeKey: "fx-fetch-fail-" + quote,
			})
		} else {
			w.logger.Warn("fx rate sync failed", fields...)
		}
		return true
	}

	// 合理性校验（D7）：错数据比宕机更危险，入库前拒收并告警，继续沿用旧值。
	if err := fx.ValidateRate(quote, result.Rate, latestRat(latest, hasLatest)); err != nil {
		w.retryNotBefore = now.Add(fxRateSyncBaseRetryBackoff)
		w.logger.Error("fx rate sync rejected by sanity check",
			zap.String("quote", quote), zap.String("source", source), zap.Error(err))
		w.publish(ctx, adminmessage.PublishParams{
			Severity:  adminmessage.SeverityWarning,
			Topic:     "fx_rate",
			Title:     fmt.Sprintf("汇率被合理性校验拒收（USD/%s，来源 %s）", quote, source),
			Body:      fmt.Sprintf("新汇率未通过合理性校验，已拒收并沿用旧值，请人工核实行情（必要时手工录入覆盖）。原因：%v", err),
			Source:    w.Name(),
			DedupeKey: "fx-rate-rejected-" + quote,
		})
		return true
	}

	if _, err := w.store.UpsertExchangeRate(ctx, sqlc.UpsertExchangeRateParams{
		BaseCurrency:  fx.BaseCurrency,
		QuoteCurrency: quote,
		Rate:          fx.NumericFromRat(result.Rate, 10),
		RateDate:      pgtype.Date{Time: result.Date, Valid: true},
		Source:        source,
	}); err != nil {
		w.logger.Error("fx rate sync: upsert failed", append([]zap.Field{zap.String("quote", quote)}, failure.LogFields(err)...)...)
		return true
	}

	w.consecutiveFailures = 0
	w.retryNotBefore = time.Time{}
	w.logger.Info("fx rate synced",
		zap.String("quote", quote), zap.String("source", source),
		zap.String("rate", result.Rate.FloatString(6)), zap.String("rate_date", result.Date.Format("2006-01-02")))
	return true
}

func (w *FxRateSyncWorker) alertStaleness(ctx context.Context, quote string, age time.Duration) {
	switch {
	case age > fxRateStaleCriticalAge:
		w.publish(ctx, adminmessage.PublishParams{
			Severity:  adminmessage.SeverityCritical,
			Topic:     "fx_rate",
			Title:     fmt.Sprintf("汇率已超过 72 小时未更新（USD/%s）", quote),
			Body:      fmt.Sprintf("最新汇率已陈旧 %.0f 小时，请人工核实外部源并在汇率页手工录入当日汇率兜底。", age.Hours()),
			Source:    w.Name(),
			DedupeKey: "fx-stale-72h-" + quote,
		})
	case age > fxRateStaleWarnAge:
		w.publish(ctx, adminmessage.PublishParams{
			Severity:  adminmessage.SeverityWarning,
			Topic:     "fx_rate",
			Title:     fmt.Sprintf("汇率超过 24 小时未更新（USD/%s）", quote),
			Body:      fmt.Sprintf("最新汇率已陈旧 %.0f 小时，毛利估算仍在使用旧值（可接受但需留意）。", age.Hours()),
			Source:    w.Name(),
			DedupeKey: "fx-stale-24h-" + quote,
		})
	}
}

func (w *FxRateSyncWorker) publish(ctx context.Context, params adminmessage.PublishParams) {
	if w.messages == nil {
		return
	}
	if _, err := w.messages.Publish(ctx, params); err != nil {
		w.logger.Error("fx rate sync: publish admin message failed", failure.LogFields(err)...)
	}
}

func (w *FxRateSyncWorker) retryBackoff() time.Duration {
	shift := w.consecutiveFailures - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 4 {
		shift = 4
	}
	backoff := fxRateSyncBaseRetryBackoff * time.Duration(1<<shift)
	if backoff > fxRateSyncMaxRetryBackoff {
		backoff = fxRateSyncMaxRetryBackoff
	}
	return backoff
}

// latestRat 把最新汇率行转成 big.Rat 供跳变校验；无历史行或值异常时返回 nil（跳过跳变检查）。
func latestRat(latest sqlc.ExchangeRate, has bool) *big.Rat {
	if !has {
		return nil
	}
	rat, err := fx.RatFromNumeric(latest.Rate)
	if err != nil {
		return nil
	}
	return rat
}
