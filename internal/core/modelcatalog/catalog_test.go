package modelcatalog

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// fakeCatalogStore 是 catalog service 单测用的可用模型查询替身。
type fakeCatalogStore struct {
	rows          []sqlc.ListAvailableModelsRow
	protocolRows  []sqlc.ListModelProtocolsRow
	err           error
	protocolErr   error
	calls         int
	protocolCalls int
}

func (s *fakeCatalogStore) ListAvailableModels(_ context.Context) ([]sqlc.ListAvailableModelsRow, error) {
	s.calls++
	return s.rows, s.err
}

func (s *fakeCatalogStore) ListModelProtocols(_ context.Context) ([]sqlc.ListModelProtocolsRow, error) {
	s.protocolCalls++
	return s.protocolRows, s.protocolErr
}

func TestListAvailableModelsMapsCapabilities(t *testing.T) {
	store := &fakeCatalogStore{
		rows: []sqlc.ListAvailableModelsRow{
			{ModelID: "openai/gpt-4.1", OwnedBy: "openai", CapabilityKeys: []string{"text.input", "text.output", "tools.function"}},
			{ModelID: "deepseek/deepseek-chat", OwnedBy: "deepseek", CapabilityKeys: nil},
		},
	}

	models, err := NewService(store).ListAvailableModels(context.Background(), nil)
	if err != nil {
		t.Fatalf("list available models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if store.calls != 1 || store.protocolCalls != 1 {
		t.Fatalf("expected one call to each store method, got models=%d protocols=%d", store.calls, store.protocolCalls)
	}

	if models[0].ID != "openai/gpt-4.1" {
		t.Fatalf("model[0] id = %q", models[0].ID)
	}
	if !reflect.DeepEqual(models[0].Capabilities, []string{"text.input", "text.output", "tools.function"}) {
		t.Fatalf("model[0] capabilities = %v", models[0].Capabilities)
	}

	// 未声明能力的模型应映射为空切片（非 nil），保证 handler 渲染 [] 而非 null。
	if models[1].Capabilities == nil {
		t.Fatal("expected unprovisioned model capabilities to be empty slice, got nil")
	}
	if len(models[1].Capabilities) != 0 {
		t.Fatalf("expected unprovisioned model to have no capabilities, got %v", models[1].Capabilities)
	}
}

func TestListAvailableModelsCapabilityFilterAND(t *testing.T) {
	store := &fakeCatalogStore{
		rows: []sqlc.ListAvailableModelsRow{
			{ModelID: "has-both", OwnedBy: "x", CapabilityKeys: []string{"image.input", "tools.function", "text.output"}},
			{ModelID: "has-one", OwnedBy: "x", CapabilityKeys: []string{"image.input"}},
			{ModelID: "has-none", OwnedBy: "x", CapabilityKeys: []string{"text.output"}},
		},
	}

	models, err := NewService(store).ListAvailableModels(context.Background(), []string{"image.input", "tools.function"})
	if err != nil {
		t.Fatalf("list available models: %v", err)
	}

	if len(models) != 1 {
		t.Fatalf("expected only the model satisfying all required caps, got %d: %v", len(models), models)
	}
	if models[0].ID != "has-both" {
		t.Fatalf("expected has-both, got %q", models[0].ID)
	}
}

func TestListAvailableModelsEmptyFilterReturnsAll(t *testing.T) {
	store := &fakeCatalogStore{
		rows: []sqlc.ListAvailableModelsRow{
			{ModelID: "a", OwnedBy: "x", CapabilityKeys: []string{"text.output"}},
			{ModelID: "b", OwnedBy: "x", CapabilityKeys: nil},
		},
	}

	models, err := NewService(store).ListAvailableModels(context.Background(), []string{})
	if err != nil {
		t.Fatalf("list available models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected empty filter to return all models, got %d", len(models))
	}
}

func TestListAvailableModelsStoreError(t *testing.T) {
	store := &fakeCatalogStore{err: errors.New("db down")}

	_, err := NewService(store).ListAvailableModels(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error when store fails")
	}
}
