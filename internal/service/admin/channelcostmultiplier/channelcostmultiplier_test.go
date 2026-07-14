package channelcostmultiplier

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// fakeStore 是 channelcostmultiplier.Store 的可配置测试替身。
type fakeStore struct {
	channel        sqlc.Channel
	channelErr     error
	binding        sqlc.ChannelModel
	bindingErr     error
	windows        []sqlc.ListEnabledChannelCostMultiplierWindowsRow
	created        sqlc.CreateChannelCostMultiplierParams
	createReturn   sqlc.ChannelCostMultiplier
	existing       sqlc.ChannelCostMultiplier
	existingErr    error
	updateReturn   sqlc.ChannelCostMultiplier
	lastWindowsArg sqlc.ListEnabledChannelCostMultiplierWindowsParams
}

func (s *fakeStore) GetChannel(context.Context, int64) (sqlc.Channel, error) {
	return s.channel, s.channelErr
}
func (s *fakeStore) GetChannelModel(context.Context, sqlc.GetChannelModelParams) (sqlc.ChannelModel, error) {
	return s.binding, s.bindingErr
}
func (s *fakeStore) GetChannelCostMultiplier(context.Context, int64) (sqlc.ChannelCostMultiplier, error) {
	return s.existing, s.existingErr
}
func (s *fakeStore) ListChannelCostMultipliersByChannel(context.Context, int64) ([]sqlc.ListChannelCostMultipliersByChannelRow, error) {
	return nil, nil
}
func (s *fakeStore) ListEnabledChannelCostMultiplierWindows(_ context.Context, arg sqlc.ListEnabledChannelCostMultiplierWindowsParams) ([]sqlc.ListEnabledChannelCostMultiplierWindowsRow, error) {
	s.lastWindowsArg = arg
	return s.windows, nil
}
func (s *fakeStore) CreateChannelCostMultiplier(_ context.Context, arg sqlc.CreateChannelCostMultiplierParams) (sqlc.ChannelCostMultiplier, error) {
	s.created = arg
	return s.createReturn, nil
}
func (s *fakeStore) UpdateChannelCostMultiplierWindow(context.Context, sqlc.UpdateChannelCostMultiplierWindowParams) (sqlc.ChannelCostMultiplier, error) {
	return s.updateReturn, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func TestCreateDefaultMultiplierSuccess(t *testing.T) {
	store := &fakeStore{
		createReturn: sqlc.ChannelCostMultiplier{
			ID: 7, ChannelID: 3, Multiplier: mustNumeric(t, "1.2"), Status: "enabled",
			EffectiveFrom: ts(time.Now()),
		},
	}
	svc := NewService(store)

	got, err := svc.Create(context.Background(), CreateInput{
		ChannelID:     3,
		ModelID:       nil, // 渠道默认倍率
		Multiplier:    "1.2",
		Status:        "enabled",
		EffectiveFrom: time.Now(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Multiplier != "1.2" || got.ModelID != nil {
		t.Fatalf("unexpected result: %+v", got)
	}
	// 默认倍率：窗口重叠查询与建行 model_id 都应为 NULL。
	if store.created.ModelID.Valid {
		t.Fatal("expected default multiplier to persist NULL model_id")
	}
	if store.lastWindowsArg.ModelID.Valid {
		t.Fatal("expected overlap check to use NULL model_key for default")
	}
}

func TestCreateRejectsOverlappingWindow(t *testing.T) {
	store := &fakeStore{
		windows: []sqlc.ListEnabledChannelCostMultiplierWindowsRow{
			{ID: 1, EffectiveFrom: ts(time.Now().Add(-time.Hour)), EffectiveTo: pgtype.Timestamptz{Valid: false}},
		},
	}
	svc := NewService(store)

	_, err := svc.Create(context.Background(), CreateInput{
		ChannelID:     3,
		Multiplier:    "1.2",
		Status:        "enabled",
		EffectiveFrom: time.Now(),
	})
	if failure.CodeOf(err) != failure.CodeAdminPricingWindowOverlap {
		t.Fatalf("expected window overlap error, got %v", err)
	}
}

func TestCreateOverrideRejectsUnboundModel(t *testing.T) {
	modelID := int64(9)
	store := &fakeStore{bindingErr: pgx.ErrNoRows}
	svc := NewService(store)

	_, err := svc.Create(context.Background(), CreateInput{
		ChannelID:     3,
		ModelID:       &modelID,
		Multiplier:    "1.4",
		Status:        "enabled",
		EffectiveFrom: time.Now(),
	})
	if failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected invalid argument for unbound model, got %v", err)
	}
}

func TestCreateRejectsNegativeMultiplier(t *testing.T) {
	svc := NewService(&fakeStore{})
	_, err := svc.Create(context.Background(), CreateInput{
		ChannelID:     3,
		Multiplier:    "-0.5",
		Status:        "enabled",
		EffectiveFrom: time.Now(),
	})
	if failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected invalid argument for negative multiplier, got %v", err)
	}
}

func mustNumeric(t *testing.T, s string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatalf("scan numeric %q: %v", s, err)
	}
	return n
}
