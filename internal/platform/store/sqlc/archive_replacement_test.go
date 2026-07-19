package sqlc_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func TestArchiveChannelWithReplacementKeepsFixedRouteNonEmpty(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()
	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("archive-channel-provider-%d", suffix), "enabled")
	targetID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("archive-target-%d", suffix), "enabled", 1, nil)
	replacementID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("archive-replacement-%d", suffix), "enabled", 2, nil)
	route, err := queries.CreateRoute(ctx, sqlc.CreateRouteParams{
		Name: fmt.Sprintf("archive-fixed-route-%d", suffix), Mode: "fixed", Status: "enabled", PriceRatio: numeric(2),
	})
	if err != nil {
		t.Fatalf("create fixed route: %v", err)
	}
	if err := queries.AddRouteChannel(ctx, sqlc.AddRouteChannelParams{RouteID: route.ID, ChannelID: targetID}); err != nil {
		t.Fatalf("bind target channel: %v", err)
	}
	affected, err := queries.ArchiveChannelWithReplacement(ctx, sqlc.ArchiveChannelWithReplacementParams{
		ID: targetID, ReplacementChannelID: replacementID,
	})
	if err != nil || affected != 1 {
		t.Fatalf("replace and archive channel: affected=%d err=%v", affected, err)
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM channels WHERE id=$1`, targetID).Scan(&status); err != nil || status != "archived" {
		t.Fatalf("target channel not archived: status=%q err=%v", status, err)
	}
	var count int64
	var onlyChannelID int64
	if err := tx.QueryRow(ctx, `SELECT COUNT(*), MIN(channel_id) FROM route_channels WHERE route_id=$1`, route.ID).Scan(&count, &onlyChannelID); err != nil {
		t.Fatalf("read final route pool: %v", err)
	}
	if count != 1 || onlyChannelID != replacementID {
		t.Fatalf("fixed route must contain only replacement: count=%d channel=%d", count, onlyChannelID)
	}
}

func TestArchiveProviderWithReplacementKeepsRouteNonEmpty(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()
	suffix := time.Now().UnixNano()
	targetProviderID := insertProvider(t, ctx, tx, fmt.Sprintf("archive-provider-target-%d", suffix), "enabled")
	replacementProviderID := insertProvider(t, ctx, tx, fmt.Sprintf("archive-provider-replacement-%d", suffix), "enabled")
	targetChannelID := insertChannel(t, ctx, tx, targetProviderID, fmt.Sprintf("archive-provider-channel-%d", suffix), "enabled", 1, nil)
	replacementChannelID := insertChannel(t, ctx, tx, replacementProviderID, fmt.Sprintf("archive-provider-replacement-channel-%d", suffix), "enabled", 2, nil)
	route, err := queries.CreateRoute(ctx, sqlc.CreateRouteParams{
		Name: fmt.Sprintf("archive-provider-route-%d", suffix), Mode: "balanced", Status: "enabled", PriceRatio: numeric(2),
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	if err := queries.AddRouteChannel(ctx, sqlc.AddRouteChannelParams{RouteID: route.ID, ChannelID: targetChannelID}); err != nil {
		t.Fatalf("bind target channel: %v", err)
	}
	affected, err := queries.ArchiveProviderWithReplacement(ctx, sqlc.ArchiveProviderWithReplacementParams{
		ID: targetProviderID, ReplacementChannelID: replacementChannelID,
	})
	if err != nil || affected != 1 {
		t.Fatalf("replace and archive provider: affected=%d err=%v", affected, err)
	}
	var providerStatus, channelStatus string
	if err := tx.QueryRow(ctx, `SELECT p.status, c.status FROM providers p JOIN channels c ON c.provider_id=p.id WHERE p.id=$1`, targetProviderID).Scan(&providerStatus, &channelStatus); err != nil {
		t.Fatalf("read archived provider/channel: %v", err)
	}
	if providerStatus != "archived" || channelStatus != "archived" {
		t.Fatalf("unexpected archive states: provider=%s channel=%s", providerStatus, channelStatus)
	}
	var count int64
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM route_channels WHERE route_id=$1 AND channel_id=$2`, route.ID, replacementChannelID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replacement missing from route: count=%d err=%v", count, err)
	}
}
