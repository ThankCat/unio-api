package channel_test

import (
	"context"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channel"
)

// archiveStore 复用 createStore 的接口实现，只改归档护栏关心的两个方法。
type archiveStore struct {
	createStore
	status          string
	enabledBindings int64
	archiveCalls    int
}

func (s *archiveStore) GetChannel(context.Context, int64) (sqlc.Channel, error) {
	return sqlc.Channel{ID: 1, Status: s.status}, nil
}

func (s *archiveStore) CountEnabledBindingsByChannel(context.Context, int64) (int64, error) {
	return s.enabledBindings, nil
}

func (s *archiveStore) ArchiveChannel(context.Context, int64) (int64, error) {
	s.archiveCalls++
	return 1, nil
}

func newArchiveService(status string) (*channel.Service, *archiveStore) {
	store := &archiveStore{status: status}
	return channel.NewService(store, createRegistry{}), store
}

// 启用中的渠道还在承接流量，归档等于让它悄无声息地退出。必须先停用。
func TestArchiveRejectsEnabledChannel(t *testing.T) {
	svc, store := newArchiveService(channel.StatusEnabled)

	err := svc.Archive(context.Background(), 1)

	if err == nil {
		t.Fatal("archiving an enabled channel must be rejected")
	}
	if got := failure.CodeOf(err); got != failure.CodeAdminArchiveRequiresDisabled {
		t.Fatalf("failure code = %q, want %q", got, failure.CodeAdminArchiveRequiresDisabled)
	}
	if store.archiveCalls != 0 {
		t.Fatalf("store must not be touched when the guard rejects, got %d calls", store.archiveCalls)
	}
}

func TestArchiveAcceptsDisabledChannel(t *testing.T) {
	svc, store := newArchiveService(channel.StatusDisabled)

	if err := svc.Archive(context.Background(), 1); err != nil {
		t.Fatalf("archiving a disabled channel must succeed, got %v", err)
	}
	if store.archiveCalls != 1 {
		t.Fatalf("ArchiveChannel calls = %d, want 1", store.archiveCalls)
	}
}

func TestArchiveKeepsNotFoundForAlreadyArchivedChannel(t *testing.T) {
	svc, _ := newArchiveService(channel.StatusArchived)

	err := svc.Archive(context.Background(), 1)

	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("failure code = %q, want %q", got, failure.CodeAdminNotFound)
	}
}

// 停用状态下仍有启用绑定时，要报的是「先解绑」而不是「先停用」——两条引导指向不同的操作。
func TestDisabledChannelWithEnabledBindingsStillReportsBindingConflict(t *testing.T) {
	svc, store := newArchiveService(channel.StatusDisabled)
	store.enabledBindings = 2

	err := svc.Archive(context.Background(), 1)

	if got := failure.CodeOf(err); got != failure.CodeAdminConflict {
		t.Fatalf("failure code = %q, want %q", got, failure.CodeAdminConflict)
	}
	if store.archiveCalls != 0 {
		t.Fatalf("store must not be touched when bindings block the archive, got %d calls", store.archiveCalls)
	}
}
