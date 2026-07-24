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
// 数据修改 CTE 在单条语句内先删 channel_models（NO ACTION 外键，语句末校验），再删 channel，
// 不因「子表仍引用」而失败；删除后 channel 与其绑定都不复存在。
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
	if _, err := queries.GetChannel(ctx, channelID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected channel gone (ErrNoRows), got %v", err)
	}
	// 级联只清自身配置，不应误伤模型本身。
	if _, err := queries.LookupModelByID(ctx, modelA); err != nil {
		t.Fatalf("model A should survive channel delete: %v", err)
	}
}

// TestDeleteModelCascadeRemovesOwnConfig 验证录错的 model 可一键真删：
// CTE 清掉它自身的配置子表——绑定、基准售价（model_prices）、渠道成本价（channel_prices，NO ACTION）；
// model_capabilities、user_model_policies 由 ON DELETE CASCADE 自动清理；channel 本身不受影响。
// 价格表是追加式配置（无删除接口，只能停用），必须由级联清掉，否则任何配过价的 model 永远删不掉。
func TestDeleteModelCascadeRemovesOwnConfig(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	timeoutMS := int32(15000)
	userID := createUserForModelPolicy(t, ctx, queries, suffix)

	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("del-model-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("del-model-channel-%d", suffix), "enabled", 10, &timeoutMS)
	modelID := insertModel(t, ctx, tx, fmt.Sprintf("openai/del-model-%d", suffix), "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelID, "del-model-upstream", "enabled")
	insertModelCapability(t, ctx, tx, modelID, "text.output", "full")
	insertUserModelPolicy(t, ctx, tx, userID, modelID, "denied")
	// 追加式价格配置：模型基准售价 + 渠道成本价，验证级联把两者一并清掉。
	now := time.Now().UTC()
	createModelPriceForTest(t, ctx, queries, modelID, now)
	createChannelPriceForTest(t, ctx, queries, channelID, modelID, now)

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
	if got := countRows(t, ctx, tx, `SELECT count(*) FROM channel_prices WHERE model_id = $1`, modelID); got != 0 {
		t.Fatalf("expected channel_prices cascaded away, got %d", got)
	}
	if got := countRows(t, ctx, tx, `SELECT count(*) FROM model_capabilities WHERE model_id = $1`, modelID); got != 0 {
		t.Fatalf("expected model_capabilities ON DELETE CASCADE removed, got %d", got)
	}
	if got := countRows(t, ctx, tx, `SELECT count(*) FROM user_model_policies WHERE model_id = $1`, modelID); got != 0 {
		t.Fatalf("expected user_model_policies ON DELETE CASCADE removed, got %d", got)
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

// insertOriginRoutingOp 插入一条源站运行态操作日志（origin_routing_operations），供级联删除测试。
// 终态（committed/aborted）需带 completed_at；未终态置空。originID 传 nil 表示批量操作（origin_id NULL）。
func insertOriginRoutingOp(t *testing.T, ctx context.Context, tx pgx.Tx, providerID int64, originID *int64, kind, state, token string) int64 {
	t.Helper()
	var completedAt any
	if state == "committed" || state == "aborted" {
		completedAt = time.Now()
	}
	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO origin_routing_operations
			(token, kind, provider_id, origin_id, transitions, payload_hash, state, completed_at)
		VALUES ($1, $2, $3, $4, '{}'::jsonb, 'testhash', $5, $6)
		RETURNING id
	`, token, kind, providerID, originID, state, completedAt).Scan(&id); err != nil {
		t.Fatalf("insert origin routing op: %v", err)
	}
	return id
}

// TestDeleteProviderCascadesOrigins 验证 P4 后：provider 名下有上游源站（及终态操作日志）但无渠道/请求账务
// 历史时，硬删在单条语句内级联清理源站；终态 origin_routing_operations 置空 FK 保留审计摘要（不删行）。
func TestDeleteProviderCascadesOrigins(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	// sqlc 层 DeleteProvider 不校验状态（archived 闸门在 service 层），故沿用兄弟用例的 enabled。
	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("del-origin-provider-%d", suffix), "enabled")
	originID := insertProviderOrigin(t, ctx, tx, providerID, "primary",
		fmt.Sprintf("https://del-origin-%d.test", suffix), "enabled")
	opID := insertOriginRoutingOp(t, ctx, tx, providerID, &originID, "origin_create", "committed",
		fmt.Sprintf("del-origin-tok-%d", suffix))

	affected, err := queries.DeleteProvider(ctx, providerID)
	if err != nil {
		t.Fatalf("delete provider with clean origin: %v", err)
	}
	if affected != 1 {
		t.Fatalf("expected 1 provider deleted, got %d", affected)
	}

	if left := countRows(t, ctx, tx, `SELECT count(*) FROM provider_origins WHERE provider_id=$1`, providerID); left != 0 {
		t.Fatalf("expected origins cascaded, %d left", left)
	}
	if kept := countRows(t, ctx, tx, `SELECT count(*) FROM origin_routing_operations WHERE id=$1`, opID); kept != 1 {
		t.Fatalf("terminal op-log must be preserved as audit, got %d rows", kept)
	}
	if nulled := countRows(t, ctx, tx,
		`SELECT count(*) FROM origin_routing_operations WHERE id=$1 AND provider_id IS NULL AND origin_id IS NULL`, opID); nulled != 1 {
		t.Fatalf("terminal op-log FKs must be nulled to release RESTRICT while keeping the row")
	}
}

// TestDeleteProviderBlockedByInFlightOriginOp 验证：源站存在未终态运行态操作（进行中的围栏）时，
// RESTRICT 外键挡住硬删（23503），避免删除进行中的运行态操作；上层据此降级为 conflict。
func TestDeleteProviderBlockedByInFlightOriginOp(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("del-inflight-provider-%d", suffix), "enabled")
	originID := insertProviderOrigin(t, ctx, tx, providerID, "primary",
		fmt.Sprintf("https://del-inflight-%d.test", suffix), "enabled")
	insertOriginRoutingOp(t, ctx, tx, providerID, &originID, "base_url", "prepared",
		fmt.Sprintf("del-inflight-tok-%d", suffix))

	if _, err := queries.DeleteProvider(ctx, providerID); !isForeignKeyViolation(err) {
		t.Fatalf("expected 23503 blocking delete with in-flight origin op, got %v", err)
	}
}
