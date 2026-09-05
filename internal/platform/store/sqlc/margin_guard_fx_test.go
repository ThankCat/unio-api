package sqlc_test

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// 跨币种毛利守卫（M5/D5）与一渠道一币种守卫（M4/D3）的 DB 级回归。
// 基线数据：createModelPriceForTest 的基准价 100/400 USD × 售价倍率 1 = 售价 100/400 USD；
// 汇率 1 USD = 7 CNY 时，售价折 CNY 为 700/2800，与 CNY 成本逐分项比较。

const currencyGuardConstraint = "ck_channel_price_currency_matches_provider"

func insertProviderWithCurrency(t *testing.T, ctx context.Context, tx pgx.Tx, slug, status, currency string) int64 {
	t.Helper()
	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO providers (slug, name, origin, status, currency)
		VALUES ($1, $2, 'https://' || $1 || '.example.test', $3, $4)
		RETURNING id
	`, slug, slug, status, currency).Scan(&id); err != nil {
		t.Fatalf("insert provider %q: %v", slug, err)
	}
	return id
}

func insertExchangeRateForTest(t *testing.T, ctx context.Context, tx pgx.Tx, quote, rate string, rateDate time.Time) {
	t.Helper()
	if _, err := tx.Exec(ctx, `
		INSERT INTO exchange_rates (base_currency, quote_currency, rate, rate_date, source)
		VALUES ('USD', $1, $2::numeric, $3::date, 'manual')
		ON CONFLICT (base_currency, quote_currency, rate_date, source)
		DO UPDATE SET rate = EXCLUDED.rate, fetched_at = now()
	`, quote, rate, rateDate); err != nil {
		t.Fatalf("insert exchange rate: %v", err)
	}
}

func createCNYChannelPrice(t *testing.T, ctx context.Context, queries *sqlc.Queries, channelID, modelID int64, at time.Time, inputCost, outputCost int64) {
	t.Helper()
	if _, err := queries.CreateChannelPrice(ctx, sqlc.CreateChannelPriceParams{
		ChannelID:         channelID,
		ModelID:           modelID,
		Currency:          "CNY",
		PricingUnit:       "per_1m_tokens",
		UncachedInputCost: numeric(inputCost),
		OutputCost:        numeric(outputCost),
		Status:            "enabled",
		EffectiveFrom:     timestamptz(at.Add(-time.Hour)),
		EffectiveTo:       nullTimestamptz(),
	}); err != nil {
		t.Fatalf("create CNY channel price: %v", err)
	}
}

// newCrossCurrencyFixture 铺一条 CNY provider → channel → model 的完整链路（不含渠道价格）。
func newCrossCurrencyFixture(t *testing.T, ctx context.Context, tx pgx.Tx, queries *sqlc.Queries, tag string) (channelID, modelID int64) {
	t.Helper()
	suffix := time.Now().UnixNano()
	providerID := insertProviderWithCurrency(t, ctx, tx, fmt.Sprintf("fx-%s-%d", tag, suffix), "enabled", "CNY")
	channelID = insertChannel(t, ctx, tx, providerID, fmt.Sprintf("fx-%s-ch-%d", tag, suffix), "enabled", 10, nil)
	modelID = insertModel(t, ctx, tx, fmt.Sprintf("openai/fx-%s-%d", tag, suffix), "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelID, fmt.Sprintf("fx-%s", tag), "enabled")
	createModelPriceForTest(t, ctx, queries, modelID, time.Now().UTC())
	return channelID, modelID
}

// 跨币种 + 汇率存在 + 盈利：售价 700/2800 CNY ≥ 成本 600/2500 CNY → 守卫放行。
func TestMarginGuardCrossCurrencyProfitablePasses(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	channelID, modelID := newCrossCurrencyFixture(t, ctx, tx, queries, "profit")
	insertExchangeRateForTest(t, ctx, tx, "CNY", "7", time.Now().UTC())
	createCNYChannelPrice(t, ctx, queries, channelID, modelID, time.Now().UTC(), 600, 2500)

	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		t.Fatalf("profitable cross-currency config must pass margin guard, got %v", err)
	}
}

// 跨币种 + 汇率存在 + 亏本：售价 700 CNY < 成本 800 CNY → 守卫拒绝，DETAIL 带币种与汇率。
func TestMarginGuardCrossCurrencyLossRejected(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	channelID, modelID := newCrossCurrencyFixture(t, ctx, tx, queries, "loss")
	insertExchangeRateForTest(t, ctx, tx, "CNY", "7", time.Now().UTC())
	createCNYChannelPrice(t, ctx, queries, channelID, modelID, time.Now().UTC(), 800, 2800)

	_, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != marginGuardConstraint {
		t.Fatalf("loss config must be rejected by %s, got %v", marginGuardConstraint, err)
	}
}

// 跨币种 + 无汇率：保守拒绝（缺汇率 = 违规），CNY 渠道价格根本配不出来（D11 时序自洽的根基）。
func TestMarginGuardCrossCurrencyMissingRateRejected(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	// 事务内清空汇率（回滚不影响真实数据）：开发库可能已被 fx-rate-sync worker 写入真实汇率。
	if _, err := tx.Exec(ctx, "DELETE FROM exchange_rates"); err != nil {
		t.Fatalf("clear exchange rates in tx: %v", err)
	}
	channelID, modelID := newCrossCurrencyFixture(t, ctx, tx, queries, "norate")
	createCNYChannelPrice(t, ctx, queries, channelID, modelID, time.Now().UTC(), 1, 4)

	_, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != marginGuardConstraint {
		t.Fatalf("missing fx rate must be rejected by %s, got %v", marginGuardConstraint, err)
	}
}

// 边界相等：售价 × 汇率 == 成本（700/2800 CNY）→ ≥ 语义放行。
func TestMarginGuardCrossCurrencyBoundaryEqualityPasses(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	channelID, modelID := newCrossCurrencyFixture(t, ctx, tx, queries, "edge")
	insertExchangeRateForTest(t, ctx, tx, "CNY", "7", time.Now().UTC())
	createCNYChannelPrice(t, ctx, queries, channelID, modelID, time.Now().UTC(), 700, 2800)

	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		t.Fatalf("boundary equality must pass (>= semantics), got %v", err)
	}
}

// 一渠道一币种（M4/D3）：USD provider 下录 CNY 渠道价格被币种一致性守卫拒绝。
func TestChannelPriceCurrencyMustMatchProvider(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	providerID := insertProviderWithCurrency(t, ctx, tx, fmt.Sprintf("fx-mismatch-%d", suffix), "enabled", "USD")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("fx-mismatch-ch-%d", suffix), "enabled", 10, nil)
	modelID := insertModel(t, ctx, tx, fmt.Sprintf("openai/fx-mismatch-%d", suffix), "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelID, "fx-mismatch", "enabled")
	createModelPriceForTest(t, ctx, queries, modelID, time.Now().UTC())
	insertExchangeRateForTest(t, ctx, tx, "CNY", "7", time.Now().UTC())
	createCNYChannelPrice(t, ctx, queries, channelID, modelID, time.Now().UTC(), 1, 4)

	_, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != currencyGuardConstraint {
		t.Fatalf("currency mismatch must be rejected by %s, got %v", currencyGuardConstraint, err)
	}
}

// 守卫取最新汇率：插入两天汇率（旧 7 新 8），成本 750/2900 CNY 在旧汇率下亏本、新汇率下盈利
// （售价折 CNY = 800/3200）→ 守卫必须用 rate_date 较新的那条放行。
func TestMarginGuardUsesLatestRate(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	channelID, modelID := newCrossCurrencyFixture(t, ctx, tx, queries, "latest")
	now := time.Now().UTC()
	insertExchangeRateForTest(t, ctx, tx, "CNY", "7", now.AddDate(0, 0, -1))
	insertExchangeRateForTest(t, ctx, tx, "CNY", "8", now)
	createCNYChannelPrice(t, ctx, queries, channelID, modelID, now, 750, 2900)

	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		t.Fatalf("guard must use the newest rate_date (8), got %v", err)
	}
}

// 倍率路径跨币种（D2 修订，000059）：CNY provider 的倍率成本按原币记账，
// 守卫分支 B 比较 sale_discount × rate ≥ multiplier × factor。
func TestMarginGuardRatioPathCrossCurrency(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	providerID := insertProviderWithCurrency(t, ctx, tx, fmt.Sprintf("fx-ratio-%d", suffix), "enabled", "CNY")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("fx-ratio-ch-%d", suffix), "enabled", 10, nil)
	modelID := insertModel(t, ctx, tx, fmt.Sprintf("openai/fx-ratio-%d", suffix), "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelID, "fx-ratio", "enabled")
	insertExchangeRateForTest(t, ctx, tx, "CNY", "7", time.Now().UTC())
	// 基准价 100/400 USD；售价倍率 0.04 → 折 CNY 侧 0.28；倍率成本 0.2 × 1.0 = 0.2 CNY/单位基准 → 盈利。
	createModelPriceForTest(t, ctx, queries, modelID, time.Now().UTC())
	if _, err := tx.Exec(ctx, `UPDATE model_prices SET sale_discount = 0.04 WHERE model_id = $1`, modelID); err != nil {
		t.Fatalf("set sale ratio: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO channel_cost_multipliers (channel_id, model_id, multiplier, status, effective_from)
		VALUES ($1, $2, 0.2, 'enabled', now() - interval '1 hour')
	`, channelID, modelID); err != nil {
		t.Fatalf("insert multiplier: %v", err)
	}
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		t.Fatalf("profitable ratio-path cross-currency must pass: %v", err)
	}

	// 售价倍率降到 0.02：0.02 × 7 = 0.14 < 0.2 → 分支 B 违规。
	_, err := tx.Exec(ctx, `UPDATE model_prices SET sale_discount = 0.02 WHERE model_id = $1`, modelID)
	if err == nil {
		_, err = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != marginGuardConstraint {
		t.Fatalf("loss ratio must be rejected by %s, got %v", marginGuardConstraint, err)
	}
}

