package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

type fakeProviderAdapterStore struct {
	rows []sqlc.ListEnabledChannelAdaptersRow
	err  error
}

func (s *fakeProviderAdapterStore) ListEnabledChannelAdapters(ctx context.Context) ([]sqlc.ListEnabledChannelAdaptersRow, error) {
	return s.rows, s.err
}

type fakeAdapterCapabilityRegistry struct {
	capabilities map[adapterCapabilityKey]bool
}

type adapterCapabilityKey struct {
	protocol   string
	adapterKey string
	capability lifecycle.AdapterCapability
}

func (r *fakeAdapterCapabilityRegistry) HasAny(protocol string, adapterKey string) bool {
	for _, capability := range []lifecycle.AdapterCapability{
		lifecycle.AdapterCapabilityNonStream,
		lifecycle.AdapterCapabilityStream,
		lifecycle.AdapterCapabilityInputTokenizer,
	} {
		if r.capabilities[adapterCapabilityKey{protocol: protocol, adapterKey: adapterKey, capability: capability}] {
			return true
		}
	}
	return false
}

func registeredAdapterCapabilities(protocol string, adapterKey string) map[adapterCapabilityKey]bool {
	return map[adapterCapabilityKey]bool{
		{protocol: protocol, adapterKey: adapterKey, capability: lifecycle.AdapterCapabilityNonStream}:      true,
		{protocol: protocol, adapterKey: adapterKey, capability: lifecycle.AdapterCapabilityStream}:         true,
		{protocol: protocol, adapterKey: adapterKey, capability: lifecycle.AdapterCapabilityInputTokenizer}: true,
	}
}

func TestProviderAdapterPreflightAcceptsRegisteredDualProtocolBindings(t *testing.T) {
	capabilities := registeredAdapterCapabilities("openai", "deepseek")
	for key, value := range registeredAdapterCapabilities("anthropic", "deepseek") {
		capabilities[key] = value
	}

	preflight := NewProviderAdapterPreflight(
		&fakeProviderAdapterStore{
			rows: []sqlc.ListEnabledChannelAdaptersRow{
				{ChannelID: 1, Protocol: "openai", AdapterKey: "deepseek", ProviderSlug: "deepseek"},
				{ChannelID: 2, Protocol: "anthropic", AdapterKey: "deepseek", ProviderSlug: "deepseek"},
			},
		},
		&fakeAdapterCapabilityRegistry{
			capabilities: capabilities,
		},
	)

	if err := preflight.ValidateEnabledChannelBindings(context.Background()); err != nil {
		t.Fatalf("ValidateEnabledChannelBindings returned error: %v", err)
	}
}

func TestProviderAdapterPreflightAcceptsNoEnabledProviders(t *testing.T) {
	preflight := NewProviderAdapterPreflight(
		&fakeProviderAdapterStore{},
		&fakeAdapterCapabilityRegistry{},
	)

	if err := preflight.ValidateEnabledChannelBindings(context.Background()); err != nil {
		t.Fatalf("ValidateEnabledChannelBindings returned error: %v", err)
	}
}

func TestProviderAdapterPreflightWrapsStoreError(t *testing.T) {
	storeErr := errors.New("database unavailable")
	preflight := NewProviderAdapterPreflight(
		&fakeProviderAdapterStore{err: storeErr},
		&fakeAdapterCapabilityRegistry{},
	)

	err := preflight.ValidateEnabledChannelBindings(context.Background())
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected store error cause, got %v", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeBootstrapStoreFailed {
		t.Fatalf("expected code %q, got %q", failure.CodeBootstrapStoreFailed, got)
	}
	if got := failure.CategoryOf(err); got != failure.CategoryBootstrap {
		t.Fatalf("expected category %q, got %q", failure.CategoryBootstrap, got)
	}
}

func TestProviderAdapterPreflightAcceptsPartialCapabilityBinding(t *testing.T) {
	preflight := NewProviderAdapterPreflight(
		&fakeProviderAdapterStore{
			rows: []sqlc.ListEnabledChannelAdaptersRow{
				{ChannelID: 42, Protocol: "openai", AdapterKey: "openai", ProviderSlug: "openai"},
			},
		},
		&fakeAdapterCapabilityRegistry{
			capabilities: map[adapterCapabilityKey]bool{
				{protocol: "openai", adapterKey: "openai", capability: lifecycle.AdapterCapabilityNonStream}: true,
			},
		},
	)

	if err := preflight.ValidateEnabledChannelBindings(context.Background()); err != nil {
		t.Fatalf("ValidateEnabledChannelBindings returned error: %v", err)
	}
}

func TestProviderAdapterPreflightRejectsUnknownBinding(t *testing.T) {
	preflight := NewProviderAdapterPreflight(
		&fakeProviderAdapterStore{
			rows: []sqlc.ListEnabledChannelAdaptersRow{
				{ChannelID: 99, Protocol: "openai", AdapterKey: "deepseek", ProviderSlug: "deepseek"},
			},
		},
		&fakeAdapterCapabilityRegistry{},
	)

	err := preflight.ValidateEnabledChannelBindings(context.Background())
	assertCapabilityMissingError(t, err, map[string]any{
		"channel_id":    int64(99),
		"provider_slug": "deepseek",
		"protocol":      "openai",
		"adapter_key":   "deepseek",
		"capability":    "binding",
	})
}

func TestProviderAdapterPreflightDoesNotResolveSameKeyAcrossProtocols(t *testing.T) {
	preflight := NewProviderAdapterPreflight(
		&fakeProviderAdapterStore{
			rows: []sqlc.ListEnabledChannelAdaptersRow{
				{ChannelID: 101, Protocol: "anthropic", AdapterKey: "deepseek", ProviderSlug: "deepseek"},
			},
		},
		&fakeAdapterCapabilityRegistry{
			capabilities: registeredAdapterCapabilities("openai", "deepseek"),
		},
	)

	err := preflight.ValidateEnabledChannelBindings(context.Background())
	assertCapabilityMissingError(t, err, map[string]any{
		"channel_id":    int64(101),
		"provider_slug": "deepseek",
		"protocol":      "anthropic",
		"adapter_key":   "deepseek",
		"capability":    "binding",
	})
}

func assertCapabilityMissingError(t *testing.T, err error, wantFields map[string]any) {
	t.Helper()

	if !errors.Is(err, ErrProviderAdapterCapabilityMissing) {
		t.Fatalf("expected ErrProviderAdapterCapabilityMissing, got %v", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeBootstrapProviderAdapterCapabilityMissing {
		t.Fatalf("expected code %q, got %q", failure.CodeBootstrapProviderAdapterCapabilityMissing, got)
	}
	if got := failure.CategoryOf(err); got != failure.CategoryBootstrap {
		t.Fatalf("expected category %q, got %q", failure.CategoryBootstrap, got)
	}

	gotFields := map[string]any{}
	for _, field := range failure.FieldsOf(err) {
		gotFields[field.Key] = field.Value
	}

	for key, want := range wantFields {
		if gotFields[key] != want {
			t.Fatalf("expected field %s=%v, got %v in %#v", key, want, gotFields[key], gotFields)
		}
	}
}
