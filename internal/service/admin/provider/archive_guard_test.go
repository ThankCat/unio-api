package provider_test

import (
	"context"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/provider"
)

// archiveStore 只服务归档护栏用例：记录 ArchiveProvider 是否真的被调用。
type archiveStore struct {
	current      sqlc.Provider
	archiveCalls int
}

func (s *archiveStore) ListProvidersPage(context.Context, sqlc.ListProvidersPageParams) ([]sqlc.Provider, error) {
	return nil, nil
}

func (s *archiveStore) CountProviders(context.Context, sqlc.CountProvidersParams) (int64, error) {
	return 0, nil
}

func (s *archiveStore) GetProvider(context.Context, int64) (sqlc.Provider, error) {
	return s.current, nil
}

func (s *archiveStore) CreateProvider(context.Context, sqlc.CreateProviderParams) (sqlc.Provider, error) {
	return sqlc.Provider{}, nil
}

func (s *archiveStore) UpdateProvider(context.Context, sqlc.UpdateProviderParams) (sqlc.Provider, error) {
	return sqlc.Provider{}, nil
}

func (s *archiveStore) DeleteProvider(context.Context, int64) (int64, error) {
	return 0, nil
}

func (s *archiveStore) ArchiveProvider(context.Context, int64) (int64, error) {
	s.archiveCalls++
	return 1, nil
}

func (s *archiveStore) RestoreProvider(context.Context, int64) (int64, error) {
	return 0, nil
}

func (s *archiveStore) CountEnabledChannelsByProvider(context.Context, int64) (int64, error) {
	return 0, nil
}

func (s *archiveStore) CountNonArchivedChannelsByProvider(context.Context, int64) (int64, error) {
	return 0, nil
}

func newArchiveService(status string) (*provider.Service, *archiveStore) {
	store := &archiveStore{current: sqlc.Provider{ID: 1, Status: status}}
	return provider.NewService(store), store
}

// 归档是「这个服务商以后不用了」的终态动作。启用中就归档等于让在跑的流量突然失去落点，
// 必须先显式停用，让运维看到流量归零之后再收尾。
func TestArchiveRejectsEnabledProvider(t *testing.T) {
	svc, store := newArchiveService(provider.StatusEnabled)

	_, err := svc.Archive(context.Background(), 1)

	if err == nil {
		t.Fatal("archiving an enabled provider must be rejected")
	}
	if got := failure.CodeOf(err); got != failure.CodeAdminArchiveRequiresDisabled {
		t.Fatalf("failure code = %q, want %q", got, failure.CodeAdminArchiveRequiresDisabled)
	}
	if store.archiveCalls != 0 {
		t.Fatalf("store must not be touched when the guard rejects, got %d calls", store.archiveCalls)
	}
}

func TestArchiveAcceptsDisabledProvider(t *testing.T) {
	svc, store := newArchiveService(provider.StatusDisabled)

	if _, err := svc.Archive(context.Background(), 1); err != nil {
		t.Fatalf("archiving a disabled provider must succeed, got %v", err)
	}
	if store.archiveCalls != 1 {
		t.Fatalf("ArchiveProvider calls = %d, want 1", store.archiveCalls)
	}
}

// 已归档仍旧走 not found，不能被新护栏改写成「请先停用」——那会让重复归档的提示驴唇不对马嘴。
func TestArchiveKeepsNotFoundForAlreadyArchived(t *testing.T) {
	svc, _ := newArchiveService(provider.StatusArchived)

	_, err := svc.Archive(context.Background(), 1)

	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("failure code = %q, want %q", got, failure.CodeAdminNotFound)
	}
}
