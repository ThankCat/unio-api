package modelcatalog

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// fakeCatalogStore 是 catalog service 单测用的可用模型查询替身。
type fakeCatalogStore struct {
	rows []sqlc.ListAvailableModelsForProjectRow
	err  error
}

func (s *fakeCatalogStore) ListAvailableModelsForProject(_ context.Context, _ int64) ([]sqlc.ListAvailableModelsForProjectRow, error) {
	return s.rows, s.err
}

func TestListAvailableModelsMapsCapabilities(t *testing.T) {
	store := &fakeCatalogStore{
		rows: []sqlc.ListAvailableModelsForProjectRow{
			{ModelID: "openai/gpt-4.1", OwnedBy: "openai", CapabilityKeys: []string{"text.input", "text.output", "tools.function"}},
			{ModelID: "deepseek/deepseek-chat", OwnedBy: "deepseek", CapabilityKeys: nil},
		},
	}

	models, err := NewService(store).ListAvailableModels(context.Background(), 42, nil)
	if err != nil {
		t.Fatalf("list available models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
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
		rows: []sqlc.ListAvailableModelsForProjectRow{
			{ModelID: "has-both", OwnedBy: "x", CapabilityKeys: []string{"image.input", "tools.function", "text.output"}},
			{ModelID: "has-one", OwnedBy: "x", CapabilityKeys: []string{"image.input"}},
			{ModelID: "has-none", OwnedBy: "x", CapabilityKeys: []string{"text.output"}},
		},
	}

	models, err := NewService(store).ListAvailableModels(context.Background(), 42, []string{"image.input", "tools.function"})
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
		rows: []sqlc.ListAvailableModelsForProjectRow{
			{ModelID: "a", OwnedBy: "x", CapabilityKeys: []string{"text.output"}},
			{ModelID: "b", OwnedBy: "x", CapabilityKeys: nil},
		},
	}

	models, err := NewService(store).ListAvailableModels(context.Background(), 42, []string{})
	if err != nil {
		t.Fatalf("list available models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected empty filter to return all models, got %d", len(models))
	}
}

func TestListAvailableModelsStoreError(t *testing.T) {
	store := &fakeCatalogStore{err: errors.New("db down")}

	_, err := NewService(store).ListAvailableModels(context.Background(), 42, nil)
	if err == nil {
		t.Fatal("expected error when store fails")
	}
}
