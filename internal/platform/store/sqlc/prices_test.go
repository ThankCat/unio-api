package sqlc_test

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// numeric 创建测试用 NUMERIC 参数，避免价格测试使用 float64。
func numeric(value int64) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(value), Valid: true}
}

// timestamptz 创建测试用 timestamptz 参数。
func timestamptz(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}

// nullTimestamptz 创建 SQL NULL timestamptz 参数。
func nullTimestamptz() pgtype.Timestamptz {
	return pgtype.Timestamptz{Valid: false}
}

// nullNumeric 创建 SQL NULL NUMERIC 参数。
func nullNumeric() pgtype.Numeric {
	return pgtype.Numeric{Valid: false}
}

// assertPriceNumericEquals 校验价格 NUMERIC 字段表示的金额值，忽略 PostgreSQL 返回的 scale 差异。
func assertPriceNumericEquals(t *testing.T, got pgtype.Numeric, want int64) {
	t.Helper()

	if !got.Valid {
		t.Fatalf("expected numeric %d to be valid", want)
	}
	if got.Int == nil {
		t.Fatal("expected numeric int to be set")
	}

	rat := new(big.Rat).SetInt(new(big.Int).Set(got.Int))
	if got.Exp > 0 {
		rat.Mul(rat, new(big.Rat).SetInt(pow10(got.Exp)))
	}
	if got.Exp < 0 {
		rat.Quo(rat, new(big.Rat).SetInt(pow10(-got.Exp)))
	}

	if rat.Cmp(big.NewRat(want, 1)) != 0 {
		t.Fatalf("expected numeric %d, got %s", want, rat.String())
	}
}

// priceParams 创建一组默认可用的测试价格参数。
func priceParams(modelID int64, now time.Time) sqlc.CreatePriceParams {
	return sqlc.CreatePriceParams{
		ModelID:              modelID,
		Currency:             "USD",
		PricingUnit:          "per_1m_tokens",
		InputPrice:           numeric(2),
		OutputPrice:          numeric(8),
		CachedInputPrice:     numeric(1),
		ReasoningOutputPrice: numeric(12),
		Status:               "enabled",
		EffectiveFrom:        timestamptz(now.Add(-time.Hour)),
		EffectiveTo:          nullTimestamptz(),
	}
}

// createPriceForTest 创建测试价格记录。
func createPriceForTest(t *testing.T, ctx context.Context, queries *sqlc.Queries, params sqlc.CreatePriceParams) sqlc.Price {
	t.Helper()

	price, err := queries.CreatePrice(ctx, params)
	if err != nil {
		t.Fatalf("create price: %v", err)
	}

	return price
}

// createPriceModelForTest 创建价格测试专用模型，并返回 models.id。
func createPriceModelForTest(t *testing.T, ctx context.Context, tx pgx.Tx, suffix int64) int64 {
	t.Helper()

	return insertModel(t, ctx, tx, fmt.Sprintf("price-model-%d", suffix), "openai", "enabled")
}

// isExclusionViolation 判断数据库错误是否是排他约束冲突。
func isExclusionViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23P01"
}

func TestFindActivePriceForModelFiltersAndOrders(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	now := time.Now().UTC()
	modelID := createPriceModelForTest(t, ctx, tx, now.UnixNano())

	disabled := priceParams(modelID, now)
	disabled.InputPrice = numeric(99)
	disabled.Status = "disabled"
	createPriceForTest(t, ctx, queries, disabled)

	expired := priceParams(modelID, now)
	expired.InputPrice = numeric(88)
	expired.EffectiveFrom = timestamptz(now.Add(-3 * time.Hour))
	expired.EffectiveTo = timestamptz(now.Add(-2 * time.Hour))
	createPriceForTest(t, ctx, queries, expired)

	future := priceParams(modelID, now)
	future.InputPrice = numeric(77)
	future.EffectiveFrom = timestamptz(now.Add(time.Hour))
	createPriceForTest(t, ctx, queries, future)

	active := priceParams(modelID, now)
	active.InputPrice = numeric(3)
	active.EffectiveFrom = timestamptz(now.Add(-30 * time.Minute))
	// active 与 future 都是 enabled 价格，受排他约束不能重叠：
	// 将 active 的生效窗口收口到 future 的开始时间，二者相邻不重叠。
	active.EffectiveTo = timestamptz(now.Add(time.Hour))
	want := createPriceForTest(t, ctx, queries, active)

	got, err := queries.FindActivePriceForModel(ctx, sqlc.FindActivePriceForModelParams{
		ModelID: modelID,
		AtTime:  timestamptz(now),
	})
	if err != nil {
		t.Fatalf("find active price: %v", err)
	}

	if got.ID != want.ID {
		t.Fatalf("expected latest active price id %d, got %d", want.ID, got.ID)
	}
	if got.Status != "enabled" {
		t.Fatalf("expected enabled price, got %q", got.Status)
	}
	assertPriceNumericEquals(t, got.InputPrice, 3)
}

