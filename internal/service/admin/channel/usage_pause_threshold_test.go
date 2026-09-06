package channel_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channel"
	subscriptionhealth "github.com/ThankCat/unio-gateway/internal/service/subscription/health"
)

type fakeUsagePauseReconciler struct {
	channelIDs []int64
}

func (r *fakeUsagePauseReconciler) ReconcileChannel(_ context.Context, channelID int64) (subscriptionhealth.ReconcileResult, error) {
	r.channelIDs = append(r.channelIDs, channelID)
	return subscriptionhealth.ReconcileResult{Scanned: 1}, nil
}

func int32Pointer(value int32) *int32 { return &value }

// poolRegistry 让池型渠道要求的 codex 适配器通过复合键校验。
type poolRegistry struct{}

func (poolRegistry) HasAny(protocol, adapterKey string) bool {
	return protocol == channel.ProtocolOpenAI && adapterKey == "codex"
}

func (poolRegistry) AdapterKeys(string) []string { return nil }

func poolChannelRow() sqlc.Channel {
	return sqlc.Channel{
		ID: 7, ProviderID: 1, Name: "pool", Protocols: []string{channel.ProtocolOpenAI},
		AdapterKey: "codex", Status: channel.StatusDisabled, CapacityRevision: 1,
		SupplyForm: channel.SupplyFormPool,
	}
}

