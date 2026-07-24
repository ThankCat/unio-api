package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	adminchannel "github.com/ThankCat/unio-gateway/internal/service/admin/channel"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

const runtimeControlReconcileInterval = 5 * time.Second

// reconcileAllRuntimeControls performs one fail-closed reconciliation pass for both durable control
// families. Origin routing operations are resolved before stable controls are restored.
func reconcileAllRuntimeControls(
	ctx context.Context,
	pool *pgxpool.Pool,
	settings *appsettings.SettingsStore,
	controls *breakerstore.Store,
	telemetry *runtimeControlTelemetry,
) error {
	observation := telemetry.capture(ctx, pool)
	originReconciler := runtimecontrol.NewOriginRoutingReconciler(pool, controls)
	if telemetry != nil {
		originReconciler.WithObserver(telemetry)
	}
	if _, err := originReconciler.Reconcile(ctx); err != nil {
		telemetry.passFailed("origin_routing", err, observation)
		return err
	}
	if err := reconcileRuntimeControls(ctx, pool, settings, controls); err != nil {
		telemetry.passFailed("runtime_operations", err, observation)
		return err
	}
	if err := appsettings.RestoreCriticalRuntimeControlsObserved(
		ctx, settings, controls, telemetry.criticalSettingReconciled,
	); err != nil {
		telemetry.passFailed("critical_settings", err, observation)
		return err
	}
	if err := restoreChannelAdmissionControls(ctx, pool, controls, telemetry); err != nil {
		telemetry.passFailed("channel_admission", err, observation)
		return err
	}
	telemetry.passSucceeded(observation)
	return nil
}

// runRuntimeControlReconciler continuously repairs isolated control loss and response-loss operations.
// Errors are logged and retried; the affected Redis control remains absent/pending, so admission stays
// fail-closed until a later successful pass.
func runRuntimeControlReconciler(
	ctx context.Context,
	pool *pgxpool.Pool,
	settings *appsettings.SettingsStore,
	controls *breakerstore.Store,
	telemetry *runtimeControlTelemetry,
	afterSuccess ...func(context.Context, breakerstore.RuntimeReconciliationProof),
) {
	ticker := time.NewTicker(runtimeControlReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			generation := breakerstore.RuntimeReconciliationGeneration{}
			if len(afterSuccess) > 0 {
				var err error
				generation, err = controls.BeginRuntimeReconciliation(ctx)
				if err != nil {
					continue
				}
			}
			if err := reconcileAllRuntimeControls(ctx, pool, settings, controls, telemetry); err != nil {
				continue
			}
			if len(afterSuccess) == 0 {
				continue
			}
			proof, err := captureRuntimeReconciliationProof(ctx, pool, generation)
			if err != nil {
				continue
			}
			for _, callback := range afterSuccess {
				if callback != nil {
					callback(ctx, proof)
				}
			}
		}
	}
}

// captureRuntimeReconciliationProof reads the complete PostgreSQL authority set after a successful
// pass. The clear Lua atomically compares every listed Origin and Channel control with Redis.
func captureRuntimeReconciliationProof(
	ctx context.Context,
	pool *pgxpool.Pool,
	generation breakerstore.RuntimeReconciliationGeneration,
) (breakerstore.RuntimeReconciliationProof, error) {
	proof := breakerstore.RuntimeReconciliationProof{Generation: generation}
	rows, err := pool.Query(ctx, `
		SELECT p.status, pe.id, pe.base_url_revision, pe.status_revision, pe.status
		FROM providers p
		JOIN provider_origins pe ON pe.provider_id = p.id
		ORDER BY p.id, pe.id`)
	if err != nil {
		return breakerstore.RuntimeReconciliationProof{}, err
	}
	for rows.Next() {
		var providerStatus, originStatus string
		var origin breakerstore.RuntimeOriginControlProof
		if err := rows.Scan(
			&providerStatus,
			&origin.OriginID,
			&origin.BaseURLRevision,
			&origin.StatusRevision,
			&originStatus,
		); err != nil {
			rows.Close()
			return breakerstore.RuntimeReconciliationProof{}, err
		}
		origin.EffectiveStatus = runtimecontrol.EffectiveOriginStatus(providerStatus, originStatus)
		proof.OriginControls = append(proof.OriginControls, origin)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return breakerstore.RuntimeReconciliationProof{}, err
	}
	rows.Close()

	channels, err := sqlc.New(pool).ListChannelsForRuntimeControlRestore(ctx)
	if err != nil {
		return breakerstore.RuntimeReconciliationProof{}, err
	}
	proof.ChannelAdmissionControls = make([]breakerstore.RuntimeChannelAdmissionControlProof, 0, len(channels))
	for _, row := range channels {
		payload, err := adminchannel.CanonicalAdmissionLimitsPayloadFromChannel(row)
		if err != nil {
			return breakerstore.RuntimeReconciliationProof{}, err
		}
		proof.ChannelAdmissionControls = append(proof.ChannelAdmissionControls,
			breakerstore.RuntimeChannelAdmissionControlProof{
				ChannelID: row.ID,
				Revision:  row.AdmissionLimitsRevision,
				Payload:   payload,
			},
		)
	}
	return proof, nil
}