func TestPriceRejectsOverlappingEnabledEffectiveWindow(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	now := time.Now().UTC()
	modelID := createPriceModelForTest(t, ctx, tx, now.UnixNano())

	first := priceParams(modelID, now)
	first.EffectiveFrom = timestamptz(now)
	first.EffectiveTo = timestamptz(now.Add(2 * time.Hour))
	createPriceForTest(t, ctx, queries, first)

	overlap := priceParams(modelID, now)
	overlap.EffectiveFrom = timestamptz(now.Add(time.Hour))
	overlap.EffectiveTo = timestamptz(now.Add(3 * time.Hour))
	_, err := queries.CreatePrice(ctx, overlap)
	if err == nil {
		t.Fatal("expected overlapping enabled price to be rejected")
	}
	if !isExclusionViolation(err) {
		t.Fatalf("expected exclusion violation, got %v", err)
	}
}

func TestPriceAllowsAdjacentEnabledEffectiveWindows(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	now := time.Now().UTC()
	modelID := createPriceModelForTest(t, ctx, tx, now.UnixNano())

	first := priceParams(modelID, now)
	first.EffectiveFrom = timestamptz(now)
	first.EffectiveTo = timestamptz(now.Add(time.Hour))
	createPriceForTest(t, ctx, queries, first)

	second := priceParams(modelID, now)
	second.InputPrice = numeric(3)
	second.EffectiveFrom = timestamptz(now.Add(time.Hour))
	second.EffectiveTo = timestamptz(now.Add(2 * time.Hour))
	want := createPriceForTest(t, ctx, queries, second)

	got, err := queries.FindActivePriceForModel(ctx, sqlc.FindActivePriceForModelParams{
		ModelID: modelID,
		AtTime:  timestamptz(now.Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("find active price at adjacent boundary: %v", err)
	}
	if got.ID != want.ID {
		t.Fatalf("expected second price id %d at adjacent boundary, got %d", want.ID, got.ID)
	}
}

func TestPriceAllowsDisabledOrDifferentScopeOverlap(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	now := time.Now().UTC()
	modelID := createPriceModelForTest(t, ctx, tx, now.UnixNano())
	otherModelID := createPriceModelForTest(t, ctx, tx, now.UnixNano()+1)

	first := priceParams(modelID, now)
	first.EffectiveFrom = timestamptz(now)
	first.EffectiveTo = timestamptz(now.Add(2 * time.Hour))
	createPriceForTest(t, ctx, queries, first)

	disabled := priceParams(modelID, now)
	disabled.Status = "disabled"
	disabled.EffectiveFrom = timestamptz(now.Add(time.Hour))
	disabled.EffectiveTo = timestamptz(now.Add(3 * time.Hour))
	createPriceForTest(t, ctx, queries, disabled)

	otherModel := priceParams(otherModelID, now)
	otherModel.EffectiveFrom = timestamptz(now.Add(time.Hour))
	otherModel.EffectiveTo = timestamptz(now.Add(3 * time.Hour))
	createPriceForTest(t, ctx, queries, otherModel)

	otherCurrency := priceParams(modelID, now)
	otherCurrency.Currency = "CNY"
	otherCurrency.EffectiveFrom = timestamptz(now.Add(time.Hour))
	otherCurrency.EffectiveTo = timestamptz(now.Add(3 * time.Hour))
	createPriceForTest(t, ctx, queries, otherCurrency)
}

func TestPriceRejectsInvalidConstraints(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*sqlc.CreatePriceParams, time.Time)
	}{
		{
			name: "invalid pricing unit",
			mutate: func(params *sqlc.CreatePriceParams, _ time.Time) {
				params.PricingUnit = "per_token"
			},
		},
		{
			name: "negative input price",
			mutate: func(params *sqlc.CreatePriceParams, _ time.Time) {
				params.InputPrice = numeric(-1)
			},
		},
		{
			name: "negative cached input price",
			mutate: func(params *sqlc.CreatePriceParams, _ time.Time) {
				params.CachedInputPrice = numeric(-1)
			},
		},
		{
			name: "invalid status",
			mutate: func(params *sqlc.CreatePriceParams, _ time.Time) {
				params.Status = "archived"
			},
		},
		{
			name: "invalid effective window",
			mutate: func(params *sqlc.CreatePriceParams, now time.Time) {
				params.EffectiveFrom = timestamptz(now)
				params.EffectiveTo = timestamptz(now)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, tx, queries, cleanup := newModelChannelTestTx(t)
			defer cleanup()

			now := time.Now().UTC()
			modelID := createPriceModelForTest(t, ctx, tx, now.UnixNano())
			params := priceParams(modelID, now)
			tc.mutate(&params, now)

			_, err := queries.CreatePrice(ctx, params)
			if err == nil {
				t.Fatal("expected check violation")
			}
			if !isCheckViolation(err) {
				t.Fatalf("expected check violation, got %v", err)
			}
		})
	}
}

