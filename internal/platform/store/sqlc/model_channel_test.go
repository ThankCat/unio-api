package sqlc_test

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// numeric / timestamptz / nullTimestamptz 是 sqlc 测试共享的 pgtype 构造助手。
func numeric(value int64) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(value), Exp: 0, Valid: true}
}

func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func nullTimestamptz() pgtype.Timestamptz {
	return pgtype.Timestamptz{Valid: false}
}

// newModelChannelTestTx 创建带回滚事务的 sqlc 查询对象，避免测试数据污染本地数据库。
func newModelChannelTestTx(t *testing.T) (context.Context, pgx.Tx, *sqlc.Queries, func()) {
	t.Helper()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

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

	tx, err := pool.Begin(ctx)
	if err != nil {
		pool.Close()
		cancel()
		t.Fatalf("begin transaction: %v", err)
	}

	cleanup := func() {
		_ = tx.Rollback(context.Background())
		pool.Close()
		cancel()
	}

	return ctx, tx, sqlc.New(tx), cleanup
}

// insertProvider 插入测试 provider，并返回数据库主键。
//
// Phase 10 起 provider 不再持有 adapter 绑定；adapter_key 已下沉到 channel。
func insertProvider(t *testing.T, ctx context.Context, tx pgx.Tx, slug string, status string) int64 {
	t.Helper()

	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO providers (slug, name, origin, status)
		VALUES ($1, $2, 'https://' || $1 || '.example.test', $3)
		RETURNING id
	`, slug, slug, status).Scan(&id)
	if err != nil {
		t.Fatalf("insert provider %q: %v", slug, err)
	}

	return id
}

// insertChannel 插入测试 channel（默认 protocol=openai、adapter_key=openai），并返回数据库主键。
func insertChannel(t *testing.T, ctx context.Context, tx pgx.Tx, providerID int64, name string, status string, priority int32, timeoutMS *int32) int64 {
	t.Helper()

	return insertChannelWithBinding(t, ctx, tx, providerID, name, "openai", "openai", status, priority, timeoutMS)
}

// insertChannelWithBinding 插入指定 protocol 与 adapter_key 的测试 channel，用于验证同协议路由过滤。
func insertChannelWithBinding(t *testing.T, ctx context.Context, tx pgx.Tx, providerID int64, name string, protocol string, adapterKey string, status string, priority int32, timeoutMS *int32) int64 {
	t.Helper()

	var timeout any
	if timeoutMS != nil {
		timeout = *timeoutMS
	}

	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO channels (provider_id, name, protocols, adapter_key, credential, status, priority, response_timeout_ms)
		VALUES ($1, $2, ARRAY[$3]::text[], $4, $5, $6, $7, $8)
		RETURNING id
	`, providerID, name, protocol, adapterKey, "sk-test-"+name, status, priority, timeout).Scan(&id)
	if err != nil {
		t.Fatalf("insert channel %q: %v", name, err)
	}

	return id
}

// withRequestAttemptRuntimeIdentity freezes the real Origin and revision
// identity for request-attempt fixtures, matching the production insert contract.
func withRequestAttemptRuntimeIdentity(t *testing.T, ctx context.Context, tx pgx.Tx, channelID int64, params sqlc.CreateRequestAttemptParams) sqlc.CreateRequestAttemptParams {
	t.Helper()

	err := tx.QueryRow(ctx, `
		SELECT c.provider_id,
		       p.origin_revision,
		       p.status_revision,
		       c.config_revision
		FROM channels c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1
	`, channelID).Scan(
		&params.ProviderID,
		&params.ProviderOriginRevision,
		&params.ProviderStatusRevision,
		&params.ChannelConfigRevision,
	)
	if err != nil {
		t.Fatalf("load request-attempt runtime identity for channel %d: %v", channelID, err)
	}
	if params.UpstreamEndpoint == "" {
		params.UpstreamEndpoint = "chat_completions"
	}

	return params
}

