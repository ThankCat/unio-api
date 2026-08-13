package channel_test

import (
	"context"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channel"
)

type createStore struct {
	provider    sqlc.Provider
	createParam sqlc.CreateChannelParams
	createCalls int
}

func (s *createStore) GetProvider(context.Context, int64) (sqlc.Provider, error) {
	return s.provider, nil
}

func (s *createStore) ListChannelsPage(context.Context, sqlc.ListChannelsPageParams) ([]sqlc.ListChannelsPageRow, error) {
	return nil, nil
}

func (s *createStore) CountChannels(context.Context, sqlc.CountChannelsParams) (int64, error) {
	return 0, nil
}

func (s *createStore) GetChannel(context.Context, int64) (sqlc.Channel, error) {
	return sqlc.Channel{}, nil
}

func (s *createStore) CreateChannel(_ context.Context, arg sqlc.CreateChannelParams) (sqlc.Channel, error) {
	s.createParam = arg
	s.createCalls++
	return sqlc.Channel{
		ID:               7,
		ProviderID:       arg.ProviderID,
		Name:             arg.Name,
		Protocol:         arg.Protocol,
		AdapterKey:       arg.AdapterKey,
		Credential:       arg.Credential,
		CapacityRevision: 1,
		Status:           arg.Status,
		Priority:         arg.Priority,
	}, nil
}

func (s *createStore) UpdateChannel(context.Context, sqlc.UpdateChannelParams) (sqlc.Channel, error) {
	return sqlc.Channel{}, nil
}

func (s *createStore) DeleteChannelCascade(context.Context, int64) (int64, error) {
	return 0, nil
}

func (s *createStore) ArchiveChannel(context.Context, int64) (int64, error) {
	return 0, nil
}

func (s *createStore) CountEnabledBindingsByChannel(context.Context, int64) (int64, error) {
	return 0, nil
}

func (s *createStore) ListRoutesReferencingChannel(context.Context, int64) ([]sqlc.ListRoutesReferencingChannelRow, error) {
	return nil, nil
}

func (s *createStore) RestoreChannel(context.Context, int64) (int64, error) {
	return 0, nil
}

type createRegistry struct{}

func (createRegistry) HasAny(protocol, adapterKey string) bool {
	return protocol == channel.ProtocolOpenAI && adapterKey == channel.ProtocolOpenAI
}

func (createRegistry) AdapterKeys(string) []string { return nil }

func TestCreatePreservesRequestedStatus(t *testing.T) {
	for _, requested := range []string{channel.StatusEnabled, channel.StatusDisabled} {
		t.Run(requested, func(t *testing.T) {
			store := &createStore{provider: sqlc.Provider{
				ID: 1, Name: "Provider", Origin: "https://example.test", Status: channel.StatusEnabled,
			}}
			svc := channel.NewService(store, createRegistry{})

			created, err := svc.Create(context.Background(), channel.CreateInput{
				ProviderID: 1,
				Name:       "primary",
				Protocol:   channel.ProtocolOpenAI,
				AdapterKey: channel.ProtocolOpenAI,
				Credential: "test-credential",
				Status:     requested,
				Priority:   0,
			})
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if store.createCalls != 1 {
				t.Fatalf("CreateChannel calls = %d, want 1", store.createCalls)
			}
			if store.createParam.Status != requested {
				t.Fatalf("stored status = %q, want %q", store.createParam.Status, requested)
			}
			if created.Status != requested {
				t.Fatalf("created status = %q, want %q", created.Status, requested)
			}
		})
	}
}

func TestCreateEnabledRequiresEnabledProvider(t *testing.T) {
	store := &createStore{provider: sqlc.Provider{
		ID: 1, Name: "Provider", Origin: "https://example.test", Status: channel.StatusDisabled,
	}}
	svc := channel.NewService(store, createRegistry{})

	_, err := svc.Create(context.Background(), channel.CreateInput{
		ProviderID: 1,
		Name:       "primary",
		Protocol:   channel.ProtocolOpenAI,
		AdapterKey: channel.ProtocolOpenAI,
		Credential: "test-credential",
		Status:     channel.StatusEnabled,
		Priority:   0,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminConflict {
		t.Fatalf("error code = %q, want %q (err=%v)", got, failure.CodeAdminConflict, err)
	}
	if store.createCalls != 0 {
		t.Fatalf("CreateChannel calls = %d, want 0", store.createCalls)
	}
}