func TestPriceSnapshotCreateGetAndRejectsDuplicateRequest(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	now := time.Now().UTC()
	modelID := createPriceModelForTest(t, ctx, tx, now.UnixNano())
	price := createPriceForTest(t, ctx, queries, priceParams(modelID, now))
	identity := createRequestRecordIdentity(t, ctx, queries)
	requestRecord := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("price-snapshot-%d", now.UnixNano()))

	created, err := queries.CreatePriceSnapshot(ctx, sqlc.CreatePriceSnapshotParams{
		RequestRecordID:      requestRecord.ID,
		PriceID:              pgtype.Int8{Int64: price.ID, Valid: true},
		Currency:             price.Currency,
		PricingUnit:          price.PricingUnit,
		InputPrice:           price.InputPrice,
		OutputPrice:          price.OutputPrice,
		CachedInputPrice:     price.CachedInputPrice,
		ReasoningOutputPrice: price.ReasoningOutputPrice,
		FormulaVersion:       "token_v1",
	})
	if err != nil {
		t.Fatalf("create price snapshot: %v", err)
	}

	if created.ID == 0 {
		t.Fatal("expected price snapshot id")
	}
	if created.RequestRecordID != requestRecord.ID {
		t.Fatalf("expected request_record_id %d, got %d", requestRecord.ID, created.RequestRecordID)
	}
	if !created.PriceID.Valid || created.PriceID.Int64 != price.ID {
		t.Fatalf("expected price_id %d, got valid=%v value=%d", price.ID, created.PriceID.Valid, created.PriceID.Int64)
	}
	if created.FormulaVersion != "token_v1" {
		t.Fatalf("expected formula version token_v1, got %q", created.FormulaVersion)
	}

	got, err := queries.GetPriceSnapshotByRequest(ctx, requestRecord.ID)
	if err != nil {
		t.Fatalf("get price snapshot by request: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("expected snapshot id %d, got %d", created.ID, got.ID)
	}

	_, err = queries.CreatePriceSnapshot(ctx, sqlc.CreatePriceSnapshotParams{
		RequestRecordID:      requestRecord.ID,
		PriceID:              pgtype.Int8{Int64: price.ID, Valid: true},
		Currency:             price.Currency,
		PricingUnit:          price.PricingUnit,
		InputPrice:           price.InputPrice,
		OutputPrice:          price.OutputPrice,
		CachedInputPrice:     price.CachedInputPrice,
		ReasoningOutputPrice: price.ReasoningOutputPrice,
		FormulaVersion:       "token_v1",
	})
	if err == nil {
		t.Fatal("expected duplicate request_record_id error")
	}
	if !isUniqueViolation(err) {
		t.Fatalf("expected unique violation, got %v", err)
	}
}

func TestPriceSnapshotKeepsCopiedPriceValues(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	now := time.Now().UTC()
	modelID := createPriceModelForTest(t, ctx, tx, now.UnixNano())
	price := createPriceForTest(t, ctx, queries, priceParams(modelID, now))
	identity := createRequestRecordIdentity(t, ctx, queries)
	requestRecord := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("price-snapshot-copy-%d", now.UnixNano()))

	created, err := queries.CreatePriceSnapshot(ctx, sqlc.CreatePriceSnapshotParams{
		RequestRecordID:      requestRecord.ID,
		PriceID:              pgtype.Int8{Int64: price.ID, Valid: true},
		Currency:             price.Currency,
		PricingUnit:          price.PricingUnit,
		InputPrice:           price.InputPrice,
		OutputPrice:          price.OutputPrice,
		CachedInputPrice:     nullNumeric(),
		ReasoningOutputPrice: nullNumeric(),
		FormulaVersion:       "token_v1",
	})
	if err != nil {
		t.Fatalf("create price snapshot: %v", err)
	}

	_, err = tx.Exec(ctx, `
		UPDATE prices
		SET input_price = $1, output_price = $2
		WHERE id = $3
	`, numeric(99), numeric(199), price.ID)
	if err != nil {
		t.Fatalf("update source price: %v", err)
	}

	got, err := queries.GetPriceSnapshotByRequest(ctx, requestRecord.ID)
	if err != nil {
		t.Fatalf("get price snapshot by request: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("expected snapshot id %d, got %d", created.ID, got.ID)
	}
	assertPriceNumericEquals(t, got.InputPrice, 2)
	assertPriceNumericEquals(t, got.OutputPrice, 8)
	if got.CachedInputPrice.Valid || got.ReasoningOutputPrice.Valid {
		t.Fatal("expected nullable specialized prices to remain null in snapshot")
	}
}