// insertModel 插入测试 model，并返回数据库主键。
func insertModel(t *testing.T, ctx context.Context, tx pgx.Tx, modelID string, ownedBy string, status string) int64 {
	t.Helper()

	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO models (model_id, display_name, owned_by, status)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, modelID, modelID, ownedBy, status).Scan(&id)
	if err != nil {
		t.Fatalf("insert model %q: %v", modelID, err)
	}

	return id
}

// insertChannelModel 插入测试 channel model 映射。
func insertChannelModel(t *testing.T, ctx context.Context, tx pgx.Tx, channelID int64, modelID int64, upstreamModel string, status string) {
	t.Helper()

	_, err := tx.Exec(ctx, `
		INSERT INTO channel_models (channel_id, model_id, upstream_model, status)
		VALUES ($1, $2, $3, $4)
	`, channelID, modelID, upstreamModel, status)
	if err != nil {
		t.Fatalf("insert channel model %q: %v", upstreamModel, err)
	}
}

// createUserForModelPolicy 创建模型策略测试专用 user。
func createUserForModelPolicy(t *testing.T, ctx context.Context, queries *sqlc.Queries, suffix int64) int64 {
	t.Helper()

	user, err := queries.CreateUser(ctx, sqlc.CreateUserParams{
		Email:        fmt.Sprintf("model-policy-user-%d@example.test", suffix),
		PasswordHash: pgtype.Text{String: "hash", Valid: true},
		DisplayName:  "model policy user",
	})
	if err != nil {
		t.Fatalf("create model policy user: %v", err)
	}

	return user.ID
}

func listContainsModel(rows []sqlc.ListAvailableModelsRow, modelID string) bool {
	for _, row := range rows {
		if row.ModelID == modelID {
			return true
		}
	}

	return false
}