// credential 渠道不能携带渠道级账号阈值：该设置只对池型有意义。
func TestCreateRejectsUsagePauseThresholdOnCredentialChannel(t *testing.T) {
	store := &createStore{provider: sqlc.Provider{ID: 1, Name: "Provider", Status: channel.StatusEnabled}}
	svc := channel.NewService(store, createRegistry{})

	_, err := svc.Create(context.Background(), channel.CreateInput{
		ProviderID: 1, Name: "primary", Protocols: []string{channel.ProtocolOpenAI},
		AdapterKey: channel.ProtocolOpenAI, Credential: "secret", Status: channel.StatusDisabled,
		AccountUsagePauseThresholdPercent: int32Pointer(80),
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
		t.Fatalf("error code = %q, want invalid argument (err=%v)", got, err)
	}
	if store.createCalls != 0 {
		t.Fatalf("CreateChannel calls = %d, want 0", store.createCalls)
	}
}

// 池型渠道创建时阈值落库：留空 → NULL（继承全局），1~100 → 覆写，0 与越界拒绝。
func TestCreatePoolChannelPersistsUsagePauseThreshold(t *testing.T) {
	cases := []struct {
		name      string
		threshold *int32
		wantErr   bool
		wantValid bool
	}{
		{name: "留空继承全局", threshold: nil, wantValid: false},
		{name: "显式 85 覆写", threshold: int32Pointer(85), wantValid: true},
		{name: "0 拒绝", threshold: int32Pointer(0), wantErr: true},
		{name: "101 拒绝", threshold: int32Pointer(101), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &createStore{provider: sqlc.Provider{ID: 1, Name: "Provider", Status: channel.StatusEnabled}}
			svc := channel.NewService(store, poolRegistry{})

			_, err := svc.Create(context.Background(), channel.CreateInput{
				ProviderID: 1, Name: "pool", Protocols: []string{channel.ProtocolOpenAI},
				AdapterKey: "codex", Status: channel.StatusDisabled, SupplyForm: channel.SupplyFormPool,
				AccountUsagePauseThresholdPercent: tc.threshold,
			})
			if tc.wantErr {
				if failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
					t.Fatalf("expected invalid argument, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			got := store.createParam.AccountUsagePauseThresholdPercent
			if got.Valid != tc.wantValid {
				t.Fatalf("stored threshold valid = %v, want %v", got.Valid, tc.wantValid)
			}
			if tc.wantValid && got.Int32 != *tc.threshold {
				t.Fatalf("stored threshold = %d, want %d", got.Int32, *tc.threshold)
			}
		})
	}
}

// 更新：缺省保持不变且不触发重算；真变化写库并重算该渠道；显式 null 回到继承；值未变不写库。
func TestUpdatePoolChannelUsagePauseThresholdReconcilesOnChange(t *testing.T) {
	t.Run("omitted keeps current and skips reconcile", func(t *testing.T) {
		store := &createStore{provider: sqlc.Provider{ID: 1, Name: "Provider", Status: channel.StatusEnabled}, channel: poolChannelRow()}
		store.channel.AccountUsagePauseThresholdPercent = pgtype.Int4{Int32: 80, Valid: true}
		reconciler := &fakeUsagePauseReconciler{}
		svc := channel.NewService(store, poolRegistry{}).WithUsagePauseReconciler(reconciler)

		updated, err := svc.Update(context.Background(), channel.UpdateInput{
			ID: 7, ProviderID: 1, Name: "pool", Status: channel.StatusDisabled,
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if store.thresholdUpdateCalls != 0 || len(reconciler.channelIDs) != 0 {
			t.Fatalf("omitted field must not write or reconcile, got writes=%d reconciles=%v", store.thresholdUpdateCalls, reconciler.channelIDs)
		}
		if updated.AccountUsagePauseThresholdPercent == nil || *updated.AccountUsagePauseThresholdPercent != 80 {
			t.Fatalf("threshold must be preserved, got %v", updated.AccountUsagePauseThresholdPercent)
		}
	})

	t.Run("changed value writes and reconciles the channel", func(t *testing.T) {
		store := &createStore{provider: sqlc.Provider{ID: 1, Name: "Provider", Status: channel.StatusEnabled}, channel: poolChannelRow()}
		reconciler := &fakeUsagePauseReconciler{}
		svc := channel.NewService(store, poolRegistry{}).WithUsagePauseReconciler(reconciler)

		updated, err := svc.Update(context.Background(), channel.UpdateInput{
			ID: 7, ProviderID: 1, Name: "pool", Status: channel.StatusDisabled,
			AccountUsagePauseThresholdProvided: true,
			AccountUsagePauseThresholdPercent:  int32Pointer(95),
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if store.thresholdUpdateCalls != 1 {
			t.Fatalf("threshold writes = %d, want 1", store.thresholdUpdateCalls)
		}
		if len(reconciler.channelIDs) != 1 || reconciler.channelIDs[0] != 7 {
			t.Fatalf("reconcile must target channel 7 once, got %v", reconciler.channelIDs)
		}
		if updated.AccountUsagePauseThresholdPercent == nil || *updated.AccountUsagePauseThresholdPercent != 95 {
			t.Fatalf("returned threshold = %v, want 95", updated.AccountUsagePauseThresholdPercent)
		}
	})

	t.Run("explicit null clears the override", func(t *testing.T) {
		store := &createStore{provider: sqlc.Provider{ID: 1, Name: "Provider", Status: channel.StatusEnabled}, channel: poolChannelRow()}
		store.channel.AccountUsagePauseThresholdPercent = pgtype.Int4{Int32: 80, Valid: true}
		reconciler := &fakeUsagePauseReconciler{}
		svc := channel.NewService(store, poolRegistry{}).WithUsagePauseReconciler(reconciler)

		updated, err := svc.Update(context.Background(), channel.UpdateInput{
			ID: 7, ProviderID: 1, Name: "pool", Status: channel.StatusDisabled,
			AccountUsagePauseThresholdProvided: true,
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if store.thresholdUpdateCalls != 1 || len(reconciler.channelIDs) != 1 {
			t.Fatalf("clearing must write once and reconcile once, got writes=%d reconciles=%v", store.thresholdUpdateCalls, reconciler.channelIDs)
		}
		if updated.AccountUsagePauseThresholdPercent != nil {
			t.Fatalf("threshold must be cleared, got %d", *updated.AccountUsagePauseThresholdPercent)
		}
	})

	t.Run("same value skips write and reconcile", func(t *testing.T) {
		store := &createStore{provider: sqlc.Provider{ID: 1, Name: "Provider", Status: channel.StatusEnabled}, channel: poolChannelRow()}
		store.channel.AccountUsagePauseThresholdPercent = pgtype.Int4{Int32: 80, Valid: true}
		reconciler := &fakeUsagePauseReconciler{}
		svc := channel.NewService(store, poolRegistry{}).WithUsagePauseReconciler(reconciler)

		if _, err := svc.Update(context.Background(), channel.UpdateInput{
			ID: 7, ProviderID: 1, Name: "pool", Status: channel.StatusDisabled,
			AccountUsagePauseThresholdProvided: true,
			AccountUsagePauseThresholdPercent:  int32Pointer(80),
		}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if store.thresholdUpdateCalls != 0 || len(reconciler.channelIDs) != 0 {
			t.Fatalf("unchanged value must not write or reconcile, got writes=%d reconciles=%v", store.thresholdUpdateCalls, reconciler.channelIDs)
		}
	})

	t.Run("credential channel rejects the field", func(t *testing.T) {
		row := poolChannelRow()
		row.SupplyForm = channel.SupplyFormCredential
		row.AdapterKey = channel.ProtocolOpenAI
		store := &createStore{provider: sqlc.Provider{ID: 1, Name: "Provider", Status: channel.StatusEnabled}, channel: row}
		svc := channel.NewService(store, createRegistry{})

		_, err := svc.Update(context.Background(), channel.UpdateInput{
			ID: 7, ProviderID: 1, Name: "pool", Status: channel.StatusDisabled,
			AccountUsagePauseThresholdProvided: true,
			AccountUsagePauseThresholdPercent:  int32Pointer(80),
		})
		if failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
			t.Fatalf("expected invalid argument, got %v", err)
		}
		if store.thresholdUpdateCalls != 0 {
			t.Fatalf("threshold writes = %d, want 0", store.thresholdUpdateCalls)
		}
	})
}
