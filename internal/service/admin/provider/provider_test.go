package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/provider"
)

type fakeProviderStore struct {
	providers               []sqlc.Provider
	getRow                  sqlc.Provider
	getErr                  error
	createRow               sqlc.Provider
	createErr               error
	createParam             sqlc.CreateProviderParams
	createCalls             int
	updateRow               sqlc.Provider
	updateErr               error
	deleteAff               int64
	deleteErr               error
	deleteID                int64
	deleteCalls             int
	archiveAff              int64
	archiveErr              error
	archiveID               int64
	getChannelRow           sqlc.Channel
	getChannelErr           error
	archiveReplacementAff   int64
	archiveReplacementParam sqlc.ArchiveProviderWithReplacementParams
	emptyRoutes             []sqlc.ListEnabledRoutesEmptiedByProviderRow
	emptyRouteErr           error
	restoreAff              int64
	restoreErr              error
	restoreID               int64
	endpoints               []sqlc.ProviderEndpoint
}

func (s *fakeProviderStore) ListProvidersPage(context.Context, sqlc.ListProvidersPageParams) ([]sqlc.Provider, error) {
	return s.providers, nil
}

func (s *fakeProviderStore) CountProviders(context.Context, sqlc.CountProvidersParams) (int64, error) {
	return int64(len(s.providers)), nil
}

func (s *fakeProviderStore) GetProvider(_ context.Context, _ int64) (sqlc.Provider, error) {
	return s.getRow, s.getErr
}

func (s *fakeProviderStore) GetChannel(_ context.Context, _ int64) (sqlc.Channel, error) {
	return s.getChannelRow, s.getChannelErr
}

func (s *fakeProviderStore) CreateProvider(_ context.Context, arg sqlc.CreateProviderParams) (sqlc.Provider, error) {
	s.createParam = arg
	s.createCalls++
	return s.createRow, s.createErr
}

func (s *fakeProviderStore) UpdateProvider(_ context.Context, _ sqlc.UpdateProviderParams) (sqlc.Provider, error) {
	return s.updateRow, s.updateErr
}

func (s *fakeProviderStore) DeleteProvider(_ context.Context, id int64) (int64, error) {
	s.deleteID = id
	s.deleteCalls++
	return s.deleteAff, s.deleteErr
}

func (s *fakeProviderStore) ArchiveProviderCascade(_ context.Context, id int64) (int64, error) {
	s.archiveID = id
	return s.archiveAff, s.archiveErr
}

func (s *fakeProviderStore) ArchiveProviderWithReplacement(_ context.Context, arg sqlc.ArchiveProviderWithReplacementParams) (int64, error) {
	s.archiveReplacementParam = arg
	return s.archiveReplacementAff, s.archiveErr
}

func (s *fakeProviderStore) ListEnabledRoutesEmptiedByProvider(context.Context, int64) ([]sqlc.ListEnabledRoutesEmptiedByProviderRow, error) {
	return s.emptyRoutes, s.emptyRouteErr
}

func (s *fakeProviderStore) RestoreProvider(_ context.Context, id int64) (int64, error) {
	s.restoreID = id
	return s.restoreAff, s.restoreErr
}

func (s *fakeProviderStore) ListProviderEndpointsByProvider(context.Context, int64) ([]sqlc.ProviderEndpoint, error) {
	return s.endpoints, nil
}

type fakeProviderFencePublisher struct {
	result runtimecontrol.PublishResult
}

func (p *fakeProviderFencePublisher) Publish(context.Context, runtimecontrol.EndpointFenceRequest) (runtimecontrol.PublishResult, error) {
	return p.result, nil
}

func (*fakeProviderFencePublisher) WithEndpointLocks(context.Context, int64, []int64, func(context.Context, pgx.Tx) error) error {
	return nil
}

type fakeProviderFenceOps struct{}

func (fakeProviderFenceOps) PrepareEndpointStatusRevisionBatch(context.Context, int64, []breakerstore.EndpointStatusRevisionTransition, int, string, string) (breakerstore.FenceResult, error) {
	return "", nil
}

func (fakeProviderFenceOps) CommitEndpointStatusRevisionBatch(context.Context, int64, []breakerstore.EndpointStatusRevisionTransition, string, string) (breakerstore.FenceResult, error) {
	return "", nil
}

