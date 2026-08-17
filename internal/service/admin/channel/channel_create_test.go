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
	channel     sqlc.Channel
	createParam sqlc.CreateChannelParams
	createCalls int
	updateParam sqlc.UpdateChannelParams
	updateCalls int
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
	return s.channel, nil
}

func (s *createStore) CreateChannel(_ context.Context, arg sqlc.CreateChannelParams) (sqlc.Channel, error) {
	s.createParam = arg
	s.createCalls++
	return sqlc.Channel{
		ID:                 7,
		ProviderID:         arg.ProviderID,
		Name:               arg.Name,
		Protocol:           arg.Protocol,
		AdapterKey:         arg.AdapterKey,
		Credential:         arg.Credential,
		CapacityRevision:   1,
		Status:             arg.Status,
		Priority:           arg.Priority,
		SupportsOpenaiFast: arg.SupportsOpenaiFast,
	}, nil
}

func (s *createStore) UpdateChannel(_ context.Context, arg sqlc.UpdateChannelParams) (sqlc.Channel, error) {
	s.updateParam = arg
	s.updateCalls++
	updated := s.channel
	updated.Name = arg.Name
	updated.Status = arg.Status
	updated.Priority = arg.Priority
	updated.SupportsOpenaiFast = arg.SupportsOpenaiFast
	return updated, nil
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

func TestCreateOpenAIFastCapability(t *testing.T) {
	store := &createStore{provider: sqlc.Provider{
		ID: 1, Name: "Provider", Origin: "https://example.test", Status: channel.StatusEnabled,
	}}
	svc := channel.NewService(store, createRegistry{})

	created, err := svc.Create(context.Background(), channel.CreateInput{
		ProviderID: 1, Name: "fast", Protocol: channel.ProtocolOpenAI,
		AdapterKey: channel.ProtocolOpenAI, SupportsOpenAIFast: true,
		Credential: "test-credential", Status: channel.StatusDisabled,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !store.createParam.SupportsOpenaiFast || !created.SupportsOpenAIFast {
		t.Fatalf("supports_openai_fast stored/returned = %v/%v, want true/true", store.createParam.SupportsOpenaiFast, created.SupportsOpenAIFast)
	}
}

func TestCreateRejectsOpenAIFastOnAnthropicChannel(t *testing.T) {
	store := &createStore{provider: sqlc.Provider{
		ID: 1, Name: "Provider", Origin: "https://example.test", Status: channel.StatusEnabled,
	}}
	svc := channel.NewService(store, createRegistry{})

	_, err := svc.Create(context.Background(), channel.CreateInput{
		ProviderID: 1, Name: "anthropic-fast", Protocol: channel.ProtocolAnthropic,
		AdapterKey: channel.ProtocolAnthropic, SupportsOpenAIFast: true,
		Credential: "test-credential", Status: channel.StatusDisabled,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
		t.Fatalf("error code = %q, want %q (err=%v)", got, failure.CodeAdminInvalidArgument, err)
	}
	if store.createCalls != 0 {
		t.Fatalf("CreateChannel calls = %d, want 0", store.createCalls)
	}
}

func TestUpdateOpenAIFastCapabilityPatchSemantics(t *testing.T) {
	tests := []struct {
		name        string
		provided    *bool
		wantEnabled bool
	}{
		{name: "omitted preserves enabled", wantEnabled: true},
		{name: "explicit false disables", provided: boolPointer(false), wantEnabled: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &createStore{
				provider: sqlc.Provider{ID: 1, Name: "Provider", Status: channel.StatusEnabled},
				channel: sqlc.Channel{
					ID: 7, ProviderID: 1, Name: "primary", Protocol: channel.ProtocolOpenAI,
					AdapterKey: channel.ProtocolOpenAI, Status: channel.StatusDisabled,
					CapacityRevision: 1, SupportsOpenaiFast: true,
				},
			}
			svc := channel.NewService(store, createRegistry{})

			updated, err := svc.Update(context.Background(), channel.UpdateInput{
				ID: 7, ProviderID: 1, Name: "primary", Status: channel.StatusDisabled,
				SupportsOpenAIFast: tt.provided,
			})
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if store.updateCalls != 1 {
				t.Fatalf("UpdateChannel calls = %d, want 1", store.updateCalls)
			}
			if store.updateParam.SupportsOpenaiFast != tt.wantEnabled || updated.SupportsOpenAIFast != tt.wantEnabled {
				t.Fatalf("supports_openai_fast stored/returned = %v/%v, want %v", store.updateParam.SupportsOpenaiFast, updated.SupportsOpenAIFast, tt.wantEnabled)
			}
		})
	}
}

func TestUpdateRejectsOpenAIFastOnAnthropicChannel(t *testing.T) {
	store := &createStore{
		provider: sqlc.Provider{ID: 1, Name: "Provider", Status: channel.StatusEnabled},
		channel: sqlc.Channel{
			ID: 7, ProviderID: 1, Name: "anthropic", Protocol: channel.ProtocolAnthropic,
			AdapterKey: channel.ProtocolAnthropic, Status: channel.StatusDisabled, CapacityRevision: 1,
		},
	}
	svc := channel.NewService(store, createRegistry{})

	_, err := svc.Update(context.Background(), channel.UpdateInput{
		ID: 7, ProviderID: 1, Name: "anthropic", Status: channel.StatusDisabled,
		SupportsOpenAIFast: boolPointer(true),
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
		t.Fatalf("error code = %q, want %q (err=%v)", got, failure.CodeAdminInvalidArgument, err)
	}
	if store.updateCalls != 0 {
		t.Fatalf("UpdateChannel calls = %d, want 0", store.updateCalls)
	}
}

func boolPointer(value bool) *bool {
	return &value
}
