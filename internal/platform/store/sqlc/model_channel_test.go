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
		INSERT INTO providers (slug, name, status)
		VALUES ($1, $2, $3)
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

// insertProviderOrigin 插入测试 ProviderOrigin（唯一 base_url），返回主键（P4 §4.2）。
func insertProviderOrigin(t *testing.T, ctx context.Context, tx pgx.Tx, providerID int64, name string, baseURL string, status string) int64 {
	t.Helper()

	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO provider_origins (provider_id, name, base_url, status)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, providerID, name, baseURL, status).Scan(&id)
	if err != nil {
		t.Fatalf("insert provider origin %q: %v", name, err)
	}

	return id
}

// insertChannelWithBinding 插入指定 protocol 与 adapter_key 的测试 channel，用于验证同协议路由过滤。
// P4 §4.4：base_url 归属 ProviderOrigin，本 helper 为每个 channel 自动建一个同 Provider 下的 enabled Origin 并绑定。
func insertChannelWithBinding(t *testing.T, ctx context.Context, tx pgx.Tx, providerID int64, name string, protocol string, adapterKey string, status string, priority int32, timeoutMS *int32) int64 {
	t.Helper()

	var timeout any
	if timeoutMS != nil {
		timeout = *timeoutMS
	}

	originID := insertProviderOrigin(t, ctx, tx, providerID, "ep-"+name, "https://"+name+".example.test", "enabled")

	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO channels (provider_id, provider_origin_id, name, protocol, adapter_key, credential, status, priority, timeout_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id
	`, providerID, originID, name, protocol, adapterKey, "sk-test-"+name, status, priority, timeout).Scan(&id)
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
		SELECT c.provider_origin_id,
		       pe.base_url_revision,
		       pe.status_revision,
		       c.config_revision
		FROM channels c
		JOIN provider_origins pe ON pe.id = c.provider_origin_id
		WHERE c.id = $1
	`, channelID).Scan(
		&params.ProviderOriginID,
		&params.ProviderOriginBaseUrlRevision,
		&params.ProviderOriginStatusRevision,
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

// insertRouteWithChannels 创建 balanced 测试线路，并把给定渠道显式加入其唯一候选池。
func insertRouteWithChannels(t *testing.T, ctx context.Context, tx pgx.Tx, channelIDs ...int64) int64 {
	t.Helper()

	var routeID int64
	err := tx.QueryRow(ctx, `
		INSERT INTO routes (name, mode, status, price_ratio)
		VALUES ($1, 'balanced', 'enabled', 1)
		RETURNING id
	`, fmt.Sprintf("test-route-%d", time.Now().UnixNano())).Scan(&routeID)
	if err != nil {
		t.Fatalf("insert test route: %v", err)
	}
	for _, channelID := range channelIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO route_channels (route_id, channel_id) VALUES ($1, $2)
		`, routeID, channelID); err != nil {
			t.Fatalf("bind channel %d to route %d: %v", channelID, routeID, err)
		}
	}
	return routeID
}

// createUserForModelPolicy 创建模型策略测试专用 user。
func createUserForModelPolicy(t *testing.T, ctx context.Context, queries *sqlc.Queries, suffix int64) int64 {
	t.Helper()

	user, err := queries.CreateUser(ctx, sqlc.CreateUserParams{
		Email:        fmt.Sprintf("model-policy-user-%d@example.test", suffix),
		PasswordHash: "hash",
		DisplayName:  "model policy user",
	})
	if err != nil {
		t.Fatalf("create model policy user: %v", err)
	}

	return user.ID
}

// insertUserModelPolicy 插入 user/model 可见性覆盖策略。
func insertUserModelPolicy(t *testing.T, ctx context.Context, tx pgx.Tx, userID int64, modelID int64, visibility string) {
	t.Helper()

	_, err := tx.Exec(ctx, `
		INSERT INTO user_model_policies (user_id, model_id, visibility)
		VALUES ($1, $2, $3)
	`, userID, modelID, visibility)
	if err != nil {
		t.Fatalf("insert user model policy %q: %v", visibility, err)
	}
}

func listContainsModel(rows []sqlc.ListAvailableModelsForUserRow, modelID string) bool {
	for _, row := range rows {
		if row.ModelID == modelID {
			return true
		}
	}

	return false
}

func TestRoutingDiagnosisQueriesClassifyModelAndProjectPolicy(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	projectID := createUserForModelPolicy(t, ctx, queries, suffix)
	allowListProjectID := createUserForModelPolicy(t, ctx, queries, suffix+1)

	enabledModel := fmt.Sprintf("openai/diagnosis-enabled-%d", suffix)
	enabledModelID := insertModel(t, ctx, tx, enabledModel, "openai", "enabled")
	disabledModel := fmt.Sprintf("openai/diagnosis-disabled-%d", suffix)
	insertModel(t, ctx, tx, disabledModel, "openai", "disabled")
	noChannelModel := fmt.Sprintf("openai/diagnosis-no-channel-%d", suffix)
	insertModel(t, ctx, tx, noChannelModel, "openai", "enabled")
	allowListedModel := fmt.Sprintf("openai/diagnosis-allowed-%d", suffix)
	allowListedModelID := insertModel(t, ctx, tx, allowListedModel, "openai", "enabled")

	exists, err := queries.ModelExistsByID(ctx, enabledModel)
	if err != nil {
		t.Fatalf("check enabled model exists: %v", err)
	}
	if !exists {
		t.Fatalf("expected enabled model %q to exist", enabledModel)
	}

	exists, err = queries.ModelExistsByID(ctx, disabledModel)
	if err != nil {
		t.Fatalf("check disabled model exists: %v", err)
	}
	if exists {
		t.Fatalf("expected disabled model %q to be treated as not found", disabledModel)
	}

	exists, err = queries.ModelExistsByID(ctx, fmt.Sprintf("openai/diagnosis-missing-%d", suffix))
	if err != nil {
		t.Fatalf("check missing model exists: %v", err)
	}
	if exists {
		t.Fatal("expected missing model to be treated as not found")
	}

	allowed, err := queries.UserCanUseModel(ctx, sqlc.UserCanUseModelParams{
		UserID:           projectID,
		RequestedModelID: noChannelModel,
	})
	if err != nil {
		t.Fatalf("check project can use no-channel model: %v", err)
	}
	if !allowed {
		t.Fatalf("expected project without policy to allow enabled model %q even without channel", noChannelModel)
	}

	insertUserModelPolicy(t, ctx, tx, projectID, enabledModelID, "denied")
	allowed, err = queries.UserCanUseModel(ctx, sqlc.UserCanUseModelParams{
		UserID:           projectID,
		RequestedModelID: enabledModel,
	})
	if err != nil {
		t.Fatalf("check project denied model: %v", err)
	}
	if allowed {
		t.Fatalf("expected denied model %q to be unavailable for project", enabledModel)
	}

	insertUserModelPolicy(t, ctx, tx, allowListProjectID, allowListedModelID, "allowed")
	allowed, err = queries.UserCanUseModel(ctx, sqlc.UserCanUseModelParams{
		UserID:           allowListProjectID,
		RequestedModelID: allowListedModel,
	})
	if err != nil {
		t.Fatalf("check project allow-listed model: %v", err)
	}
	if !allowed {
		t.Fatalf("expected allow-listed model %q to be available for project", allowListedModel)
	}

	allowed, err = queries.UserCanUseModel(ctx, sqlc.UserCanUseModelParams{
		UserID:           allowListProjectID,
		RequestedModelID: noChannelModel,
	})
	if err != nil {
		t.Fatalf("check project inherited model in allow-list mode: %v", err)
	}
	if allowed {
		t.Fatalf("expected inherited model %q to be unavailable in allow-list mode", noChannelModel)
	}
}

func TestListAvailableModelsForProjectFiltersDisabledRelations(t *testing.T) {
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

	disabledChannelID := insertChannel(t, ctx, tx, enabledProviderID, fmt.Sprintf("catalog-disabled-channel-%d", suffix), "disabled", 1, &timeoutMS)
	disabledChannelModel := fmt.Sprintf("openai/catalog-disabled-channel-%d", suffix)
	disabledChannelModelID := insertModel(t, ctx, tx, disabledChannelModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, disabledChannelID, disabledChannelModelID, "catalog-disabled-channel", "enabled")

	disabledProviderID := insertProvider(t, ctx, tx, fmt.Sprintf("catalog-disabled-provider-%d", suffix), "disabled")
	disabledProviderChannelID := insertChannel(t, ctx, tx, disabledProviderID, fmt.Sprintf("catalog-disabled-provider-channel-%d", suffix), "enabled", 1, &timeoutMS)
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
	routeID := insertRouteWithChannels(t, ctx, tx, enabledChannelID, duplicateChannelID, disabledChannelID, disabledProviderChannelID)

	got, err := queries.ListAvailableModelsForUser(ctx, sqlc.ListAvailableModelsForUserParams{UserID: 1, RouteID: routeID})
	if err != nil {
		t.Fatalf("list available models: %v", err)
	}

	visibleCount := 0
	disabledModels := map[string]bool{
		disabledModel:         true,
		disabledMappingModel:  true,
		disabledChannelModel:  true,
		disabledProviderModel: true,
	}
	for _, model := range got {
		if model.ModelID == visibleModel {
			visibleCount++
			if model.OwnedBy != "openai" {
				t.Fatalf("expected owned_by %q, got %q", "openai", model.OwnedBy)
			}
			if model.DisplayName != model.ModelID {
				t.Fatalf("expected display name %q, got %q", model.ModelID, model.DisplayName)
			}
		}

		if disabledModels[model.ModelID] {
			t.Fatalf("expected disabled relation model %q to be filtered", model.ModelID)
		}
	}

	if visibleCount != 1 {
		t.Fatalf("expected visible model %q once, got %d in %#v", visibleModel, visibleCount, got)
	}

	zeroProject, err := queries.ListAvailableModelsForUser(ctx, sqlc.ListAvailableModelsForUserParams{UserID: 0, RouteID: routeID})
	if err != nil {
		t.Fatalf("list available models for zero project: %v", err)
	}
	if len(zeroProject) != 0 {
		t.Fatalf("expected zero project to see no models, got %d", len(zeroProject))
	}
}

func TestFindRouteCandidatesOrdersAndFilters(t *testing.T) {
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

	// 阶段 15：FindRouteCandidates 只返回「已定价」渠道，给 3 条预期候选各配一条 enabled 渠道-模型价。
	now := time.Now().UTC()
	createChannelPriceForTest(t, ctx, queries, fallbackChannelID, modelID, now)
	createChannelPriceForTest(t, ctx, queries, primaryChannelID, modelID, now)
	createChannelPriceForTest(t, ctx, queries, secondaryChannelID, modelID, now)
	// DEC-026：模型需有基准价（× 线路倍率得客户售价），否则候选被过滤。
	createModelPriceForTest(t, ctx, queries, modelID, now)
	routeID := insertRouteWithChannels(t, ctx, tx,
		fallbackChannelID, primaryChannelID, secondaryChannelID,
		disabledChannelID, disabledMappingChannelID, disabledProviderChannelID,
	)

	got, err := queries.FindRouteCandidates(ctx, routeCandidatesParams(requestedModel, 1, routeID))
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
	if !first.TimeoutMs.Valid || first.TimeoutMs.Int32 != timeoutMS {
		t.Fatalf("expected timeout_ms %d, got valid=%v value=%d", timeoutMS, first.TimeoutMs.Valid, first.TimeoutMs.Int32)
	}

	disabledModel := fmt.Sprintf("openai/routing-disabled-model-%d", suffix)
	disabledModelID := insertModel(t, ctx, tx, disabledModel, "openai", "disabled")
	insertChannelModel(t, ctx, tx, primaryChannelID, disabledModelID, "gpt-routing-disabled-model", "enabled")

	disabledModelCandidates, err := queries.FindRouteCandidates(ctx, routeCandidatesParams(disabledModel, 1, routeID))
	if err != nil {
		t.Fatalf("find disabled model candidates: %v", err)
	}
	if len(disabledModelCandidates) != 0 {
		t.Fatalf("expected disabled model to have no candidates, got %d", len(disabledModelCandidates))
	}

	unknownCandidates, err := queries.FindRouteCandidates(ctx, routeCandidatesParams(fmt.Sprintf("openai/routing-unknown-%d", suffix), 1, routeID))
	if err != nil {
		t.Fatalf("find unknown model candidates: %v", err)
	}
	if len(unknownCandidates) != 0 {
		t.Fatalf("expected unknown model to have no candidates, got %d", len(unknownCandidates))
	}
}

func TestRoutePoolExcludesUnboundChannelAndModel(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	timeoutMS := int32(15000)
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("route-boundary-%d", suffix), "enabled")
	boundChannelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("route-bound-%d", suffix), "enabled", 20, &timeoutMS)
	unboundChannelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("route-unbound-%d", suffix), "enabled", 1, &timeoutMS)

	sharedModel := fmt.Sprintf("openai/route-shared-%d", suffix)
	sharedModelID := insertModel(t, ctx, tx, sharedModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, boundChannelID, sharedModelID, "shared-bound", "enabled")
	insertChannelModel(t, ctx, tx, unboundChannelID, sharedModelID, "shared-unbound", "enabled")

	unboundOnlyModel := fmt.Sprintf("openai/route-unbound-only-%d", suffix)
	unboundOnlyModelID := insertModel(t, ctx, tx, unboundOnlyModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, unboundChannelID, unboundOnlyModelID, "unbound-only", "enabled")

	now := time.Now().UTC()
	createModelPriceForTest(t, ctx, queries, sharedModelID, now)
	createModelPriceForTest(t, ctx, queries, unboundOnlyModelID, now)
	createChannelPriceForTest(t, ctx, queries, boundChannelID, sharedModelID, now)
	createChannelPriceForTest(t, ctx, queries, unboundChannelID, sharedModelID, now)
	createChannelPriceForTest(t, ctx, queries, unboundChannelID, unboundOnlyModelID, now)
	routeID := insertRouteWithChannels(t, ctx, tx, boundChannelID)

	candidates, err := queries.FindRouteCandidates(ctx, routeCandidatesParams(sharedModel, 1, routeID))
	if err != nil {
		t.Fatalf("find route-bound candidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ChannelID != boundChannelID {
		t.Fatalf("expected only bound channel %d, got %#v", boundChannelID, candidates)
	}

	models, err := queries.ListAvailableModelsForUser(ctx, sqlc.ListAvailableModelsForUserParams{UserID: 1, RouteID: routeID})
	if err != nil {
		t.Fatalf("list route-bound models: %v", err)
	}
	if !listContainsModel(models, sharedModel) {
		t.Fatalf("expected shared model %q to remain visible, got %#v", sharedModel, models)
	}
	if listContainsModel(models, unboundOnlyModel) {
		t.Fatalf("unbound-only model %q must not be visible, got %#v", unboundOnlyModel, models)
	}
}

func TestProjectModelPolicyDeniedFiltersCatalogAndRouting(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	timeoutMS := int32(15000)
	projectID := createUserForModelPolicy(t, ctx, queries, suffix)

	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("policy-deny-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("policy-deny-channel-%d", suffix), "enabled", 10, &timeoutMS)

	visibleModel := fmt.Sprintf("openai/policy-visible-%d", suffix)
	visibleModelID := insertModel(t, ctx, tx, visibleModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, visibleModelID, "policy-visible", "enabled")

	deniedModel := fmt.Sprintf("openai/policy-denied-%d", suffix)
	deniedModelID := insertModel(t, ctx, tx, deniedModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, deniedModelID, "policy-denied", "enabled")
	insertUserModelPolicy(t, ctx, tx, projectID, deniedModelID, "denied")

	// 阶段 15：已定价过滤——给可见/被拒模型各配价，确保候选差异来自策略而非缺价。
	createChannelPriceForTest(t, ctx, queries, channelID, visibleModelID, time.Now().UTC())
	createChannelPriceForTest(t, ctx, queries, channelID, deniedModelID, time.Now().UTC())
	createModelPriceForTest(t, ctx, queries, visibleModelID, time.Now().UTC())
	createModelPriceForTest(t, ctx, queries, deniedModelID, time.Now().UTC())
	routeID := insertRouteWithChannels(t, ctx, tx, channelID)

	models, err := queries.ListAvailableModelsForUser(ctx, sqlc.ListAvailableModelsForUserParams{UserID: projectID, RouteID: routeID})
	if err != nil {
		t.Fatalf("list available models for denied policy: %v", err)
	}
	if !listContainsModel(models, visibleModel) {
		t.Fatalf("expected visible model %q in catalog, got %#v", visibleModel, models)
	}
	if listContainsModel(models, deniedModel) {
		t.Fatalf("expected denied model %q to be filtered from catalog, got %#v", deniedModel, models)
	}

	visibleCandidates, err := queries.FindRouteCandidates(ctx, routeCandidatesParams(visibleModel, projectID, routeID))
	if err != nil {
		t.Fatalf("find visible route candidates: %v", err)
	}
	if len(visibleCandidates) != 1 {
		t.Fatalf("expected visible model to have 1 candidate, got %d: %#v", len(visibleCandidates), visibleCandidates)
	}

	deniedCandidates, err := queries.FindRouteCandidates(ctx, routeCandidatesParams(deniedModel, projectID, routeID))
	if err != nil {
		t.Fatalf("find denied route candidates: %v", err)
	}
	if len(deniedCandidates) != 0 {
		t.Fatalf("expected denied model to have no candidates, got %d: %#v", len(deniedCandidates), deniedCandidates)
	}
}

func TestProjectModelPolicyAllowedEnablesAllowListMode(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	timeoutMS := int32(15000)
	projectID := createUserForModelPolicy(t, ctx, queries, suffix)

	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("policy-allow-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("policy-allow-channel-%d", suffix), "enabled", 10, &timeoutMS)

	allowedModel := fmt.Sprintf("openai/policy-allowed-%d", suffix)
	allowedModelID := insertModel(t, ctx, tx, allowedModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, allowedModelID, "policy-allowed", "enabled")
	insertUserModelPolicy(t, ctx, tx, projectID, allowedModelID, "allowed")

	inheritedModel := fmt.Sprintf("openai/policy-inherited-%d", suffix)
	inheritedModelID := insertModel(t, ctx, tx, inheritedModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, inheritedModelID, "policy-inherited", "enabled")

	// 阶段 15：已定价过滤——给允许/继承模型各配价，确保候选差异来自 allow-list 而非缺价。
	createChannelPriceForTest(t, ctx, queries, channelID, allowedModelID, time.Now().UTC())
	createChannelPriceForTest(t, ctx, queries, channelID, inheritedModelID, time.Now().UTC())
	createModelPriceForTest(t, ctx, queries, allowedModelID, time.Now().UTC())
	createModelPriceForTest(t, ctx, queries, inheritedModelID, time.Now().UTC())
	routeID := insertRouteWithChannels(t, ctx, tx, channelID)

	models, err := queries.ListAvailableModelsForUser(ctx, sqlc.ListAvailableModelsForUserParams{UserID: projectID, RouteID: routeID})
	if err != nil {
		t.Fatalf("list available models for allow-list policy: %v", err)
	}
	if !listContainsModel(models, allowedModel) {
		t.Fatalf("expected allowed model %q in catalog, got %#v", allowedModel, models)
	}
	if listContainsModel(models, inheritedModel) {
		t.Fatalf("expected inherited model %q to be filtered in allow-list mode, got %#v", inheritedModel, models)
	}

	allowedCandidates, err := queries.FindRouteCandidates(ctx, routeCandidatesParams(allowedModel, projectID, routeID))
	if err != nil {
		t.Fatalf("find allowed route candidates: %v", err)
	}
	if len(allowedCandidates) != 1 {
		t.Fatalf("expected allowed model to have 1 candidate, got %d: %#v", len(allowedCandidates), allowedCandidates)
	}

	inheritedCandidates, err := queries.FindRouteCandidates(ctx, routeCandidatesParams(inheritedModel, projectID, routeID))
	if err != nil {
		t.Fatalf("find inherited route candidates: %v", err)
	}
	if len(inheritedCandidates) != 0 {
		t.Fatalf("expected inherited model to have no candidates in allow-list mode, got %d: %#v", len(inheritedCandidates), inheritedCandidates)
	}
}

// createChannelPriceForTest 创建一条 enabled 渠道-模型成本价（成本 1/4），供路由「已配成本」过滤与计费测试（DEC-026）。
// effective_from 取 at-1h、effective_to 为空，保证在 at（及之后）时刻生效。
func createChannelPriceForTest(t *testing.T, ctx context.Context, queries *sqlc.Queries, channelID, modelID int64, at time.Time) sqlc.ChannelPrice {
	t.Helper()

	price, err := queries.CreateChannelPrice(ctx, sqlc.CreateChannelPriceParams{
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
	return price
}

// createModelPriceForTest 创建一条 enabled 模型基准售价（model_prices，2/8），供 DEC-026 路由
// 「模型已配基准价」过滤：FindRouteCandidates 要求模型有 active 基准价（× 线路倍率得客户售价）。
func createModelPriceForTest(t *testing.T, ctx context.Context, queries *sqlc.Queries, modelID int64, at time.Time) {
	t.Helper()

	if _, err := queries.CreateModelPrice(ctx, sqlc.CreateModelPriceParams{
		ModelID:            modelID,
		Currency:           "USD",
		PricingUnit:        "per_1m_tokens",
		UncachedInputPrice: numeric(2),
		OutputPrice:        numeric(8),
		Status:             "enabled",
		EffectiveFrom:      timestamptz(at.Add(-time.Hour)),
		EffectiveTo:        nullTimestamptz(),
	}); err != nil {
		t.Fatalf("create model price: %v", err)
	}
}

// routeCandidatesParams 构造显式线路池的候选查询参数，at_time 取当前。
func routeCandidatesParams(model string, projectID, routeID int64) sqlc.FindRouteCandidatesParams {
	return sqlc.FindRouteCandidatesParams{
		RequestedModelID: model,
		IngressProtocol:  "openai",
		UserID:           projectID,
		RouteID:          routeID,
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
func findAvailableModelRow(t *testing.T, rows []sqlc.ListAvailableModelsForUserRow, modelID string) sqlc.ListAvailableModelsForUserRow {
	t.Helper()

	for _, row := range rows {
		if row.ModelID == modelID {
			return row
		}
	}

	t.Fatalf("model %q not found in available models %#v", modelID, rows)
	return sqlc.ListAvailableModelsForUserRow{}
}

// TestListAvailableModelsForProjectReturnsCapTags 验证可用模型查询返回的 cap-tags：
// 只含 support_level<>'unsupported' 的能力、去重升序，未声明能力的模型为空数组。
func TestListAvailableModelsForProjectReturnsCapTags(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	timeoutMS := int32(15000)
	projectID := createUserForModelPolicy(t, ctx, queries, suffix)

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
	routeID := insertRouteWithChannels(t, ctx, tx, channelID)

	models, err := queries.ListAvailableModelsForUser(ctx, sqlc.ListAvailableModelsForUserParams{UserID: projectID, RouteID: routeID})
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
