package model_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/model"
)

type fakeModelStore struct {
	models      []sqlc.ListModelsPageRow
	lookupRow   sqlc.Model
	lookupErr   error
	createRow   sqlc.Model
	createErr   error
	createParam sqlc.CreateModelParams
	createCalls int
	updateRow   sqlc.Model
	updateErr   error
	deleteAff   int64
	deleteErr   error
	deleteID    int64
	deleteCalls int
}

func (s *fakeModelStore) ListModelsPage(context.Context, sqlc.ListModelsPageParams) ([]sqlc.ListModelsPageRow, error) {
	return s.models, nil
}

func (s *fakeModelStore) CountModels(context.Context, sqlc.CountModelsParams) (int64, error) {
	return int64(len(s.models)), nil
}

func (s *fakeModelStore) LookupModelByID(context.Context, int64) (sqlc.Model, error) {
	return s.lookupRow, s.lookupErr
}

func (s *fakeModelStore) GetModelCatalogState(context.Context, int64) (sqlc.GetModelCatalogStateRow, error) {
	return sqlc.GetModelCatalogStateRow{}, pgx.ErrNoRows
}

func (s *fakeModelStore) CreateModel(_ context.Context, arg sqlc.CreateModelParams) (sqlc.Model, error) {
	s.createParam = arg
	s.createCalls++
	return s.createRow, s.createErr
}

func (s *fakeModelStore) UpdateModel(context.Context, sqlc.UpdateModelParams) (sqlc.Model, error) {
	return s.updateRow, s.updateErr
}

func (s *fakeModelStore) DeleteModelCascade(_ context.Context, id int64) (int64, error) {
	s.deleteID = id
	s.deleteCalls++
	return s.deleteAff, s.deleteErr
}

func TestCreateRejectsInvalidArguments(t *testing.T) {
	cases := []struct {
		name string
		in   model.CreateInput
	}{
		{"bad model_id", model.CreateInput{ModelID: "bad id", DisplayName: "x", OwnedBy: "y", Status: model.StatusEnabled}},
		{"empty display_name", model.CreateInput{ModelID: "deepseek-chat", DisplayName: "  ", OwnedBy: "y", Status: model.StatusEnabled}},
		{"empty owned_by", model.CreateInput{ModelID: "deepseek-chat", DisplayName: "x", OwnedBy: " ", Status: model.StatusEnabled}},
		{"bad status", model.CreateInput{ModelID: "deepseek-chat", DisplayName: "x", OwnedBy: "y", Status: "paused"}},
		{"bad max_output_tokens", model.CreateInput{ModelID: "deepseek-chat", DisplayName: "x", OwnedBy: "y", Status: model.StatusEnabled, Metadata: model.Metadata{MaxOutputTokens: ptr(int64(0))}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeModelStore{}
			_, err := model.NewService(store).Create(context.Background(), tc.in)
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
	store := &fakeModelStore{createErr: &pgconn.PgError{Code: "23505"}}
	_, err := model.NewService(store).Create(context.Background(), model.CreateInput{
		ModelID: "deepseek-chat", DisplayName: "DeepSeek Chat", OwnedBy: "deepseek", Status: model.StatusEnabled,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminConflict {
		t.Fatalf("expected %q, got %q", failure.CodeAdminConflict, got)
	}
}

func TestCreateSuccessTrimsAndMaps(t *testing.T) {
	now := time.Now()
	store := &fakeModelStore{createRow: sqlc.Model{
		ID: 9, ModelID: "deepseek-chat", DisplayName: "DeepSeek Chat", OwnedBy: "deepseek",
		Status: model.StatusEnabled, Source: "manual",
		CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}}

	got, err := model.NewService(store).Create(context.Background(), model.CreateInput{
		ModelID: "  deepseek-chat  ", DisplayName: "  DeepSeek Chat  ", OwnedBy: "  deepseek  ",
		Status: model.StatusEnabled, Metadata: model.Metadata{MaxOutputTokens: ptr(int64(8192))},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if store.createParam.ModelID != "deepseek-chat" || store.createParam.OwnedBy != "deepseek" {
		t.Fatalf("expected trimmed params, got %+v", store.createParam)
	}
	if !store.createParam.MaxOutputTokens.Valid || store.createParam.MaxOutputTokens.Int64 != 8192 {
		t.Fatalf("expected max_output_tokens passed through, got %+v", store.createParam.MaxOutputTokens)
	}
	if got.ID != 9 || got.ModelID != "deepseek-chat" || got.Source != "manual" {
		t.Fatalf("unexpected mapped model: %+v", got)
	}
}

func TestGetNotFound(t *testing.T) {
	store := &fakeModelStore{lookupErr: pgx.ErrNoRows}
	_, err := model.NewService(store).Get(context.Background(), 5)
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

func TestUpdateNotFound(t *testing.T) {
	store := &fakeModelStore{updateErr: pgx.ErrNoRows}
	_, err := model.NewService(store).Update(context.Background(), model.UpdateInput{
		ID: 5, DisplayName: "DeepSeek Chat", OwnedBy: "deepseek", Status: model.StatusDisabled,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

func TestDeleteRejectsInvalidID(t *testing.T) {
	store := &fakeModelStore{}
	err := model.NewService(store).Delete(context.Background(), 0)
	if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
	}
	if store.deleteCalls != 0 {
		t.Fatalf("store should not be called on invalid id")
	}
}

// 录错且无引用的模型可真删；级联清理由 DB CTE 完成，受影响行 0 仅当 model 不存在。
func TestDeleteSuccess(t *testing.T) {
	store := &fakeModelStore{deleteAff: 1}
	if err := model.NewService(store).Delete(context.Background(), 9); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if store.deleteID != 9 {
		t.Fatalf("expected delete id 9, got %d", store.deleteID)
	}
}

func TestDeleteNotFoundWhenNoRows(t *testing.T) {
	store := &fakeModelStore{deleteAff: 0}
	err := model.NewService(store).Delete(context.Background(), 9)
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

// 已被请求/账务历史引用时，DB 报外键冲突（23503），降级为 conflict 提示改用停用。
func TestDeleteConflictOnForeignKeyViolation(t *testing.T) {
	store := &fakeModelStore{deleteErr: &pgconn.PgError{Code: "23503"}}
	err := model.NewService(store).Delete(context.Background(), 9)
	if got := failure.CodeOf(err); got != failure.CodeAdminConflict {
		t.Fatalf("expected %q, got %q", failure.CodeAdminConflict, got)
	}
}

func ptr[T any](v T) *T { return &v }
