package sqlc_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/credential"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
func insertProvider(t *testing.T, ctx context.Context, tx pgx.Tx, slug string, adapter string, status string) int64 {
	t.Helper()

	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO providers (slug, name, adapter, status)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, slug, slug, adapter, status).Scan(&id)
	if err != nil {
		t.Fatalf("insert provider %q: %v", slug, err)
	}

	return id
}

// insertChannel 插入测试 channel，并返回数据库主键。
func insertChannel(t *testing.T, ctx context.Context, tx pgx.Tx, providerID int64, name string, status string, priority int32, timeoutMS *int32) int64 {
	t.Helper()

	var timeout any
	if timeoutMS != nil {
		timeout = *timeoutMS
	}

	credentialEncrypted, err := credential.EncryptFixedTestCredential("sk-test-" + name)
	if err != nil {
		t.Fatalf("encrypt channel credential for %q: %v", name, err)
	}

	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO channels (provider_id, name, base_url, credential_encrypted, status, priority, timeout_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, providerID, name, "https://"+name+".example.test/v1", credentialEncrypted, status, priority, timeout).Scan(&id)
	if err != nil {
		t.Fatalf("insert channel %q: %v", name, err)
	}

	return id
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

// createProjectForModelPolicy 创建模型策略测试专用 project。
func createProjectForModelPolicy(t *testing.T, ctx context.Context, queries *sqlc.Queries, suffix int64) int64 {
	t.Helper()

	user, err := queries.CreateUser(ctx, sqlc.CreateUserParams{
		Email:        fmt.Sprintf("model-policy-user-%d@example.test", suffix),
		PasswordHash: "hash",
		DisplayName:  "model policy user",
	})
	if err != nil {
		t.Fatalf("create model policy user: %v", err)
	}

	project, err := queries.CreateProject(ctx, sqlc.CreateProjectParams{
		UserID: user.ID,
		Name:   fmt.Sprintf("model-policy-project-%d", suffix),
	})
	if err != nil {
		t.Fatalf("create model policy project: %v", err)
	}

	return project.ID
}

// insertProjectModelPolicy 插入 project/model 可见性覆盖策略。
func insertProjectModelPolicy(t *testing.T, ctx context.Context, tx pgx.Tx, projectID int64, modelID int64, visibility string) {
	t.Helper()

	_, err := tx.Exec(ctx, `
		INSERT INTO project_model_policies (project_id, model_id, visibility)
		VALUES ($1, $2, $3)
	`, projectID, modelID, visibility)
	if err != nil {
		t.Fatalf("insert project model policy %q: %v", visibility, err)
	}
}

func listContainsModel(rows []sqlc.ListAvailableModelsForProjectRow, modelID string) bool {
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
	projectID := createProjectForModelPolicy(t, ctx, queries, suffix)
	allowListProjectID := createProjectForModelPolicy(t, ctx, queries, suffix+1)

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

	allowed, err := queries.ProjectCanUseModel(ctx, sqlc.ProjectCanUseModelParams{
		ProjectID:        projectID,
		RequestedModelID: noChannelModel,
	})
	if err != nil {
		t.Fatalf("check project can use no-channel model: %v", err)
	}
	if !allowed {
		t.Fatalf("expected project without policy to allow enabled model %q even without channel", noChannelModel)
	}

	insertProjectModelPolicy(t, ctx, tx, projectID, enabledModelID, "denied")
	allowed, err = queries.ProjectCanUseModel(ctx, sqlc.ProjectCanUseModelParams{
		ProjectID:        projectID,
		RequestedModelID: enabledModel,
	})
	if err != nil {
		t.Fatalf("check project denied model: %v", err)
	}
	if allowed {
		t.Fatalf("expected denied model %q to be unavailable for project", enabledModel)
	}

	insertProjectModelPolicy(t, ctx, tx, allowListProjectID, allowListedModelID, "allowed")
	allowed, err = queries.ProjectCanUseModel(ctx, sqlc.ProjectCanUseModelParams{
		ProjectID:        allowListProjectID,
		RequestedModelID: allowListedModel,
	})
	if err != nil {
		t.Fatalf("check project allow-listed model: %v", err)
	}
	if !allowed {
		t.Fatalf("expected allow-listed model %q to be available for project", allowListedModel)
	}

	allowed, err = queries.ProjectCanUseModel(ctx, sqlc.ProjectCanUseModelParams{
		ProjectID:        allowListProjectID,
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

	enabledProviderID := insertProvider(t, ctx, tx, fmt.Sprintf("catalog-openai-%d", suffix), "openai", "enabled")
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

	disabledProviderID := insertProvider(t, ctx, tx, fmt.Sprintf("catalog-disabled-provider-%d", suffix), "openai", "disabled")
	disabledProviderChannelID := insertChannel(t, ctx, tx, disabledProviderID, fmt.Sprintf("catalog-disabled-provider-channel-%d", suffix), "enabled", 1, &timeoutMS)
	disabledProviderModel := fmt.Sprintf("openai/catalog-disabled-provider-%d", suffix)
	disabledProviderModelID := insertModel(t, ctx, tx, disabledProviderModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, disabledProviderChannelID, disabledProviderModelID, "catalog-disabled-provider", "enabled")

	got, err := queries.ListAvailableModelsForProject(ctx, 1)
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

	zeroProject, err := queries.ListAvailableModelsForProject(ctx, 0)
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

	enabledProviderID := insertProvider(t, ctx, tx, fmt.Sprintf("routing-openai-%d", suffix), "openai", "enabled")
	requestedModel := fmt.Sprintf("openai/routing-gpt-%d", suffix)
	modelID := insertModel(t, ctx, tx, requestedModel, "openai", "enabled")

	fallbackChannelID := insertChannel(t, ctx, tx, enabledProviderID, fmt.Sprintf("routing-fallback-%d", suffix), "enabled", 20, &timeoutMS)
	primaryChannelID := insertChannel(t, ctx, tx, enabledProviderID, fmt.Sprintf("routing-primary-%d", suffix), "enabled", 10, &timeoutMS)
	secondaryChannelID := insertChannel(t, ctx, tx, enabledProviderID, fmt.Sprintf("routing-secondary-%d", suffix), "enabled", 10, &timeoutMS)
	disabledChannelID := insertChannel(t, ctx, tx, enabledProviderID, fmt.Sprintf("routing-disabled-channel-%d", suffix), "disabled", 0, &timeoutMS)
	disabledMappingChannelID := insertChannel(t, ctx, tx, enabledProviderID, fmt.Sprintf("routing-disabled-mapping-%d", suffix), "enabled", 0, &timeoutMS)

	disabledProviderID := insertProvider(t, ctx, tx, fmt.Sprintf("routing-disabled-provider-%d", suffix), "openai", "disabled")
	disabledProviderChannelID := insertChannel(t, ctx, tx, disabledProviderID, fmt.Sprintf("routing-disabled-provider-channel-%d", suffix), "enabled", 0, &timeoutMS)

	insertChannelModel(t, ctx, tx, fallbackChannelID, modelID, "gpt-routing-fallback", "enabled")
	insertChannelModel(t, ctx, tx, primaryChannelID, modelID, "gpt-routing-primary", "enabled")
	insertChannelModel(t, ctx, tx, secondaryChannelID, modelID, "gpt-routing-secondary", "enabled")
	insertChannelModel(t, ctx, tx, disabledChannelID, modelID, "gpt-routing-disabled-channel", "enabled")
	insertChannelModel(t, ctx, tx, disabledMappingChannelID, modelID, "gpt-routing-disabled-mapping", "disabled")
	insertChannelModel(t, ctx, tx, disabledProviderChannelID, modelID, "gpt-routing-disabled-provider", "enabled")

	got, err := queries.FindRouteCandidates(ctx, sqlc.FindRouteCandidatesParams{
		RequestedModelID: requestedModel,
		ProjectID:        1,
	})
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
	if len(first.CredentialEncrypted) == 0 {
		t.Fatal("expected encrypted credential on route candidate")
	}
	if !first.TimeoutMs.Valid || first.TimeoutMs.Int32 != timeoutMS {
		t.Fatalf("expected timeout_ms %d, got valid=%v value=%d", timeoutMS, first.TimeoutMs.Valid, first.TimeoutMs.Int32)
	}

	disabledModel := fmt.Sprintf("openai/routing-disabled-model-%d", suffix)
	disabledModelID := insertModel(t, ctx, tx, disabledModel, "openai", "disabled")
	insertChannelModel(t, ctx, tx, primaryChannelID, disabledModelID, "gpt-routing-disabled-model", "enabled")

	disabledModelCandidates, err := queries.FindRouteCandidates(ctx, sqlc.FindRouteCandidatesParams{
		RequestedModelID: disabledModel,
		ProjectID:        1,
	})
	if err != nil {
		t.Fatalf("find disabled model candidates: %v", err)
	}
	if len(disabledModelCandidates) != 0 {
		t.Fatalf("expected disabled model to have no candidates, got %d", len(disabledModelCandidates))
	}

	unknownCandidates, err := queries.FindRouteCandidates(ctx, sqlc.FindRouteCandidatesParams{
		RequestedModelID: fmt.Sprintf("openai/routing-unknown-%d", suffix),
		ProjectID:        1,
	})
	if err != nil {
		t.Fatalf("find unknown model candidates: %v", err)
	}
	if len(unknownCandidates) != 0 {
		t.Fatalf("expected unknown model to have no candidates, got %d", len(unknownCandidates))
	}
}

func TestProjectModelPolicyDeniedFiltersCatalogAndRouting(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	timeoutMS := int32(15000)
	projectID := createProjectForModelPolicy(t, ctx, queries, suffix)

	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("policy-deny-provider-%d", suffix), "openai", "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("policy-deny-channel-%d", suffix), "enabled", 10, &timeoutMS)

	visibleModel := fmt.Sprintf("openai/policy-visible-%d", suffix)
	visibleModelID := insertModel(t, ctx, tx, visibleModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, visibleModelID, "policy-visible", "enabled")

	deniedModel := fmt.Sprintf("openai/policy-denied-%d", suffix)
	deniedModelID := insertModel(t, ctx, tx, deniedModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, deniedModelID, "policy-denied", "enabled")
	insertProjectModelPolicy(t, ctx, tx, projectID, deniedModelID, "denied")

	models, err := queries.ListAvailableModelsForProject(ctx, projectID)
	if err != nil {
		t.Fatalf("list available models for denied policy: %v", err)
	}
	if !listContainsModel(models, visibleModel) {
		t.Fatalf("expected visible model %q in catalog, got %#v", visibleModel, models)
	}
	if listContainsModel(models, deniedModel) {
		t.Fatalf("expected denied model %q to be filtered from catalog, got %#v", deniedModel, models)
	}

	visibleCandidates, err := queries.FindRouteCandidates(ctx, sqlc.FindRouteCandidatesParams{
		RequestedModelID: visibleModel,
		ProjectID:        projectID,
	})
	if err != nil {
		t.Fatalf("find visible route candidates: %v", err)
	}
	if len(visibleCandidates) != 1 {
		t.Fatalf("expected visible model to have 1 candidate, got %d: %#v", len(visibleCandidates), visibleCandidates)
	}

	deniedCandidates, err := queries.FindRouteCandidates(ctx, sqlc.FindRouteCandidatesParams{
		RequestedModelID: deniedModel,
		ProjectID:        projectID,
	})
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
	projectID := createProjectForModelPolicy(t, ctx, queries, suffix)

	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("policy-allow-provider-%d", suffix), "openai", "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("policy-allow-channel-%d", suffix), "enabled", 10, &timeoutMS)

	allowedModel := fmt.Sprintf("openai/policy-allowed-%d", suffix)
	allowedModelID := insertModel(t, ctx, tx, allowedModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, allowedModelID, "policy-allowed", "enabled")
	insertProjectModelPolicy(t, ctx, tx, projectID, allowedModelID, "allowed")

	inheritedModel := fmt.Sprintf("openai/policy-inherited-%d", suffix)
	inheritedModelID := insertModel(t, ctx, tx, inheritedModel, "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, inheritedModelID, "policy-inherited", "enabled")

	models, err := queries.ListAvailableModelsForProject(ctx, projectID)
	if err != nil {
		t.Fatalf("list available models for allow-list policy: %v", err)
	}
	if !listContainsModel(models, allowedModel) {
		t.Fatalf("expected allowed model %q in catalog, got %#v", allowedModel, models)
	}
	if listContainsModel(models, inheritedModel) {
		t.Fatalf("expected inherited model %q to be filtered in allow-list mode, got %#v", inheritedModel, models)
	}

	allowedCandidates, err := queries.FindRouteCandidates(ctx, sqlc.FindRouteCandidatesParams{
		RequestedModelID: allowedModel,
		ProjectID:        projectID,
	})
	if err != nil {
		t.Fatalf("find allowed route candidates: %v", err)
	}
	if len(allowedCandidates) != 1 {
		t.Fatalf("expected allowed model to have 1 candidate, got %d: %#v", len(allowedCandidates), allowedCandidates)
	}

	inheritedCandidates, err := queries.FindRouteCandidates(ctx, sqlc.FindRouteCandidatesParams{
		RequestedModelID: inheritedModel,
		ProjectID:        projectID,
	})
	if err != nil {
		t.Fatalf("find inherited route candidates: %v", err)
	}
	if len(inheritedCandidates) != 0 {
		t.Fatalf("expected inherited model to have no candidates in allow-list mode, got %d: %#v", len(inheritedCandidates), inheritedCandidates)
	}
}