// 倍率路径 + 无汇率：CNY provider 配倍率成本时若无汇率，守卫保守拒绝（D11 同语义）。
func TestMarginGuardRatioPathMissingRateRejected(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	if _, err := tx.Exec(ctx, "DELETE FROM exchange_rates"); err != nil {
		t.Fatalf("clear exchange rates in tx: %v", err)
	}
	suffix := time.Now().UnixNano()
	providerID := insertProviderWithCurrency(t, ctx, tx, fmt.Sprintf("fx-ratio-nr-%d", suffix), "enabled", "CNY")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("fx-ratio-nr-ch-%d", suffix), "enabled", 10, nil)
	modelID := insertModel(t, ctx, tx, fmt.Sprintf("openai/fx-ratio-nr-%d", suffix), "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelID, "fx-ratio-nr", "enabled")
	createModelPriceForTest(t, ctx, queries, modelID, time.Now().UTC())
	if _, err := tx.Exec(ctx, `
		INSERT INTO channel_cost_multipliers (channel_id, model_id, multiplier, status, effective_from)
		VALUES ($1, $2, 0.2, 'enabled', now() - interval '1 hour')
	`, channelID, modelID); err != nil {
		t.Fatalf("insert multiplier: %v", err)
	}

	_, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != marginGuardConstraint {
		t.Fatalf("ratio path without fx rate must be rejected by %s, got %v", marginGuardConstraint, err)
	}
}