func (fakeProviderFenceOps) AbortEndpointStatusRevisionBatch(context.Context, int64, []breakerstore.EndpointStatusRevisionTransition, string, string) (breakerstore.FenceResult, error) {
	return "", nil
}

func TestCreateRejectsInvalidArguments(t *testing.T) {
	cases := []struct {
		name string
		in   provider.CreateInput
	}{
		{"bad slug", provider.CreateInput{Slug: "Bad Slug", Name: "x", Status: provider.StatusEnabled}},
		{"empty name", provider.CreateInput{Slug: "openai", Name: "  ", Status: provider.StatusEnabled}},
		{"bad status", provider.CreateInput{Slug: "openai", Name: "x", Status: "paused"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeProviderStore{}
			_, err := provider.NewService(store).Create(context.Background(), tc.in)
			if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
				t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
			}
			if store.createCalls != 0 {
				t.Fatalf("store should not be called on invalid argument")
			}
		})
	}
}

func TestCreateConflictOnUniqueViolation(t *testing.T) {
	store := &fakeProviderStore{createErr: &pgconn.PgError{Code: "23505"}}
	_, err := provider.NewService(store).Create(context.Background(), provider.CreateInput{
		Slug: "openai", Name: "OpenAI", Status: provider.StatusEnabled,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminConflict {
		t.Fatalf("expected %q, got %q", failure.CodeAdminConflict, got)
	}
}

func TestCreateSuccessTrimsAndMaps(t *testing.T) {
	now := time.Now()
	store := &fakeProviderStore{createRow: sqlc.Provider{
		ID: 7, Slug: "openai", Name: "OpenAI", Status: provider.StatusEnabled,
		CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}}

	got, err := provider.NewService(store).Create(context.Background(), provider.CreateInput{
		Slug: "  openai  ", Name: "  OpenAI  ", Status: provider.StatusEnabled,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if store.createParam.Slug != "openai" || store.createParam.Name != "OpenAI" {
		t.Fatalf("expected trimmed params, got %+v", store.createParam)
	}
	if got.ID != 7 || got.Slug != "openai" || got.Status != provider.StatusEnabled {
		t.Fatalf("unexpected mapped provider: %+v", got)
	}
}

func TestGetNotFound(t *testing.T) {
	store := &fakeProviderStore{getErr: pgx.ErrNoRows}
	_, err := provider.NewService(store).Get(context.Background(), 5)
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

func TestUpdateNotFound(t *testing.T) {
	store := &fakeProviderStore{updateErr: pgx.ErrNoRows}
	_, err := provider.NewService(store).Update(context.Background(), provider.UpdateInput{
		ID: 5, Name: "OpenAI", Status: provider.StatusDisabled,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

func TestUpdateReturnsPendingAndCountsOnlyEffectiveEndpointTransitions(t *testing.T) {
	store := &fakeProviderStore{
		getRow: sqlc.Provider{ID: 7, Name: "OpenAI", Status: provider.StatusEnabled},
		endpoints: []sqlc.ProviderEndpoint{
			{ID: 11, ProviderID: 7, Status: provider.StatusEnabled, BaseUrlRevision: 1, StatusRevision: 3},
			{ID: 12, ProviderID: 7, Status: provider.StatusDisabled, BaseUrlRevision: 1, StatusRevision: 5},
			{ID: 13, ProviderID: 7, Status: provider.StatusArchived, BaseUrlRevision: 1, StatusRevision: 8},
		},
	}
	fencePublisher := &fakeProviderFencePublisher{result: runtimecontrol.PublishResult{
		State: runtimecontrol.PublishRuntimeSyncPending,
	}}
	svc := provider.NewService(store).WithStatusFencer(
		provider.NewStatusFencer(fencePublisher, fakeProviderFenceOps{}),
		nil,
	)

	got, err := svc.Update(context.Background(), provider.UpdateInput{
		ID: 7, Name: "OpenAI", Status: provider.StatusDisabled,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !got.RuntimeSyncPending {
		t.Fatal("expected runtime sync pending")
	}
	if got.AffectedEndpointCount != 1 {
		t.Fatalf("expected only the enabled endpoint to transition, got %d", got.AffectedEndpointCount)
	}
}

func TestDeleteRejectsInvalidID(t *testing.T) {
	store := &fakeProviderStore{}
	err := provider.NewService(store).Delete(context.Background(), 0)
	if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
	}
	if store.deleteCalls != 0 {
		t.Fatalf("store should not be called on invalid id")
	}
}

func TestDeleteSuccess(t *testing.T) {
	// 硬删闸门（D-4）：只允许删除已归档 provider，故 getRow 须为 archived。
	store := &fakeProviderStore{deleteAff: 1, getRow: sqlc.Provider{ID: 7, Status: "archived"}}
	if err := provider.NewService(store).Delete(context.Background(), 7); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if store.deleteID != 7 {
		t.Fatalf("expected delete id 7, got %d", store.deleteID)
	}
}

// 未归档的 provider 直接删除被拦截（先归档）。
func TestDeleteRejectsWhenNotArchived(t *testing.T) {
	store := &fakeProviderStore{deleteAff: 1, getRow: sqlc.Provider{ID: 7, Status: "enabled"}}
	err := provider.NewService(store).Delete(context.Background(), 7)
	if got := failure.CodeOf(err); got != failure.CodeAdminConflict {
		t.Fatalf("expected %q, got %q", failure.CodeAdminConflict, got)
	}
	if store.deleteCalls != 0 {
		t.Fatalf("store delete must not be called before archive")
	}
}

// 录错且无引用的 provider 可真删：受影响行 0 仅当目标不存在，返回 not_found。
func TestDeleteNotFoundWhenNoRows(t *testing.T) {
	store := &fakeProviderStore{deleteAff: 0, getRow: sqlc.Provider{ID: 7, Status: "archived"}}
	err := provider.NewService(store).Delete(context.Background(), 7)
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

// 已归档但仍被请求/账务历史引用时，DB 报外键冲突（23503），降级为 conflict。
func TestDeleteConflictOnForeignKeyViolation(t *testing.T) {
	store := &fakeProviderStore{deleteErr: &pgconn.PgError{Code: "23503"}, getRow: sqlc.Provider{ID: 7, Status: "archived"}}
	err := provider.NewService(store).Delete(context.Background(), 7)
	if got := failure.CodeOf(err); got != failure.CodeAdminConflict {
		t.Fatalf("expected %q, got %q", failure.CodeAdminConflict, got)
	}
}

func TestArchiveAndRestore(t *testing.T) {
	store := &fakeProviderStore{archiveAff: 1, restoreAff: 1}
	svc := provider.NewService(store)
	if _, err := svc.Archive(context.Background(), 7, nil); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if store.archiveID != 7 {
		t.Fatalf("expected archive id 7, got %d", store.archiveID)
	}
	if _, err := svc.Restore(context.Background(), 7); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if store.restoreID != 7 {
		t.Fatalf("expected restore id 7, got %d", store.restoreID)
	}
}

func TestArchiveRejectsEmptyingEnabledRoute(t *testing.T) {
	store := &fakeProviderStore{
		archiveAff:  1,
		emptyRoutes: []sqlc.ListEnabledRoutesEmptiedByProviderRow{{ID: 3, Name: "production"}},
	}
	_, err := provider.NewService(store).Archive(context.Background(), 7, nil)
	if failure.CodeOf(err) != failure.CodeAdminConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
	if store.archiveID != 0 {
		t.Fatal("archive mutation must not run when an enabled route would be emptied")
	}
}

func TestArchiveAtomicallyReplacesProviderChannels(t *testing.T) {
	replacementID := int64(11)
	store := &fakeProviderStore{
		getRow: sqlc.Provider{ID: 8, Status: "enabled"},
		getChannelRow: sqlc.Channel{
			ID: replacementID, ProviderID: 8, ProviderEndpointID: 3, Status: "enabled", CredentialValid: true,
			Credential: "sk-live",
		},
		archiveReplacementAff: 1,
	}
	if _, err := provider.NewService(store).Archive(context.Background(), 7, &replacementID); err != nil {
		t.Fatalf("replace and archive provider: %v", err)
	}
	if store.archiveReplacementParam.ID != 7 || store.archiveReplacementParam.ReplacementChannelID != replacementID {
		t.Fatalf("unexpected atomic archive params: %+v", store.archiveReplacementParam)
	}
	if store.archiveID != 0 {
		t.Fatal("legacy archive mutation must not run for replacement operation")
	}
}
