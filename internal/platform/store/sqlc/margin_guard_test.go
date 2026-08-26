package sqlc_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
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

// 模型售价倍率被压到成本线以下必须被拒。
// 倍率是倍率路径下售价公式的一半，漏掉它就等于给亏本配置留了一条后门。
func TestMarginGuardRejectsModelSaleRatioBelowCost(t *testing.T) {
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
		UPDATE model_prices
		SET sale_price_ratio = 0.0000000001
		WHERE model_id = $1 AND status = 'enabled'`, modelID); err != nil {
		t.Fatalf("stage below-cost model sale ratio: %v", err)
	}

	_, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != marginGuardConstraint {
		t.Fatalf("expected negative-margin constraint, got %v", err)
	}
}

// Fast 档绝对售价低于 Fast 成本必须被拒。
//
// 000041 的守卫对 Fast 档只算「Fast 基准价 × 倍率」，而运行时是会读 Fast 绝对售价的，
// 于是给 Fast 配了亏本的绝对售价能写进库。本用例锁住修复：Fast 售价侧必须先看绝对售价。
func TestMarginGuardRejectsFastAbsoluteSalePriceBelowCost(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	fixture := stageFastTierMargin(t, ctx, tx, queries, "margin-fast-bad")

	// Fast 成本 2/8，Fast 绝对售价压到 1e-10：逐项比较必定亏本。
	if _, err := tx.Exec(ctx, `
		UPDATE model_price_service_tiers
		SET sale_uncached_input_price = 0.0000000001,
		    sale_output_price = 0.0000000001
		WHERE model_price_id = $1 AND service_tier = 'fast'`, fixture.modelPriceID); err != nil {
		t.Fatalf("stage below-cost fast absolute sale price: %v", err)
	}

	_, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != marginGuardConstraint {
		t.Fatalf("expected negative-margin constraint, got %v", err)
	}
}

// Fast 档绝对售价高于 Fast 成本必须放行——反例的对照，确认修复没有把正常配置一起拦掉。
func TestMarginGuardAcceptsFastAbsoluteSalePriceAboveCost(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	fixture := stageFastTierMargin(t, ctx, tx, queries, "margin-fast-ok")

	// Fast 成本 2/8，绝对售价 20/80：十倍毛利。
	if _, err := tx.Exec(ctx, `
		UPDATE model_price_service_tiers
		SET sale_uncached_input_price = 20,
		    sale_output_price = 80
		WHERE model_price_id = $1 AND service_tier = 'fast'`, fixture.modelPriceID); err != nil {
		t.Fatalf("stage above-cost fast absolute sale price: %v", err)
	}

	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		t.Fatalf("profitable fast absolute sale price rejected: %v", err)
	}
}

type fastTierMarginFixture struct {
	modelID      int64
	channelID    int64
	modelPriceID int64
}

// stageFastTierMargin 搭起「模型与渠道两侧都配了 Fast 档」的最小场景，即守卫分支 D 的触发条件。
// Standard 侧留足毛利（基准价 100/400 × 倍率 1.0 对成本 1/4），使断言只受 Fast 档影响。
func stageFastTierMargin(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	queries *sqlc.Queries,
	name string,
) fastTierMarginFixture {
	t.Helper()

	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("%s-%d", name, suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("%s-channel-%d", name, suffix), "enabled", 10, nil)
	modelID := insertModel(t, ctx, tx, fmt.Sprintf("openai/%s-%d", name, suffix), "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelID, name, "enabled")
	now := time.Now().UTC()
	createModelPriceForTest(t, ctx, queries, modelID, now)
	channelPrice := createChannelPriceForTest(t, ctx, queries, channelID, modelID, now)

	var modelPriceID int64
	if err := tx.QueryRow(ctx, `
		SELECT id FROM model_prices WHERE model_id = $1 AND status = 'enabled'
	`, modelID).Scan(&modelPriceID); err != nil {
		t.Fatalf("lookup model price: %v", err)
	}

	// Fast 基准价与 Standard 同量级，避免倍率路径先行触发违规。
	if _, err := tx.Exec(ctx, `
		INSERT INTO model_price_service_tiers (
			model_price_id, service_tier, uncached_input_price, output_price
		) VALUES ($1, 'fast', 100, 400)
	`, modelPriceID); err != nil {
		t.Fatalf("insert fast model price tier: %v", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO channel_price_service_tiers (
			channel_price_id, service_tier, uncached_input_cost, output_cost
		) VALUES ($1, 'fast', 2, 8)
	`, channelPrice.ID); err != nil {
		t.Fatalf("insert fast channel price tier: %v", err)
	}

	return fastTierMarginFixture{modelID: modelID, channelID: channelID, modelPriceID: modelPriceID}
}