func TestListAvailableModelsListsEnabledModelsAndDerivesProtocols(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	timeoutMS := int32(30000)

	enabledProviderID := insertProvider(t, ctx, tx, fmt.Sprintf("catalog-openai-%d", suffix), "enabled")
	enabledChannelID := insertChannel(t, ctx, tx, enabledProviderID, fmt.Sprintf("catalog-openai-main-%d", suffix), "enabled", 10, &timeoutMS)
	duplicateChannelID := insertChannel(t, ctx, tx, enabledProviderID, fmt.Sprintf("catalog-openai-backup-%d", suffix), "enabled", 20, &timeoutMS)

	visibleModel := fmt.Sprintf("openai/catalog-visible-%d", suffix)
	visibleModelID := insertModel(t, ctx, tx, visibleModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, enabledChannelID, visibleModelID, "catalog-visible", "enabled")
	insertChannelModel(t, ctx, tx, duplicateChannelID, visibleModelID, "catalog-visible", "enabled")

	disabledModel := fmt.Sprintf("openai/catalog-disabled-model-%d", suffix)
	disabledModelID := insertModel(t, ctx, tx, disabledModel, "openai", "disabled")
	insertChannelModel(t, ctx, tx, enabledChannelID, disabledModelID, "catalog-disabled-model", "enabled")

	disabledMappingModel := fmt.Sprintf("openai/catalog-disabled-mapping-%d", suffix)
	disabledMappingModelID := insertModel(t, ctx, tx, disabledMappingModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, enabledChannelID, disabledMappingModelID, "catalog-disabled-mapping", "disabled")

	disabledChannelID := insertChannel(t, ctx, tx, enabledProviderID, fmt.Sprintf("catalog-disabled-channel-%d", suffix), "disabled", 10, &timeoutMS)
	disabledChannelModel := fmt.Sprintf("openai/catalog-disabled-channel-%d", suffix)
	disabledChannelModelID := insertModel(t, ctx, tx, disabledChannelModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, disabledChannelID, disabledChannelModelID, "catalog-disabled-channel", "enabled")

	disabledProviderID := insertProvider(t, ctx, tx, fmt.Sprintf("catalog-disabled-provider-%d", suffix), "disabled")
	disabledProviderChannelID := insertChannel(t, ctx, tx, disabledProviderID, fmt.Sprintf("catalog-disabled-provider-channel-%d", suffix), "enabled", 10, &timeoutMS)
	disabledProviderModel := fmt.Sprintf("openai/catalog-disabled-provider-%d", suffix)
	disabledProviderModelID := insertModel(t, ctx, tx, disabledProviderModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, disabledProviderChannelID, disabledProviderModelID, "catalog-disabled-provider", "enabled")

	now := time.Now().UTC()
	for _, modelID := range []int64{visibleModelID, disabledModelID, disabledMappingModelID, disabledChannelModelID, disabledProviderModelID} {
		createModelPriceForTest(t, ctx, queries, modelID, now)
	}
	createChannelPriceForTest(t, ctx, queries, enabledChannelID, visibleModelID, now)
	createChannelPriceForTest(t, ctx, queries, duplicateChannelID, visibleModelID, now)
	createChannelPriceForTest(t, ctx, queries, enabledChannelID, disabledModelID, now)
	createChannelPriceForTest(t, ctx, queries, enabledChannelID, disabledMappingModelID, now)
	createChannelPriceForTest(t, ctx, queries, disabledChannelID, disabledChannelModelID, now)
	createChannelPriceForTest(t, ctx, queries, disabledProviderChannelID, disabledProviderModelID, now)
	// 目录只看 models.status：模型 enabled 就列出，enabled 的前提是供给不变量已保证有可用渠道。
	// 绑定/渠道/服务商三级停用不再从目录里摘掉模型，而是体现在协议集合为空——
	// 这两件事必须分开，否则「列表里有」和「现在能调」会被混成同一个信号。
	got, err := queries.ListAvailableModels(ctx)
	if err != nil {
		t.Fatalf("list available models: %v", err)
	}

	listed := make(map[string]sqlc.ListAvailableModelsRow, len(got))
	for _, model := range got {
		listed[model.ModelID] = model
	}
	for _, want := range []string{visibleModel, disabledMappingModel, disabledChannelModel, disabledProviderModel} {
		if _, ok := listed[want]; !ok {
			t.Fatalf("enabled model %q must be listed, got %#v", want, got)
		}
	}
	if _, ok := listed[disabledModel]; ok {
		t.Fatalf("disabled model %q must not be listed", disabledModel)
	}
	if row := listed[visibleModel]; row.OwnedBy != "openai" || row.DisplayName != row.ModelID {
		t.Fatalf("unexpected catalog row for %q: %#v", visibleModel, row)
	}

	// 协议集合恒等于实际供给能力：只有 visibleModel 有可用渠道，其余三个虽在目录里但协议为空。
	protocolRows, err := queries.ListModelProtocols(ctx)
	if err != nil {
		t.Fatalf("list model protocols: %v", err)
	}
	protocols := make(map[string][]string, len(protocolRows))
	for _, row := range protocolRows {
		protocols[row.ModelID] = row.Protocols
	}
	if got := protocols[visibleModel]; len(got) != 1 || got[0] != "openai" {
		t.Fatalf("visible model protocols = %#v, want [openai]", got)
	}
	for _, unsupported := range []string{disabledMappingModel, disabledChannelModel, disabledProviderModel} {
		if got := protocols[unsupported]; len(got) != 0 {
			t.Fatalf("model %q has no usable channel, protocols must be empty, got %#v", unsupported, got)
		}
	}
}

