package sqlc_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
)

// countRows 跑一条 SELECT count(*) 并返回计数，供级联删除测试断言子表已被清空。
func countRows(t *testing.T, ctx context.Context, tx pgx.Tx, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := tx.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("count rows (%s): %v", query, err)
	}
	return n
}

// TestDeleteChannelCascadeRemovesOwnConfig 验证录错的 channel 可一键真删：
// 数据修改 CTE 在单条语句内先删 channel_models（NO ACTION 外键，语句末校验）、渠道成本价
// 及其 Fast 档子行、无账务引用的探测记录，再删 channel，不因「子表仍引用」而失败；
// 删除后 channel 与其绑定、价格、探测都不复存在。
func TestDeleteChannelCascadeRemovesOwnConfig(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	timeoutMS := int32(15000)

	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("del-chan-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("del-chan-%d", suffix), "enabled", 10, &timeoutMS)
	modelA := insertModel(t, ctx, tx, fmt.Sprintf("openai/del-chan-model-a-%d", suffix), "openai", "enabled")
	modelB := insertModel(t, ctx, tx, fmt.Sprintf("openai/del-chan-model-b-%d", suffix), "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelA, "del-chan-a", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelB, "del-chan-b", "disabled")
	// 渠道成本价带 Fast 档子行（channel_price_service_tiers，NO ACTION 外键指向 channel_prices），
	// 未在级联中清理时会挡住整条删除。
	now := time.Now().UTC()
	createChannelPriceWithFastForTest(t, ctx, queries, channelID, modelA, now)
	// 渠道曾被探测/验证（无账务引用），不应挡住硬删。
	insertProbeRecord(t, ctx, tx, providerID, channelID, modelA, fmt.Sprintf("del-chan-probe-%d", suffix))

	if got := countRows(t, ctx, tx, `SELECT count(*) FROM channel_models WHERE channel_id = $1`, channelID); got != 2 {
		t.Fatalf("expected 2 bindings before delete, got %d", got)
	}

	affected, err := queries.DeleteChannelCascade(ctx, channelID)
	if err != nil {
		t.Fatalf("delete channel cascade: %v", err)
	}
	if affected != 1 {
		t.Fatalf("expected 1 channel deleted, got %d", affected)
	}

	if got := countRows(t, ctx, tx, `SELECT count(*) FROM channel_models WHERE channel_id = $1`, channelID); got != 0 {
		t.Fatalf("expected bindings cascaded away, got %d", got)
	}
	if got := countRows(t, ctx, tx, `SELECT count(*) FROM channel_price_service_tiers t JOIN channel_prices p ON p.id = t.channel_price_id WHERE p.channel_id = $1`, channelID); got != 0 {
		t.Fatalf("expected channel price fast tiers cascaded away, got %d", got)
	}
	if got := countRows(t, ctx, tx, `SELECT count(*) FROM provider_probe_records WHERE channel_id = $1`, channelID); got != 0 {
		t.Fatalf("expected probe records cascaded away, got %d", got)
	}
	if _, err := queries.GetChannel(ctx, channelID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected channel gone (ErrNoRows), got %v", err)
	}
	// 级联只清自身配置，不应误伤模型本身。
	if _, err := queries.LookupModelByID(ctx, modelA); err != nil {
		t.Fatalf("model A should survive channel delete: %v", err)
	}
}

// TestDeleteModelCascadeRemovesOwnConfig 验证录错的 model 可一键真删：
// CTE 清掉它自身的配置子表——绑定、基准售价（model_prices）及其 Fast 档子行、渠道成本价
// （channel_prices，NO ACTION）及其 Fast 档子行、渠道模型验证结果（运维数据）；
// 探测记录是不可变事实、只解除模型归因（model_id 置 NULL）不删行；
// model_capabilities 由 ON DELETE CASCADE 自动清理；channel 本身不受影响。
// 价格表是追加式配置（无删除接口，只能停用），必须由级联清掉，否则任何配过价的 model 永远删不掉；
// 验证/探测由渠道清单功能自动产生，若不处理，被验证过的 model 即使从未服务请求也永远删不掉。
func TestDeleteModelCascadeRemovesOwnConfig(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	timeoutMS := int32(15000)

	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("del-model-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("del-model-channel-%d", suffix), "enabled", 10, &timeoutMS)
	modelID := insertModel(t, ctx, tx, fmt.Sprintf("openai/del-model-%d", suffix), "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelID, "del-model-upstream", "enabled")
	insertModelCapability(t, ctx, tx, modelID, "text.output", "full")
	// 追加式价格配置：模型基准售价 + 渠道成本价（均带 Fast 档子行），验证级联把整组一并清掉。
	now := time.Now().UTC()
	createModelPriceWithFastForTest(t, ctx, queries, modelID, now)
	createChannelPriceWithFastForTest(t, ctx, queries, channelID, modelID, now)
	// 渠道模型清单的验证结果与探测记录：验证项随模型删除，探测事实保留但解除归因。
	insertVerificationItem(t, ctx, tx, channelID, modelID, "del-model-upstream")
	probeID := insertProbeRecord(t, ctx, tx, providerID, channelID, modelID, fmt.Sprintf("del-model-probe-%d", suffix))

	affected, err := queries.DeleteModelCascade(ctx, modelID)
	if err != nil {
		t.Fatalf("delete model cascade: %v", err)
	}
	if affected != 1 {
		t.Fatalf("expected 1 model deleted, got %d", affected)
	}

	if got := countRows(t, ctx, tx, `SELECT count(*) FROM channel_models WHERE model_id = $1`, modelID); got != 0 {
		t.Fatalf("expected channel_models cascaded away, got %d", got)
	}
	if got := countRows(t, ctx, tx, `SELECT count(*) FROM model_prices WHERE model_id = $1`, modelID); got != 0 {
		t.Fatalf("expected model_prices cascaded away, got %d", got)
	}
	if got := countRows(t, ctx, tx, `SELECT count(*) FROM model_price_service_tiers t JOIN model_prices p ON p.id = t.model_price_id WHERE p.model_id = $1`, modelID); got != 0 {
		t.Fatalf("expected model price fast tiers cascaded away, got %d", got)
	}
	if got := countRows(t, ctx, tx, `SELECT count(*) FROM channel_prices WHERE model_id = $1`, modelID); got != 0 {
		t.Fatalf("expected channel_prices cascaded away, got %d", got)
	}
	if got := countRows(t, ctx, tx, `SELECT count(*) FROM channel_price_service_tiers t JOIN channel_prices p ON p.id = t.channel_price_id WHERE p.model_id = $1`, modelID); got != 0 {
		t.Fatalf("expected channel price fast tiers cascaded away, got %d", got)
	}
	if got := countRows(t, ctx, tx, `SELECT count(*) FROM model_capabilities WHERE model_id = $1`, modelID); got != 0 {
		t.Fatalf("expected model_capabilities ON DELETE CASCADE removed, got %d", got)
	}
	if got := countRows(t, ctx, tx, `SELECT count(*) FROM channel_model_verification_items WHERE model_id = $1`, modelID); got != 0 {
		t.Fatalf("expected verification items cascaded away, got %d", got)
	}
	// 探测事实保留，但模型归因已解除。
	if got := countRows(t, ctx, tx, `SELECT count(*) FROM provider_probe_records WHERE id = $1 AND model_id IS NULL`, probeID); got != 1 {
		t.Fatalf("expected probe record kept with model_id detached, got %d", got)
	}
	// channel 本身不应被模型删除连带删掉。
	if _, err := queries.GetChannel(ctx, channelID); err != nil {
		t.Fatalf("channel should survive model delete: %v", err)
	}
}

// TestDeleteChannelModelRemovesOwnChannelPrice 验证解绑单个 (channel, model) 时，
// 同一条语句先清掉该边自身的 channel_prices（追加式成本价配置，无删除接口），再删绑定；
// 不因「该边配过成本价」而失败。兄弟绑定/价格、model 与 channel 本身都不受影响。
func TestDeleteChannelModelRemovesOwnChannelPrice(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	timeoutMS := int32(15000)

	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("unbind-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("unbind-channel-%d", suffix), "enabled", 10, &timeoutMS)
	modelA := insertModel(t, ctx, tx, fmt.Sprintf("openai/unbind-model-a-%d", suffix), "openai", "enabled")
	modelB := insertModel(t, ctx, tx, fmt.Sprintf("openai/unbind-model-b-%d", suffix), "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelA, "unbind-a", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelB, "unbind-b", "enabled")
	now := time.Now().UTC()
	createChannelPriceForTest(t, ctx, queries, channelID, modelA, now)
	createChannelPriceForTest(t, ctx, queries, channelID, modelB, now)

	affected, err := queries.DeleteChannelModel(ctx, sqlc.DeleteChannelModelParams{
		ChannelID: channelID,
		ModelID:   modelA,
	})
	if err != nil {
		t.Fatalf("delete channel model: %v", err)
	}
	if affected != 1 {
		t.Fatalf("expected 1 binding deleted, got %d", affected)
	}

	if got := countRows(t, ctx, tx, `SELECT count(*) FROM channel_prices WHERE channel_id = $1 AND model_id = $2`, channelID, modelA); got != 0 {
		t.Fatalf("expected unbound edge's channel_prices cleaned, got %d", got)
	}
	if got := countRows(t, ctx, tx, `SELECT count(*) FROM channel_models WHERE channel_id = $1 AND model_id = $2`, channelID, modelA); got != 0 {
		t.Fatalf("expected binding gone, got %d", got)
	}
	// 兄弟绑定（model B）及其成本价不受影响。
	if got := countRows(t, ctx, tx, `SELECT count(*) FROM channel_models WHERE channel_id = $1 AND model_id = $2`, channelID, modelB); got != 1 {
		t.Fatalf("expected sibling binding to survive, got %d", got)
	}
	if got := countRows(t, ctx, tx, `SELECT count(*) FROM channel_prices WHERE channel_id = $1 AND model_id = $2`, channelID, modelB); got != 1 {
		t.Fatalf("expected sibling channel_price to survive, got %d", got)
	}
	// model 与 channel 本身都不应被解绑连带删掉。
	if _, err := queries.LookupModelByID(ctx, modelA); err != nil {
		t.Fatalf("model A should survive unbind: %v", err)
	}
	if _, err := queries.GetChannel(ctx, channelID); err != nil {
		t.Fatalf("channel should survive unbind: %v", err)
	}
}

// TestDeleteProviderCleanWhenEmpty 验证录错且名下无渠道的 provider 可真删（slug 释放可重录）。
func TestDeleteProviderCleanWhenEmpty(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("del-empty-provider-%d", suffix), "enabled")

	affected, err := queries.DeleteProvider(ctx, providerID)
	if err != nil {
		t.Fatalf("delete empty provider: %v", err)
	}
	if affected != 1 {
		t.Fatalf("expected 1 provider deleted, got %d", affected)
	}
}

// TestDeleteProviderClearsAdminOnlyLedger 验证：账本只有手工调额分录（无真实交易归因）时，
// 删除随手清理分录与余额缓存行——测试服务商设过余额也能删（2026-08-29 用户反馈）。
// 交易性分录（usage/probe）必然伴随渠道/探测记录的外键引用，拒绝路径由 BlockedByChannel 用例覆盖。
func TestDeleteProviderClearsAdminOnlyLedger(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("del-adj-provider-%d", suffix), "enabled")
	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_ledger_entries
			(provider_id, entry_type, amount, currency, balance_before, balance_after, idempotency_key, reason)
		VALUES ($1, 'adjustment_credit', 123, 'USD', 0, 123, $2, '测试注资')
	`, providerID, fmt.Sprintf("del-adj-ledger-%d", suffix)); err != nil {
		t.Fatalf("insert adjustment ledger entry: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_balances (provider_id, currency, balance) VALUES ($1, 'USD', 123)
	`, providerID); err != nil {
		t.Fatalf("insert provider balance: %v", err)
	}

	affected, err := queries.DeleteProvider(ctx, providerID)
	if err != nil {
		t.Fatalf("delete provider with admin-only ledger: %v", err)
	}
	if affected != 1 {
		t.Fatalf("expected 1 provider deleted, got %d", affected)
	}
	var ledgerLeft, balanceLeft int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM provider_ledger_entries WHERE provider_id = $1`, providerID).Scan(&ledgerLeft); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM provider_balances WHERE provider_id = $1`, providerID).Scan(&balanceLeft); err != nil {
		t.Fatalf("count balances: %v", err)
	}
	if ledgerLeft != 0 || balanceLeft != 0 {
		t.Fatalf("expected admin-only ledger cleared, ledger=%d balances=%d", ledgerLeft, balanceLeft)
	}
}

// TestDeleteProviderBlockedByChannel 验证：provider 名下仍有渠道时，DB 的 NO ACTION 外键
// 拒绝删除（23503）。这也间接证明「未被级联清理的引用」会在语句末挡住删除——
// 等价于 channel/model 被请求/账务历史引用时的拒绝路径，上层据此降级为 conflict。
func TestDeleteProviderBlockedByChannel(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	timeoutMS := int32(15000)
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("del-busy-provider-%d", suffix), "enabled")
	insertChannel(t, ctx, tx, providerID, fmt.Sprintf("del-busy-channel-%d", suffix), "enabled", 10, &timeoutMS)

	_, err := queries.DeleteProvider(ctx, providerID)
	if !isForeignKeyViolation(err) {
		t.Fatalf("expected foreign key violation (23503) deleting provider with channel, got %v", err)
	}
}

// insertProviderRoutingOp 插入一条 Provider 运行态操作日志，供硬删护栏测试。
func insertProviderRoutingOp(t *testing.T, ctx context.Context, tx pgx.Tx, providerID int64, kind, state, token string) int64 {
	t.Helper()
	var completedAt any
	if state == "committed" || state == "aborted" {
		completedAt = time.Now()
	}
	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO provider_routing_operations
			(token, kind, provider_id, transitions, payload_hash, state, completed_at)
		VALUES ($1, $2, $3, '{}'::jsonb, 'testhash', $4, $5)
		RETURNING id
	`, token, kind, providerID, state, completedAt).Scan(&id); err != nil {
		t.Fatalf("insert provider routing op: %v", err)
	}
	return id
}

// TestDeleteProviderRemovesTerminalOperations 验证无渠道/请求账务历史时，硬删会同步清理终态操作日志。
func TestDeleteProviderRemovesTerminalOperations(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	// sqlc 层 DeleteProvider 不校验状态（archived 闸门在 service 层），故沿用兄弟用例的 enabled。
	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("del-provider-%d", suffix), "enabled")
	opID := insertProviderRoutingOp(t, ctx, tx, providerID, "origin", "committed",
		fmt.Sprintf("del-provider-tok-%d", suffix))

	affected, err := queries.DeleteProvider(ctx, providerID)
	if err != nil {
		t.Fatalf("delete provider with terminal operation: %v", err)
	}
	if affected != 1 {
		t.Fatalf("expected 1 provider deleted, got %d", affected)
	}

	if left := countRows(t, ctx, tx, `SELECT count(*) FROM provider_routing_operations WHERE id=$1`, opID); left != 0 {
		t.Fatalf("terminal operation must be removed, got %d rows", left)
	}
}

// TestDeleteProviderBlockedByInFlightOriginOp 验证：源站存在未终态运行态操作（进行中的围栏）时，
// RESTRICT 外键挡住硬删（23503），避免删除进行中的运行态操作；上层据此降级为 conflict。
func TestDeleteProviderBlockedByInFlightOriginOp(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("del-inflight-provider-%d", suffix), "enabled")
	insertProviderRoutingOp(t, ctx, tx, providerID, "origin", "prepared",
		fmt.Sprintf("del-inflight-tok-%d", suffix))

	if _, err := queries.DeleteProvider(ctx, providerID); !isForeignKeyViolation(err) {
		t.Fatalf("expected 23503 blocking delete with in-flight provider op, got %v", err)
	}
}

// createModelPriceWithFastForTest 创建一条带 Fast 档子行（model_price_service_tiers）的
// enabled 模型基准售价，供级联删除测试验证 Fast 档不会挡住模型硬删。
func createModelPriceWithFastForTest(t *testing.T, ctx context.Context, queries *sqlc.Queries, modelID int64, at time.Time) {
	t.Helper()

	if _, err := queries.CreateModelPrice(ctx, sqlc.CreateModelPriceParams{
		ModelID:            modelID,
		Currency:           "USD",
		PricingUnit:        "per_1m_tokens",
		UncachedInputPrice: numeric(100),
		OutputPrice:        numeric(400),
		SalePriceRatio:     numeric(1),
		Status:             "enabled",
		EffectiveFrom:      timestamptz(at.Add(-time.Hour)),
		EffectiveTo:        nullTimestamptz(),
		FastConfigured:     true,
		// Fast 档只需 uncached/output 两项必填。
		FastUncachedInputPrice: numeric(200),
		FastOutputPrice:        numeric(800),
	}); err != nil {
		t.Fatalf("create model price with fast tier: %v", err)
	}
}

// createChannelPriceWithFastForTest 创建一条带 Fast 档子行（channel_price_service_tiers）的
// enabled 渠道-模型成本价，供级联删除测试验证 Fast 档不会挡住模型/渠道硬删。
func createChannelPriceWithFastForTest(t *testing.T, ctx context.Context, queries *sqlc.Queries, channelID, modelID int64, at time.Time) {
	t.Helper()

	if _, err := queries.CreateChannelPrice(ctx, sqlc.CreateChannelPriceParams{
		ChannelID:         channelID,
		ModelID:           modelID,
		Currency:          "USD",
		PricingUnit:       "per_1m_tokens",
		UncachedInputCost: numeric(1),
		OutputCost:        numeric(4),
		Status:            "enabled",
		EffectiveFrom:     timestamptz(at.Add(-time.Hour)),
		EffectiveTo:       nullTimestamptz(),
		FastConfigured:    true,
		// Fast 档只需 uncached/output 两项必填。
		FastUncachedInputCost: numeric(2),
		FastOutputCost:        numeric(8),
	}); err != nil {
		t.Fatalf("create channel price with fast tier: %v", err)
	}
}

// insertVerificationItem 插入一条渠道模型验证 run + item（运维观测数据），
// 模拟渠道清单验证跑过该模型：item 通过 NO ACTION 外键引用 models。
func insertVerificationItem(t *testing.T, ctx context.Context, tx pgx.Tx, channelID, modelID int64, upstreamModel string) {
	t.Helper()

	var runID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO channel_model_verification_runs
			(channel_id, source, status, channel_config_revision, provider_origin_revision, provider_status_revision, total_count, succeeded_count)
		VALUES ($1, 'manual', 'succeeded', 1, 1, 1, 1, 1)
		RETURNING id
	`, channelID).Scan(&runID); err != nil {
		t.Fatalf("insert verification run: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO channel_model_verification_items
			(run_id, model_id, upstream_model, status, success, http_status)
		VALUES ($1, $2, $3, 'succeeded', true, 200)
	`, runID, modelID, upstreamModel); err != nil {
		t.Fatalf("insert verification item: %v", err)
	}
}

// insertProbeRecord 插入一条无账务引用的探测事实（provider_probe_records），返回主键。
// 模拟渠道测试/验证探测过该模型：model_id 归因通过 NO ACTION 外键引用 models。
func insertProbeRecord(t *testing.T, ctx context.Context, tx pgx.Tx, providerID, channelID, modelID int64, idempotencyKey string) int64 {
	t.Helper()

	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO provider_probe_records
			(provider_id, channel_id, model_id, protocol, source, upstream_model, success, http_status, idempotency_key)
		VALUES ($1, $2, $3, 'openai', 'verification', 'probe-upstream', true, 200, $4)
		RETURNING id
	`, providerID, channelID, modelID, idempotencyKey).Scan(&id); err != nil {
		t.Fatalf("insert provider probe record: %v", err)
	}
	return id
}
