package runtimecontrol_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func TestBootstrapStateEpochTransitionIsStrictAndCanonical(t *testing.T) {
	now := time.Date(2026, time.July, 22, 1, 2, 3, 0, time.UTC)
	transition, err := runtimecontrol.NewBootstrapStateEpochTransition(now)
	if err != nil {
		t.Fatalf("new bootstrap transition: %v", err)
	}
	if transition.OldEpoch != nil || transition.OldRevision != nil || transition.NewRevision != 1 ||
		transition.Reason != runtimecontrol.StateEpochReasonBootstrap || !transition.StateLossConfirmed {
		t.Fatalf("unexpected bootstrap transition: %+v", transition)
	}
	raw, err := transition.Marshal()
	if err != nil {
		t.Fatalf("marshal bootstrap transition: %v", err)
	}
	decoded, err := runtimecontrol.DecodeStateEpochTransition(raw)
	if err != nil {
		t.Fatalf("decode bootstrap transition: %v", err)
	}
	if decoded.NewEpoch != transition.NewEpoch || !decoded.NotBefore.Equal(now) {
		t.Fatalf("transition changed after round trip: %+v", decoded)
	}
}

func TestStateEpochTransitionRejectsUnknownAndUnsafeShapes(t *testing.T) {
	now := time.Date(2026, time.July, 22, 1, 2, 3, 0, time.UTC)
	oldEpoch := "00112233445566778899aabbccddeeff"
	oldRevision := int64(4)
	recoveryID := "recovery-4"
	valid := runtimecontrol.StateEpochTransition{
		RecoveryID: &recoveryID,
		OldEpoch:   &oldEpoch, OldRevision: &oldRevision,
		NewEpoch: "ffeeddccbbaa99887766554433221100", NewRevision: 5,
		Reason: runtimecontrol.StateEpochReasonStateLoss, StateLossConfirmed: true,
		DetectedAt: now, NotBefore: now.Add(time.Hour),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid state-loss transition rejected: %v", err)
	}

	unsafe := valid
	unsafe.StateLossConfirmed = false
	if err := unsafe.Validate(); err == nil {
		t.Fatal("unconfirmed state loss must be rejected")
	}
	unsafe = valid
	unsafe.NewRevision = oldRevision
	if err := unsafe.Validate(); err == nil {
		t.Fatal("non-incrementing revision must be rejected")
	}

	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"abort_allowed":true}`)...)
	if _, err := runtimecontrol.DecodeStateEpochTransition(raw); err == nil {
		t.Fatal("unknown transition field must be rejected")
	}
}

func TestNewStateEpochRecoveryTransitionRequiresExplicitSafeInputs(t *testing.T) {
	now := time.Date(2026, time.July, 22, 1, 2, 3, 0, time.UTC)
	activatedAt := now.Add(-time.Hour)
	old := runtimecontrol.StateEpoch{
		Epoch: "00112233445566778899aabbccddeeff", State: runtimecontrol.StateEpochReady,
		Reason: runtimecontrol.StateEpochReasonBootstrap, ActivatedAt: &activatedAt,
	}
	transition, err := runtimecontrol.NewStateEpochRecoveryTransition(
		old, 4, "recovery-restore", runtimecontrol.StateEpochReasonRestore, true, now, now.Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("new restore transition: %v", err)
	}
	if transition.NewRevision != 5 || transition.NewEpoch == old.Epoch || transition.Reason != runtimecontrol.StateEpochReasonRestore {
		t.Fatalf("unexpected restore transition: %+v", transition)
	}
	if _, err := runtimecontrol.NewStateEpochRecoveryTransition(
		old, 4, "recovery-restore", runtimecontrol.StateEpochReasonRestore, false, now, now.Add(time.Hour),
	); err == nil {
		t.Fatal("unconfirmed state loss must be rejected")
	}
	if _, err := runtimecontrol.NewStateEpochRecoveryTransition(
		old, 4, "recovery-bootstrap", runtimecontrol.StateEpochReasonBootstrap, true, now, now.Add(time.Hour),
	); err == nil {
		t.Fatal("bootstrap reason must be rejected by maintenance transition")
	}
	if _, err := runtimecontrol.NewStateEpochRecoveryTransition(
		old, 4, "recovery-too-late", runtimecontrol.StateEpochReasonRestore, true, now, now.Add(24*time.Hour+time.Second),
	); err == nil {
		t.Fatal("not_before beyond 24 hours must be rejected")
	}
}

func TestEnsureStateEpochSeedRequiresCoordinator(t *testing.T) {
	_, err := runtimecontrol.EnsureStateEpochSeed(t.Context(), nil)
	if failure.CodeOf(err) != failure.CodeConfigInvalid || errors.Is(err, runtimecontrol.ErrStateEpochConflict) {
		t.Fatalf("unexpected nil coordinator error: code=%q err=%v", failure.CodeOf(err), err)
	}
}