func TestFindModelCandidatesOrdersAndFilters(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	timeoutMS := int32(15000)

	enabledProviderID := insertProvider(t, ctx, tx, fmt.Sprintf("routing-openai-%d", suffix), "enabled")
	requestedModel := fmt.Sprintf("openai/routing-gpt-%d", suffix)
	modelID := insertModel(t, ctx, tx, requestedModel, "openai", "enabled")

	fallbackChannelID := insertChannel(t, ctx, tx, enabledProviderID, fmt.Sprintf("routing-fallback-%d", suffix), "enabled", 20, &timeoutMS)
	primaryChannelID := insertChannel(t, ctx, tx, enabledProviderID, fmt.Sprintf("routing-primary-%d", suffix), "enabled", 10, &timeoutMS)
	secondaryChannelID := insertChannel(t, ctx, tx, enabledProviderID, fmt.Sprintf("routing-secondary-%d", suffix), "enabled", 10, &timeoutMS)
	disabledChannelID := insertChannel(t, ctx, tx, enabledProviderID, fmt.Sprintf("routing-disabled-channel-%d", suffix), "disabled", 0, &timeoutMS)
	disabledMappingChannelID := insertChannel(t, ctx, tx, enabledProviderID, fmt.Sprintf("routing-disabled-mapping-%d", suffix), "enabled", 0, &timeoutMS)

	disabledProviderID := insertProvider(t, ctx, tx, fmt.Sprintf("routing-disabled-provider-%d", suffix), "disabled")
	disabledProviderChannelID := insertChannel(t, ctx, tx, disabledProviderID, fmt.Sprintf("routing-disabled-provider-channel-%d", suffix), "enabled", 0, &timeoutMS)

	insertChannelModel(t, ctx, tx, fallbackChannelID, modelID, "gpt-routing-fallback", "enabled")
	insertChannelModel(t, ctx, tx, primaryChannelID, modelID, "gpt-routing-primary", "enabled")
	insertChannelModel(t, ctx, tx, secondaryChannelID, modelID, "gpt-routing-secondary", "enabled")
	insertChannelModel(t, ctx, tx, disabledChannelID, modelID, "gpt-routing-disabled-channel", "enabled")
	insertChannelModel(t, ctx, tx, disabledMappingChannelID, modelID, "gpt-routing-disabled-mapping", "disabled")
	insertChannelModel(t, ctx, tx, disabledProviderChannelID, modelID, "gpt-routing-disabled-provider", "enabled")

	// 阶段 15：FindModelCandidates 只返回「已定价」渠道，给 3 条预期候选各配一条 enabled 渠道-模型价。
	now := time.Now().UTC()
	createChannelPriceForTest(t, ctx, queries, fallbackChannelID, modelID, now)
	createChannelPriceForTest(t, ctx, queries, primaryChannelID, modelID, now)
	createChannelPriceForTest(t, ctx, queries, secondaryChannelID, modelID, now)
	// 模型需有基准价（× 售价倍率得客户售价），否则候选被过滤。
	createModelPriceForTest(t, ctx, queries, modelID, now)
	// 服务商需有当前生效充值汇率（D-02），否则其下渠道一律不进候选。
	createProviderRechargeRateForTest(t, ctx, queries, enabledProviderID, now)
	createProviderRechargeRateForTest(t, ctx, queries, disabledProviderID, now)

	got, err := queries.FindModelCandidates(ctx, modelCandidatesParams(requestedModel))
	if err != nil {
		t.Fatalf("find route candidates: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 route candidates, got %d: %#v", len(got), got)
	}

	wantChannelIDs := []int64{primaryChannelID, secondaryChannelID, fallbackChannelID}
	wantUpstreamModels := []string{"gpt-routing-primary", "gpt-routing-secondary", "gpt-routing-fallback"}
	for i := range wantChannelIDs {
		if got[i].ChannelID != wantChannelIDs[i] {
			t.Fatalf("candidate %d: expected channel id %d, got %d", i, wantChannelIDs[i], got[i].ChannelID)
		}
		if got[i].UpstreamModel != wantUpstreamModels[i] {
			t.Fatalf("candidate %d: expected upstream model %q, got %q", i, wantUpstreamModels[i], got[i].UpstreamModel)
		}
	}

	first := got[0]
	if first.RequestedModelID != requestedModel {
		t.Fatalf("expected requested model %q, got %q", requestedModel, first.RequestedModelID)
	}
	if first.AdapterKey != "openai" {
		t.Fatalf("expected adapter key %q, got %q", "openai", first.AdapterKey)
	}
	if first.ProviderSlug != fmt.Sprintf("routing-openai-%d", suffix) {
		t.Fatalf("expected provider slug for enabled provider, got %q", first.ProviderSlug)
	}
	if first.Credential == "" {
		t.Fatal("expected plaintext credential on route candidate")
	}
	if !first.ResponseTimeoutMs.Valid || first.ResponseTimeoutMs.Int32 != timeoutMS {
		t.Fatalf("expected response_timeout_ms %d, got valid=%v value=%d", timeoutMS, first.ResponseTimeoutMs.Valid, first.ResponseTimeoutMs.Int32)
	}

	disabledModel := fmt.Sprintf("openai/routing-disabled-model-%d", suffix)
	disabledModelID := insertModel(t, ctx, tx, disabledModel, "openai", "disabled")
	insertChannelModel(t, ctx, tx, primaryChannelID, disabledModelID, "gpt-routing-disabled-model", "enabled")

	disabledModelCandidates, err := queries.FindModelCandidates(ctx, modelCandidatesParams(disabledModel))
	if err != nil {
		t.Fatalf("find disabled model candidates: %v", err)
	}
	if len(disabledModelCandidates) != 0 {
		t.Fatalf("expected disabled model to have no candidates, got %d", len(disabledModelCandidates))
	}

	unknownCandidates, err := queries.FindModelCandidates(ctx, modelCandidatesParams(fmt.Sprintf("openai/routing-unknown-%d", suffix)))
	if err != nil {
		t.Fatalf("find unknown model candidates: %v", err)
	}
	if len(unknownCandidates) != 0 {
		t.Fatalf("expected unknown model to have no candidates, got %d", len(unknownCandidates))
	}
}

