package sqlc_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestRoutingMarginGuardAcceptsSafeConfiguration(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("margin-safe-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("margin-safe-channel-%d", suffix), "enabled", 1, nil)
	modelID := insertModel(t, ctx, tx, fmt.Sprintf("openai/margin-safe-%d", suffix), "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelID, "margin-safe", "enabled")
	now := time.Now().UTC()
	createModelPriceForTest(t, ctx, queries, modelID, now)
	createChannelPriceForTest(t, ctx, queries, channelID, modelID, now)
	insertRouteWithChannels(t, ctx, tx, channelID)

	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		t.Fatalf("safe margin configuration rejected: %v", err)
	}
}

func TestRoutingMarginGuardRejectsNegativeComponent(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("margin-negative-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("margin-negative-channel-%d", suffix), "enabled", 1, nil)
	modelID := insertModel(t, ctx, tx, fmt.Sprintf("openai/margin-negative-%d", suffix), "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelID, "margin-negative", "enabled")
	now := time.Now().UTC()
	createModelPriceForTest(t, ctx, queries, modelID, now)
	createChannelPriceForTest(t, ctx, queries, channelID, modelID, now)
	routeID := insertRouteWithChannels(t, ctx, tx, channelID)
	if _, err := tx.Exec(ctx, "UPDATE routes SET price_ratio = 0.1 WHERE id = $1", routeID); err != nil {
		t.Fatalf("stage unsafe route ratio: %v", err)
	}

	_, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "ck_non_negative_route_margin" {
		t.Fatalf("expected negative-margin constraint, got %v", err)
	}
}
