package sqlc_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// marginGuardConstraint 是毛利守卫的约束名；断言它而非错误文本，避免文案改动破坏测试。
const marginGuardConstraint = "ck_non_negative_margin"

func TestMarginGuardAcceptsSafeConfiguration(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("margin-safe-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("margin-safe-channel-%d", suffix), "enabled", 10, nil)
	modelID := insertModel(t, ctx, tx, fmt.Sprintf("openai/margin-safe-%d", suffix), "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelID, "margin-safe", "enabled")
	now := time.Now().UTC()
	createModelPriceForTest(t, ctx, queries, modelID, now)
	createChannelPriceForTest(t, ctx, queries, channelID, modelID, now)

	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		t.Fatalf("safe margin configuration rejected: %v", err)
	}
}

// 绝对售价低于渠道成本必须被拒：这条路径下倍率不参与，逐项比较售价与成本。
func TestMarginGuardRejectsAbsoluteSalePriceBelowCost(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("margin-abs-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("margin-abs-channel-%d", suffix), "enabled", 10, nil)
	modelID := insertModel(t, ctx, tx, fmt.Sprintf("openai/margin-abs-%d", suffix), "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelID, "margin-abs", "enabled")
	now := time.Now().UTC()
	createModelPriceForTest(t, ctx, queries, modelID, now)
	createChannelPriceForTest(t, ctx, queries, channelID, modelID, now)

	if _, err := tx.Exec(ctx, `
		UPDATE model_prices
		SET sale_uncached_input_price = 0.0000000001,
		    sale_output_price = 0.0000000001
		WHERE model_id = $1 AND status = 'enabled'`, modelID); err != nil {
		t.Fatalf("stage below-cost absolute sale price: %v", err)
	}

	_, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != marginGuardConstraint {
		t.Fatalf("expected negative-margin constraint, got %v", err)
	}
}

// 全局售价倍率被压到成本线以下必须被拒。
// 倍率是倍率路径下售价公式的一半，漏掉它就等于给亏本配置留了一条后门。
func TestMarginGuardRejectsGlobalSaleRatioBelowCost(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("margin-ratio-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("margin-ratio-channel-%d", suffix), "enabled", 10, nil)
	modelID := insertModel(t, ctx, tx, fmt.Sprintf("openai/margin-ratio-%d", suffix), "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelID, "margin-ratio", "enabled")
	now := time.Now().UTC()
	createModelPriceForTest(t, ctx, queries, modelID, now)
	createChannelPriceForTest(t, ctx, queries, channelID, modelID, now)

	if _, err := tx.Exec(ctx, `
		INSERT INTO app_settings (key, value, description)
		VALUES ('gateway.model_sale_price_ratio', jsonb_build_object('ratio', '0.0000000001'), 'test')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatalf("stage below-cost global sale ratio: %v", err)
	}

	_, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != marginGuardConstraint {
		t.Fatalf("expected negative-margin constraint, got %v", err)
	}
}
