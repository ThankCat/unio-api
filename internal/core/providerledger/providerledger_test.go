package providerledger

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	coreusage "github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func providerLedgerNumeric(t *testing.T, value string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(value); err != nil {
		t.Fatalf("scan numeric %q: %v", value, err)
	}
	return n
}

func providerLedgerDeps(t *testing.T) (context.Context, *pgxpool.Pool, *Service, int64) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		cancel()
		t.Fatalf("create postgres pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		cancel()
		t.Fatalf("ping postgres: %v", err)
	}
	suffix := time.Now().UnixNano()
	var providerID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO providers (slug, name, origin, status)
		VALUES ($1, $2, $3, 'enabled') RETURNING id
	`, fmt.Sprintf("provider-ledger-%d", suffix), "Provider Ledger Test", fmt.Sprintf("https://provider-ledger-%d.example", suffix)).Scan(&providerID); err != nil {
		pool.Close()
		cancel()
		t.Fatalf("insert provider: %v", err)
	}
	cleanup := func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM provider_ledger_entries WHERE provider_id = $1`, providerID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM provider_probe_records WHERE provider_id = $1`, providerID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM provider_balances WHERE provider_id = $1`, providerID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM providers WHERE id = $1`, providerID)
		pool.Close()
		cancel()
	}
	t.Cleanup(cleanup)
	return ctx, pool, NewService(pool, sqlc.New(pool)), providerID
}

func TestProviderLedgerSetsExactTargetBalance(t *testing.T) {
	ctx, _, service, providerID := providerLedgerDeps(t)
	entry, err := service.SetTargetBalance(ctx, TargetParams{
		ProviderID: providerID, TargetBalance: providerLedgerNumeric(t, "1.79"), Currency: "USD",
		IdempotencyKey: fmt.Sprintf("provider-target-%d", time.Now().UnixNano()), Reason: "upstream reconciliation",
	})
	if err != nil {
		t.Fatalf("set target balance: %v", err)
	}
	if entry.EntryType != EntryTypeAdjustmentCredit {
		t.Fatalf("expected credit entry, got %q", entry.EntryType)
	}
	assertProviderLedgerNumeric(t, entry.BalanceBefore, "0")
	assertProviderLedgerNumeric(t, entry.Amount, "1.79")
	assertProviderLedgerNumeric(t, entry.BalanceAfter, "1.79")

	entry, err = service.SetTargetBalance(ctx, TargetParams{
		ProviderID: providerID, TargetBalance: providerLedgerNumeric(t, "-0.25"), Currency: "USD",
		IdempotencyKey: fmt.Sprintf("provider-target-negative-%d", time.Now().UnixNano()), Reason: "upstream reconciliation",
	})
	if err != nil {
		t.Fatalf("set negative target balance: %v", err)
	}
	if entry.EntryType != EntryTypeAdjustmentDebit {
		t.Fatalf("expected debit entry, got %q", entry.EntryType)
	}
	assertProviderLedgerNumeric(t, entry.Amount, "2.04")
	assertProviderLedgerNumeric(t, entry.BalanceAfter, "-0.25")
}

func TestProviderProbeAccountingOnlyDebitsKnownCost(t *testing.T) {
	ctx, pool, service, providerID := providerLedgerDeps(t)
	suffix := time.Now().UnixNano()
	var modelID, channelID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO models (model_id, display_name, owned_by, status)
		VALUES ($1, 'Probe Model', 'test', 'enabled') RETURNING id
	`, fmt.Sprintf("provider-probe-model-%d", suffix)).Scan(&modelID); err != nil {
		t.Fatalf("insert model: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO channels (provider_id, name, protocol, adapter_key, credential, status, priority)
		VALUES ($1, $2, 'openai', 'openai', 'test-secret', 'enabled', 0) RETURNING id
	`, providerID, fmt.Sprintf("provider-probe-channel-%d", suffix)).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO channel_models (channel_id, model_id, upstream_model, status)
		VALUES ($1, $2, 'probe-upstream-model', 'enabled')
	`, channelID, modelID); err != nil {
		t.Fatalf("insert channel model: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO channel_prices (
			channel_id, model_id, currency, pricing_unit, uncached_input_cost, output_cost, status, effective_from
		) VALUES ($1, $2, 'USD', 'per_1m_tokens', 1, 2, 'enabled', now() - interval '1 minute')
	`, channelID, modelID); err != nil {
		t.Fatalf("insert channel price: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM provider_ledger_entries WHERE provider_id = $1`, providerID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM provider_probe_records WHERE provider_id = $1`, providerID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM channel_prices WHERE channel_id = $1`, channelID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM channel_models WHERE channel_id = $1`, channelID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM channels WHERE id = $1`, channelID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM models WHERE id = $1`, modelID)
	})

	facts := &adapter.ResponseFacts{
		UsageSource: coreusage.SourceUpstreamResponse,
		Usage: coreusage.Facts{
			UncachedInputTokens: coreusage.KnownTokens(1000), CacheReadInputTokens: coreusage.NotApplicableTokens(),
			CacheWrite5mInputTokens: coreusage.NotApplicableTokens(), CacheWrite1hInputTokens: coreusage.NotApplicableTokens(),
			CacheWrite30mInputTokens: coreusage.NotApplicableTokens(), OutputTokensTotal: coreusage.KnownTokens(500),
			ReasoningOutputTokens: coreusage.NotApplicableTokens(),
		},
	}
	if err := service.AccountProbe(ctx, ProbeParams{
		ProviderID: providerID, ChannelID: channelID, ModelID: modelID, Protocol: "openai", Source: "manual",
		UpstreamModel: "probe-upstream-model", Success: true, HTTPStatus: 200, LatencyMs: 10,
		Facts: facts, IdempotencyKey: fmt.Sprintf("provider-probe-success-%d", suffix),
	}); err != nil {
		t.Fatalf("account reliable probe: %v", err)
	}
	var balance pgtype.Numeric
	if err := pool.QueryRow(ctx, `SELECT balance FROM provider_balances WHERE provider_id = $1 AND currency = 'USD'`, providerID).Scan(&balance); err != nil {
		t.Fatalf("get probe balance: %v", err)
	}
	assertProviderLedgerNumeric(t, balance, "-0.002")

	if err := service.AccountProbe(ctx, ProbeParams{
		ProviderID: providerID, ChannelID: channelID, ModelID: modelID, Protocol: "openai", Source: "manual",
		UpstreamModel: "probe-upstream-model", Success: false, HTTPStatus: 502, ErrorCode: "upstream_error",
		Message: "upstream failed", LatencyMs: 20, IdempotencyKey: fmt.Sprintf("provider-probe-failed-%d", suffix),
	}); err != nil {
		t.Fatalf("account failed probe: %v", err)
	}
	var probeDebits int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM provider_ledger_entries WHERE provider_id = $1 AND entry_type = 'probe_debit'`, providerID).Scan(&probeDebits); err != nil {
		t.Fatalf("count probe debits: %v", err)
	}
	if probeDebits != 1 {
		t.Fatalf("expected failed probe to keep one debit, got %d", probeDebits)
	}

	if err := service.AccountProbe(ctx, ProbeParams{
		ProviderID: providerID, ChannelID: channelID, ModelID: modelID, Protocol: "openai", Source: "manual",
		UpstreamModel: "probe-upstream-model", Success: false, HTTPStatus: 200, ErrorCode: "protocol_error",
		Message:   "upstream response missing required usage",
		LatencyMs: 15, IdempotencyKey: fmt.Sprintf("provider-probe-missing-usage-%d", suffix),
	}); err != nil {
		t.Fatalf("account 2xx probe without usage: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM provider_ledger_entries WHERE provider_id = $1 AND entry_type = 'probe_debit'`, providerID).Scan(&probeDebits); err != nil {
		t.Fatalf("count probe debits after missing usage: %v", err)
	}
	if probeDebits != 1 {
		t.Fatalf("missing usage must not create a probe debit, got %d", probeDebits)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM channel_prices WHERE channel_id = $1`, channelID); err != nil {
		t.Fatalf("remove probe pricing: %v", err)
	}
	noPriceKey := fmt.Sprintf("provider-probe-no-price-%d", suffix)
	if err := service.AccountProbe(ctx, ProbeParams{
		ProviderID: providerID, ChannelID: channelID, ModelID: modelID, Protocol: "openai", Source: "manual",
		UpstreamModel: "probe-upstream-model", Success: true, HTTPStatus: 200, LatencyMs: 12,
		Facts: facts, IdempotencyKey: noPriceKey,
	}); err != nil {
		t.Fatalf("account reliable probe without pricing: %v", err)
	}
	var usageReliable bool
	var costAmount pgtype.Numeric
	if err := pool.QueryRow(ctx, `
		SELECT usage_reliable, cost_amount
		FROM provider_probe_records
		WHERE idempotency_key = $1
	`, noPriceKey).Scan(&usageReliable, &costAmount); err != nil {
		t.Fatalf("get reliable probe without pricing: %v", err)
	}
	if !usageReliable || costAmount.Valid {
		t.Fatalf("expected reliable usage with unknown cost, got reliable=%v cost=%+v", usageReliable, costAmount)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM provider_ledger_entries WHERE provider_id = $1 AND entry_type = 'probe_debit'`, providerID).Scan(&probeDebits); err != nil {
		t.Fatalf("count probe debits after unknown cost: %v", err)
	}
	if probeDebits != 1 {
		t.Fatalf("unknown probe cost must not create a debit, got %d", probeDebits)
	}
}

func assertProviderLedgerNumeric(t *testing.T, got pgtype.Numeric, want string) {
	t.Helper()
	if !got.Valid || got.Int == nil {
		t.Fatalf("invalid numeric result: %+v", got)
	}
	wantRat, ok := new(big.Rat).SetString(want)
	if !ok {
		t.Fatalf("invalid expected numeric %q", want)
	}
	gotRat := numericRat(got)
	if gotRat.Cmp(wantRat) != 0 {
		t.Fatalf("expected %s, got %s", wantRat.RatString(), gotRat.RatString())
	}
}

func TestProviderLedgerAdjustmentsSupportNegativeBalanceAndIdempotency(t *testing.T) {
	ctx, pool, service, providerID := providerLedgerDeps(t)
	key := fmt.Sprintf("provider-ledger-adjust-%d", time.Now().UnixNano())
	entry, err := service.AdjustDebit(ctx, AdjustParams{ProviderID: providerID, Amount: providerLedgerNumeric(t, "5"), Currency: "USD", IdempotencyKey: key, Reason: "test debit"})
	if err != nil {
		t.Fatalf("adjust debit: %v", err)
	}
	assertProviderLedgerNumeric(t, entry.BalanceBefore, "0")
	assertProviderLedgerNumeric(t, entry.BalanceAfter, "-5")

	replay, err := service.AdjustDebit(ctx, AdjustParams{ProviderID: providerID, Amount: providerLedgerNumeric(t, "5.0"), Currency: "USD", IdempotencyKey: key, Reason: "test debit"})
	if err != nil || replay.ID != entry.ID {
		t.Fatalf("idempotent replay mismatch: entry=%+v err=%v", replay, err)
	}
	_, err = service.AdjustDebit(ctx, AdjustParams{ProviderID: providerID, Amount: providerLedgerNumeric(t, "6"), Currency: "USD", IdempotencyKey: key, Reason: "test debit"})
	if failure.CodeOf(err) != failure.CodeLedgerIdempotencyConflict {
		t.Fatalf("expected idempotency conflict, got %v (%s)", err, failure.CodeOf(err))
	}

	credit, err := service.AdjustCredit(ctx, AdjustParams{ProviderID: providerID, Amount: providerLedgerNumeric(t, "2"), Currency: "USD", IdempotencyKey: key + ":credit", Reason: "test credit"})
	if err != nil {
		t.Fatalf("adjust credit: %v", err)
	}
	assertProviderLedgerNumeric(t, credit.BalanceAfter, "-3")
	row, err := sqlc.New(pool).GetProviderBalance(ctx, sqlc.GetProviderBalanceParams{ProviderID: providerID, Currency: "USD"})
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	assertProviderLedgerNumeric(t, row.Balance, "-3")
}

func TestProviderLedgerConcurrentAdjustmentsKeepContinuousBalance(t *testing.T) {
	ctx, pool, service, providerID := providerLedgerDeps(t)
	const count = 12
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := service.AdjustDebit(ctx, AdjustParams{
				ProviderID: providerID, Amount: providerLedgerNumeric(t, "1"), Currency: "USD",
				IdempotencyKey: fmt.Sprintf("provider-ledger-concurrent-%d", i), Reason: "concurrent test",
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent adjustment: %v", err)
		}
	}
	var balance pgtype.Numeric
	if err := pool.QueryRow(ctx, `SELECT balance FROM provider_balances WHERE provider_id = $1 AND currency = 'USD'`, providerID).Scan(&balance); err != nil {
		t.Fatalf("get balance: %v", err)
	}
	assertProviderLedgerNumeric(t, balance, "-12")
	var entries int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM provider_ledger_entries WHERE provider_id = $1`, providerID).Scan(&entries); err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if entries != count {
		t.Fatalf("expected %d entries, got %d", count, entries)
	}
}
