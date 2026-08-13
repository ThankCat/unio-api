package channelmodel_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelmodel"
)

type fakeStore struct {
	channel     sqlc.Channel
	channelErr  error
	model       sqlc.Model
	modelErr    error
	listRows    []sqlc.ListChannelModelsByChannelRow
	createRow   sqlc.ChannelModel
	createErr   error
	createParam sqlc.CreateChannelModelParams
	createCalls int
	updateRow   sqlc.ChannelModel
	updateErr   error
	getRow      sqlc.ChannelModel
	getErr      error
	evidenceErr error
	deleteRows  int64
	deleteErr   error
	deleteCalls int
}

func (s *fakeStore) GetChannel(context.Context, int64) (sqlc.Channel, error) {
	return s.channel, s.channelErr
}

func (s *fakeStore) LookupModelByID(context.Context, int64) (sqlc.Model, error) {
	return s.model, s.modelErr
}

func (s *fakeStore) ListChannelModelsByChannel(context.Context, int64) ([]sqlc.ListChannelModelsByChannelRow, error) {
	return s.listRows, nil
}

func (s *fakeStore) GetChannelModel(_ context.Context, arg sqlc.GetChannelModelParams) (sqlc.ChannelModel, error) {
	if s.getErr != nil {
		return sqlc.ChannelModel{}, s.getErr
	}
	if s.getRow.ModelID != 0 {
		return s.getRow, nil
	}
	return sqlc.ChannelModel{ChannelID: arg.ChannelID, ModelID: arg.ModelID, UpstreamModel: "gpt-4", Status: channelmodel.StatusDisabled}, nil
}

func (s *fakeStore) GetCurrentChannelModelVerificationEvidence(context.Context, sqlc.GetCurrentChannelModelVerificationEvidenceParams) (sqlc.ChannelModelVerificationItem, error) {
	return sqlc.ChannelModelVerificationItem{}, s.evidenceErr
}

func (s *fakeStore) CreateChannelModel(_ context.Context, arg sqlc.CreateChannelModelParams) (sqlc.ChannelModel, error) {
	s.createParam = arg
	s.createCalls++
	return s.createRow, s.createErr
}

func (s *fakeStore) UpdateChannelModel(context.Context, sqlc.UpdateChannelModelParams) (sqlc.ChannelModel, error) {
	return s.updateRow, s.updateErr
}

func (s *fakeStore) DeleteChannelModel(context.Context, sqlc.DeleteChannelModelParams) (int64, error) {
	s.deleteCalls++
	return s.deleteRows, s.deleteErr
}

func TestCreateRejectsInvalidArguments(t *testing.T) {
	cases := []struct {
		name string
		in   channelmodel.CreateInput
	}{
		{"missing channel", channelmodel.CreateInput{ModelID: 1, UpstreamModel: "gpt-4", Status: channelmodel.StatusEnabled}},
		{"missing model", channelmodel.CreateInput{ChannelID: 1, UpstreamModel: "gpt-4", Status: channelmodel.StatusEnabled}},
		{"empty upstream_model", channelmodel.CreateInput{ChannelID: 1, ModelID: 1, UpstreamModel: "  ", Status: channelmodel.StatusEnabled}},
		{"bad status", channelmodel.CreateInput{ChannelID: 1, ModelID: 1, UpstreamModel: "gpt-4", Status: "paused"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			_, err := channelmodel.NewService(store, nil, nil).Create(context.Background(), tc.in)
			if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
				t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
			}
			if store.createCalls != 0 {
				t.Fatalf("store should not be called on invalid argument")
			}
		})
	}
}

func TestCreateChannelNotFound(t *testing.T) {
	store := &fakeStore{channelErr: pgx.ErrNoRows}
	_, err := channelmodel.NewService(store, nil, nil).Create(context.Background(), channelmodel.CreateInput{
		ChannelID: 1, ModelID: 1, UpstreamModel: "gpt-4", Status: channelmodel.StatusEnabled,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

func TestCreateModelNotFound(t *testing.T) {
	store := &fakeStore{modelErr: pgx.ErrNoRows}
	_, err := channelmodel.NewService(store, nil, nil).Create(context.Background(), channelmodel.CreateInput{
		ChannelID: 1, ModelID: 99, UpstreamModel: "gpt-4", Status: channelmodel.StatusEnabled,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
	}
}

func TestCreateConflictOnUniqueViolation(t *testing.T) {
	store := &fakeStore{createErr: &pgconn.PgError{Code: "23505"}}
	_, err := channelmodel.NewService(store, nil, nil).Create(context.Background(), channelmodel.CreateInput{
		ChannelID: 1, ModelID: 1, UpstreamModel: "gpt-4", Status: channelmodel.StatusEnabled,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminConflict {
		t.Fatalf("expected %q, got %q", failure.CodeAdminConflict, got)
	}
}

func TestCreateSuccessTrimsAndMaps(t *testing.T) {
	store := &fakeStore{createRow: sqlc.ChannelModel{
		ID: 7, ChannelID: 1, ModelID: 2, UpstreamModel: "gpt-4o", Status: channelmodel.StatusEnabled,
		CreatedAt: pgtype.Timestamptz{Valid: true},
		UpdatedAt: pgtype.Timestamptz{Valid: true},
	}}

	got, err := channelmodel.NewService(store, nil, nil).Create(context.Background(), channelmodel.CreateInput{
		ChannelID: 1, ModelID: 2, UpstreamModel: "  gpt-4o  ", Status: channelmodel.StatusEnabled,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if store.createParam.UpstreamModel != "gpt-4o" {
		t.Fatalf("expected trimmed upstream_model, got %q", store.createParam.UpstreamModel)
	}
	if store.createParam.Status != channelmodel.StatusDisabled {
		t.Fatalf("new binding must be disabled, got %q", store.createParam.Status)
	}
	if got.ID != 7 || got.UpstreamModel != "gpt-4o" {
		t.Fatalf("unexpected mapped binding: %+v", got)
	}
}

func TestUpdateEnableRequiresCurrentVerification(t *testing.T) {
	store := &fakeStore{getRow: sqlc.ChannelModel{
		ChannelID: 1, ModelID: 1, UpstreamModel: "gpt-4", Status: channelmodel.StatusDisabled,
	}}
	_, err := channelmodel.NewService(store, nil, nil).Update(context.Background(), channelmodel.UpdateInput{
		ChannelID: 1, ModelID: 1, UpstreamModel: "gpt-4", Status: channelmodel.StatusEnabled,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminConflict {
		t.Fatalf("expected verification conflict, got %q", got)
	}
}

func TestUpdateNotFound(t *testing.T) {
	store := &fakeStore{updateErr: pgx.ErrNoRows}
	_, err := channelmodel.NewService(store, nil, nil).Update(context.Background(), channelmodel.UpdateInput{
		ChannelID: 1, ModelID: 1, UpstreamModel: "gpt-4", Status: channelmodel.StatusDisabled,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

// 启用成功（含 Model disabled 护栏）、停用/解除的 Offering 联动、全局最后供给自动停用 Model
// 等进入事务与 Model 锁的路径由 supply 联动 DB 测试覆盖（需 DATABASE_URL），
// 见 internal/platform/store/sqlc/supply_linkage_db_test.go。