// reconcileRuntimeControls 在 RestoreMissing/严格核对前，先按 PostgreSQL durable operation
// 收口五个关键 setting 与 Channel admission limits 的所有普通非终态发布。
// epoch 由专用 coordinator 处理，不进入这里。
func reconcileRuntimeControls(
	ctx context.Context,
	pool *pgxpool.Pool,
	settings *appsettings.SettingsStore,
	controls *breakerstore.Store,
) error {
	reconciler := runtimecontrol.NewReconciler(pool, controls, func(op sqlc.RuntimeControlOperation) (breakerstore.ControlTarget, bool) {
		switch op.Kind {
		case runtimecontrol.KindAppSetting:
			if !op.SettingKey.Valid {
				return breakerstore.ControlTarget{}, false
			}
			switch op.SettingKey.String {
			case appsettings.GatewayRouteRateLimitDefaultsKey:
				return controls.RouteRateLimitControl(), true
			case appsettings.GatewayChannelRateLimitDefaultsKey:
				return controls.ChannelRateLimitControl(), true
			case appsettings.GatewayConcurrencyDefaultsKey:
				return controls.GlobalConcurrencyControl(), true
			case appsettings.GatewayCircuitBreakerKey, appsettings.GatewayRoutingBalanceKey:
				return controls.SettingControl(op.SettingKey.String), true
			default:
				return breakerstore.ControlTarget{}, false
			}
		case runtimecontrol.KindChannelAdmissionLimits:
			if !op.ChannelID.Valid || op.ChannelID.Int64 <= 0 {
				return breakerstore.ControlTarget{}, false
			}
			return controls.ChannelAdmissionControl(op.ChannelID.Int64), true
		default:
			return breakerstore.ControlTarget{}, false
		}
	})

	queries := sqlc.New(pool)
	_, err := reconciler.ReconcileWithPayload(ctx, func(ctx context.Context, op sqlc.RuntimeControlOperation) (string, bool, error) {
		expectedRevision := op.CurrentRevision
		if op.State == "db_committed" {
			expectedRevision = op.NextRevision
		}
		switch op.Kind {
		case runtimecontrol.KindAppSetting:
			if !op.SettingKey.Valid {
				return "", false, nil
			}
			record, err := settings.Record(ctx, op.SettingKey.String)
			if err != nil {
				return "", false, err
			}
			if record.Revision != expectedRevision {
				return "", false, fmt.Errorf(
					"runtimecontrol: setting %s revision=%d, operation %s expects %d",
					op.SettingKey.String, record.Revision, op.Token, expectedRevision,
				)
			}
			payload, err := appsettings.CanonicalRuntimeSettingPayload(op.SettingKey.String, record.Value)
			return string(payload), err == nil, err
		case runtimecontrol.KindChannelAdmissionLimits:
			if !op.ChannelID.Valid || op.ChannelID.Int64 <= 0 {
				return "", false, nil
			}
			row, err := queries.GetChannel(ctx, op.ChannelID.Int64)
			if err != nil {
				return "", false, err
			}
			if row.AdmissionLimitsRevision != expectedRevision {
				return "", false, fmt.Errorf(
					"runtimecontrol: channel %d admission revision=%d, operation %s expects %d",
					row.ID, row.AdmissionLimitsRevision, op.Token, expectedRevision,
				)
			}
			payload, err := adminchannel.CanonicalAdmissionLimitsPayloadFromChannel(row)
			return payload, err == nil, err
		default:
			return "", false, nil
		}
	})
	return err
}

// restoreChannelAdmissionControls 只补齐缺失 control，并严格拒绝 stale/ahead/pending/payload mismatch。
func restoreChannelAdmissionControls(
	ctx context.Context,
	pool *pgxpool.Pool,
	controls *breakerstore.Store,
	telemetry *runtimeControlTelemetry,
) error {
	rows, err := sqlc.New(pool).ListChannelsForRuntimeControlRestore(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		payload, err := adminchannel.CanonicalAdmissionLimitsPayloadFromChannel(row)
		if err != nil {
			return err
		}
		target := controls.ChannelAdmissionControl(row.ID)
		restored, err := controls.RestoreMissingControl(ctx, target, row.AdmissionLimitsRevision, payload)
		if err != nil {
			return err
		}
		snapshot, err := controls.ReadControl(ctx, target, row.AdmissionLimitsRevision)
		if err != nil {
			return err
		}
		if snapshot.SyncState != "active" || snapshot.PendingRevision != 0 ||
			snapshot.ActiveRevision != row.AdmissionLimitsRevision ||
			!bytes.Equal([]byte(snapshot.ActivePayload), []byte(payload)) {
			return fmt.Errorf("runtimecontrol: channel %d admission control requires reconciliation", row.ID)
		}
		telemetry.channelControlReconciled(row.ID, row.AdmissionLimitsRevision, restored)
	}
	return nil
}