// Go/SQL 双守卫等价性（§9.3）：同一组 (成本, 汇率) fixture 分别送入
// billing.ValidateNonNegativeMarginFX 与 margin_violations_current 视图，判定必须完全一致。
func TestMarginGuardGoSQLEquivalence(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	sale := billing.CustomerPriceSnapshot{
		Currency:           "USD",
		PricingUnit:        "per_1m_tokens",
		UncachedInputPrice: numeric(100),
		OutputPrice:        numeric(400),
		FormulaVersion:     billing.FormulaVersionV1,
	}
	rate := big.NewRat(7, 1)

	cases := []struct {
		name                  string
		inputCost, outputCost int64
		withRate              bool
	}{
		{"profitable", 600, 2500, true},
		{"loss_on_input", 800, 2800, true},
		{"loss_on_output", 600, 3000, true},
		{"boundary_equal", 700, 2800, true},
		{"missing_rate", 600, 2500, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			channelID, modelID := newCrossCurrencyFixture(t, ctx, tx, queries, "eq-"+tc.name)
			if tc.withRate {
				insertExchangeRateForTest(t, ctx, tx, "CNY", "7", time.Now().UTC())
			} else {
				if _, err := tx.Exec(ctx, "DELETE FROM exchange_rates"); err != nil {
					t.Fatalf("clear rates: %v", err)
				}
			}
			createCNYChannelPrice(t, ctx, queries, channelID, modelID, time.Now().UTC(), tc.inputCost, tc.outputCost)

			// SQL 侧判定：视图中该 (channel, model) 是否有违规行。
			var sqlViolations int
			if err := tx.QueryRow(ctx, `
				SELECT COUNT(*) FROM margin_violations_current WHERE channel_id = $1 AND model_id = $2
			`, channelID, modelID).Scan(&sqlViolations); err != nil {
				t.Fatalf("query violations view: %v", err)
			}
			sqlRejected := sqlViolations > 0

			// Go 侧判定：同一组数字送入 billing 守卫。
			cost := billing.ProviderCostSnapshot{
				Currency:          "CNY",
				PricingUnit:       "per_1m_tokens",
				UncachedInputCost: numeric(tc.inputCost),
				OutputCost:        numeric(tc.outputCost),
				FormulaVersion:    billing.FormulaVersionV1,
			}
			var goRejected bool
			var fxRate *big.Rat
			if tc.withRate {
				fxRate = rate
			}
			violations, err := billing.ValidateNonNegativeMarginFX(sale, cost, fxRate)
			if err != nil {
				if !errors.Is(err, billing.ErrMissingFxRate) {
					t.Fatalf("go guard error: %v", err)
				}
				goRejected = true
			} else {
				goRejected = len(violations) > 0
			}

			if goRejected != sqlRejected {
				t.Fatalf("Go/SQL guard verdicts diverged: go=%v sql=%v (case %s)", goRejected, sqlRejected, tc.name)
			}

			// 清理本子用例的配置，避免影响下一个子用例的视图判定（同一事务内顺序执行）。
			if _, err := tx.Exec(ctx, `UPDATE channel_prices SET status = 'disabled' WHERE channel_id = $1`, channelID); err != nil {
				t.Fatalf("disable channel price: %v", err)
			}
		})
	}
	// missing_rate 子用例在事务内清空了汇率表；最终校验点前恢复一条，
	// 避免误伤库中真实存在的 CNY 倍率路径配置（000059 后它们的守卫校验依赖汇率）。
	insertExchangeRateForTest(t, ctx, tx, "CNY", "7", time.Now().UTC())
	// 收尾恢复约束校验点：让 deferred 守卫在（已全部停用的）终态上通过。
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		t.Fatalf("final constraint check: %v", err)
	}
}