// TestFindModelCandidatesRequiresSchedulableAccountForPoolChannel 冻结池型渠道的候选资格：
// 池的供给单元是账号，零可调度账号的池不产生候选（「池空」与 breaker open 是两个事实），
// 有一个 enabled 账号即恢复候选。credential 型渠道不受该条件影响。
func TestFindModelCandidatesRequiresSchedulableAccountForPoolChannel(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	now := time.Now().UTC()

	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("pool-provider-%d", suffix), "enabled")
	defaultConcurrency := int32(3)
	poolChannelID := insertPoolChannel(t, ctx, tx, providerID, fmt.Sprintf("pool-channel-%d", suffix), &defaultConcurrency)
	if _, err := tx.Exec(ctx, `UPDATE channels SET status = 'enabled' WHERE id = $1`, poolChannelID); err != nil {
		t.Fatalf("enable pool channel: %v", err)
	}

	requestedModel := fmt.Sprintf("openai/pool-model-%d", suffix)
	modelID := insertModel(t, ctx, tx, requestedModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, poolChannelID, modelID, "gpt-5-codex", "enabled")
	createModelPriceForTest(t, ctx, queries, modelID, now)
	createChannelPriceForTest(t, ctx, queries, poolChannelID, modelID, now)
	createProviderRechargeRateForTest(t, ctx, queries, providerID, now)

	empty, err := queries.FindModelCandidates(ctx, modelCandidatesParams(requestedModel))
	if err != nil {
		t.Fatalf("find candidates for empty pool: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty pool must not produce candidates, got %d: %#v", len(empty), empty)
	}

	account := createAccount(t, ctx, queries, poolChannelID, fmt.Sprintf("acct-pool-%d", suffix), 50)
	// 导入落 disabled：还没显式启用之前，池依然算空。
	stillEmpty, err := queries.FindModelCandidates(ctx, modelCandidatesParams(requestedModel))
	if err != nil {
		t.Fatalf("find candidates for disabled-only pool: %v", err)
	}
	if len(stillEmpty) != 0 {
		t.Fatalf("pool with only disabled accounts must not produce candidates, got %d", len(stillEmpty))
	}

	enableAccount(t, ctx, queries, account.ID)
	got, err := queries.FindModelCandidates(ctx, modelCandidatesParams(requestedModel))
	if err != nil {
		t.Fatalf("find candidates for populated pool: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("populated pool must produce 1 candidate, got %d: %#v", len(got), got)
	}
	if got[0].SupplyForm != "pool" {
		t.Fatalf("candidate supply_form = %q, want pool", got[0].SupplyForm)
	}
	if got[0].Credential != "" {
		t.Fatalf("pool channel must not carry a channel credential, got %q", got[0].Credential)
	}
	if !got[0].AccountDefaultConcurrency.Valid || got[0].AccountDefaultConcurrency.Int32 != defaultConcurrency {
		t.Fatalf("account_default_concurrency = %#v, want %d", got[0].AccountDefaultConcurrency, defaultConcurrency)
	}
}

