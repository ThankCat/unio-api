package sqlc_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestCreateModelPriceRequiresReplacementConfirmation(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	modelID := insertModel(
		t,
		ctx,
		tx,
		fmt.Sprintf("model-price-overlap-%d", time.Now().UnixNano()),
		"openai",
		"enabled",
	)
	from := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	if _, err := queries.CreateModelPrice(ctx, modelPriceParams(modelID, from, 1, 4)); err != nil {
		t.Fatalf("create existing model price: %v", err)
	}

	_, err := queries.CreateModelPrice(ctx, modelPriceParams(modelID, from.Add(time.Minute), 2, 8))
	if err == nil {
		t.Fatal("overlapping enabled price without confirmation must fail")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23P01" {
		t.Fatalf("overlap error = %v, want PostgreSQL 23P01", err)
	}
}

func TestCreateModelPriceAtomicallyReplacesEnabledWindow(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	modelID := insertModel(
		t,
		ctx,
		tx,
		fmt.Sprintf("model-price-replace-%d", time.Now().UnixNano()),
		"openai",
		"enabled",
	)
	oldFrom := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	oldPrice, err := queries.CreateModelPrice(ctx, modelPriceParams(modelID, oldFrom, 1, 4))
	if err != nil {
		t.Fatalf("create existing model price: %v", err)
	}

	newFrom := oldFrom.Add(30 * time.Minute)
	replacement := modelPriceParams(modelID, newFrom, 2, 8)
	replacement.ReplaceOverlappingEnabled = true
	replacement.LongContextEnabled = true
	replacement.LongContextThreshold = pgtype.Int8{Int64: 272000, Valid: true}
	replacement.LongContextInputMultiplier = numeric(2)
	replacement.LongContextOutputMultiplier = numeric(3)
	replacement.FastConfigured = true
	replacement.FastUncachedInputPrice = numeric(5)
	replacement.FastOutputPrice = numeric(20)

	created, err := queries.CreateModelPrice(ctx, replacement)
	if err != nil {
		t.Fatalf("replace model price: %v", err)
	}
	if created.Status != "enabled" || !created.LongContextEnabled {
		t.Fatalf("replacement facts = status %q long_context %t", created.Status, created.LongContextEnabled)
	}
	if created.FastServiceTierID == 0 || !created.FastUncachedInputPrice.Valid || !created.FastOutputPrice.Valid {
		t.Fatalf("replacement Fast price was not created: %+v", created)
	}

	replaced, err := queries.GetModelPrice(ctx, oldPrice.ID)
	if err != nil {
		t.Fatalf("reload replaced model price: %v", err)
	}
	if replaced.Status != "disabled" {
		t.Fatalf("replaced status = %q, want disabled", replaced.Status)
	}
	if !replaced.EffectiveTo.Valid || !replaced.EffectiveTo.Time.Equal(newFrom) {
		t.Fatalf("replaced effective_to = %v, want %s", replaced.EffectiveTo, newFrom)
	}
}

func modelPriceParams(
	modelID int64,
	from time.Time,
	input int64,
	output int64,
) sqlc.CreateModelPriceParams {
	return sqlc.CreateModelPriceParams{
		ModelID:                     modelID,
		EffectiveFrom:               timestamptz(from),
		Status:                      "enabled",
		Currency:                    "USD",
		PricingUnit:                 "per_1m_tokens",
		EffectiveTo:                 nullTimestamptz(),
		UncachedInputPrice:          numeric(input),
		OutputPrice:                 numeric(output),
		CacheReadInputPrice:         pgtype.Numeric{},
		CacheWrite5mInputPrice:      pgtype.Numeric{},
		CacheWrite1hInputPrice:      pgtype.Numeric{},
		CacheWrite30mInputPrice:     pgtype.Numeric{},
		ReasoningOutputPrice:        pgtype.Numeric{},
		FastUncachedInputPrice:      pgtype.Numeric{},
		FastCacheReadInputPrice:     pgtype.Numeric{},
		FastCacheWrite5mInputPrice:  pgtype.Numeric{},
		FastCacheWrite1hInputPrice:  pgtype.Numeric{},
		FastCacheWrite30mInputPrice: pgtype.Numeric{},
		FastOutputPrice:             pgtype.Numeric{},
		FastReasoningOutputPrice:    pgtype.Numeric{},
	}
}