// createChannelPriceForTest 创建一条 enabled 渠道-模型成本价（成本 1/4），供路由「已配成本」过滤与计费测试（DEC-026）。
// effective_from 取 at-1h、effective_to 为空，保证在 at（及之后）时刻生效。
func createChannelPriceForTest(t *testing.T, ctx context.Context, queries *sqlc.Queries, channelID, modelID int64, at time.Time) sqlc.CreateChannelPriceRow {
	t.Helper()

	created, err := queries.CreateChannelPrice(ctx, sqlc.CreateChannelPriceParams{
		ChannelID:         channelID,
		ModelID:           modelID,
		Currency:          "USD",
		PricingUnit:       "per_1m_tokens",
		UncachedInputCost: numeric(1),
		OutputCost:        numeric(4),
		Status:            "enabled",
		EffectiveFrom:     timestamptz(at.Add(-time.Hour)),
		EffectiveTo:       nullTimestamptz(),
	})
	if err != nil {
		t.Fatalf("create channel price: %v", err)
	}
	return created
}

// createModelPriceForTest 创建一条 enabled 模型基准售价（model_prices，2/8），供 DEC-026 路由
// 「模型已配基准价」过滤：FindModelCandidates 要求模型有 active 基准价（× 线路倍率得客户售价）。
func createModelPriceForTest(t *testing.T, ctx context.Context, queries *sqlc.Queries, modelID int64, at time.Time) {
	t.Helper()

	if _, err := queries.CreateModelPrice(ctx, sqlc.CreateModelPriceParams{
		ModelID:     modelID,
		Currency:    "USD",
		PricingUnit: "per_1m_tokens",
		// 基准价相对渠道成本（1/4）留足倍数，毛利守卫不会误伤这些用例。
		UncachedInputPrice: numeric(100),
		OutputPrice:        numeric(400),
		// 倍率随价格行走，不再取自环境设置：按 1.0 卖即等于基准价。
		// 需要别的倍率时由用例自己 UPDATE，环境不再影响结果。
		SalePriceRatio: numeric(1),
		Status:         "enabled",
		EffectiveFrom:  timestamptz(at.Add(-time.Hour)),
		EffectiveTo:    nullTimestamptz(),
	}); err != nil {
		t.Fatalf("create model price: %v", err)
	}
}

// createProviderRechargeRateForTest 创建一条当前生效的服务商充值汇率（1.0，USD）。
// D-02 严格拦截：FindModelCandidates 要求服务商有当前生效充值汇率，否则其渠道一律不进候选。
// 汇率取 1.0 使倍率路径成本口径与基准价一致，不影响毛利守卫。
func createProviderRechargeRateForTest(t *testing.T, ctx context.Context, queries *sqlc.Queries, providerID int64, at time.Time) {
	t.Helper()

	if _, err := queries.CreateProviderRechargeRate(ctx, sqlc.CreateProviderRechargeRateParams{
		ProviderID:       providerID,
		ProviderCurrency: "USD",
		Rate:             numeric(1),
		Status:           "enabled",
		Source:           "manual",
		EffectiveFrom:    timestamptz(at.Add(-time.Hour)),
		EffectiveTo:      nullTimestamptz(),
	}); err != nil {
		t.Fatalf("create provider recharge rate: %v", err)
	}
}

// modelCandidatesParams 构造候选查询参数，at_time 取当前。
func modelCandidatesParams(model string) sqlc.FindModelCandidatesParams {
	return sqlc.FindModelCandidatesParams{
		RequestedModelID: model,
		IngressProtocol:  "openai",
		AtTime:           timestamptz(time.Now().UTC()),
	}
}

// insertModelCapability 插入测试 model capability 声明（source=manual）。
func insertModelCapability(t *testing.T, ctx context.Context, tx pgx.Tx, modelID int64, key string, supportLevel string) {
	t.Helper()

	_, err := tx.Exec(ctx, `
		INSERT INTO model_capabilities (model_id, capability_key, support_level)
		VALUES ($1, $2, $3)
	`, modelID, key, supportLevel)
	if err != nil {
		t.Fatalf("insert model capability %q: %v", key, err)
	}
}

// findAvailableModelRow 在可用模型列表中按对外 model_id 定位行，缺失即失败。
func findAvailableModelRow(t *testing.T, rows []sqlc.ListAvailableModelsRow, modelID string) sqlc.ListAvailableModelsRow {
	t.Helper()

	for _, row := range rows {
		if row.ModelID == modelID {
			return row
		}
	}

	t.Fatalf("model %q not found in available models %#v", modelID, rows)
	return sqlc.ListAvailableModelsRow{}
}

// TestListAvailableModelsForProjectReturnsCapTags 验证可用模型查询返回的 cap-tags：
// 只含 support_level<>'unsupported' 的能力、去重升序，未声明能力的模型为空数组。
func TestListAvailableModelsForProjectReturnsCapTags(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	timeoutMS := int32(15000)

	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("cap-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("cap-channel-%d", suffix), "enabled", 10, &timeoutMS)

	provisioned := fmt.Sprintf("openai/cap-provisioned-%d", suffix)
	provisionedID := insertModel(t, ctx, tx, provisioned, "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, provisionedID, "cap-provisioned", "enabled")
	insertModelCapability(t, ctx, tx, provisionedID, "text.output", "full")
	insertModelCapability(t, ctx, tx, provisionedID, "tools.function", "limited")
	insertModelCapability(t, ctx, tx, provisionedID, "image.input", "unsupported")

	unprovisioned := fmt.Sprintf("openai/cap-unprovisioned-%d", suffix)
	unprovisionedID := insertModel(t, ctx, tx, unprovisioned, "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, unprovisionedID, "cap-unprovisioned", "enabled")
	now := time.Now().UTC()
	createModelPriceForTest(t, ctx, queries, provisionedID, now)
	createModelPriceForTest(t, ctx, queries, unprovisionedID, now)
	createChannelPriceForTest(t, ctx, queries, channelID, provisionedID, now)
	createChannelPriceForTest(t, ctx, queries, channelID, unprovisionedID, now)

	models, err := queries.ListAvailableModels(ctx)
	if err != nil {
		t.Fatalf("list available models: %v", err)
	}

	provRow := findAvailableModelRow(t, models, provisioned)
	wantCaps := []string{"text.output", "tools.function"}
	if len(provRow.CapabilityKeys) != len(wantCaps) {
		t.Fatalf("provisioned cap-tags = %v, want %v (unsupported excluded, sorted)", provRow.CapabilityKeys, wantCaps)
	}
	for i, want := range wantCaps {
		if provRow.CapabilityKeys[i] != want {
			t.Fatalf("provisioned cap-tags[%d] = %q, want %q", i, provRow.CapabilityKeys[i], want)
		}
	}

	unprovRow := findAvailableModelRow(t, models, unprovisioned)
	if len(unprovRow.CapabilityKeys) != 0 {
		t.Fatalf("unprovisioned cap-tags = %v, want empty", unprovRow.CapabilityKeys)
	}
}
