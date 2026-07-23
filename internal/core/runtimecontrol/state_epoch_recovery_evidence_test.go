package runtimecontrol_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
)

func TestStateEpochRecoveryEvidenceStrictApproval(t *testing.T) {
	detectedAt := time.Date(2026, time.July, 22, 3, 0, 0, 0, time.UTC)
	notBefore := detectedAt.Add(24 * time.Hour)
	checkedAt := notBefore.Add(time.Minute)
	recordedAt := checkedAt.Add(time.Minute)
	oldEpoch := "00112233445566778899aabbccddeeff"
	oldRevision := int64(3)
	recoveryID := "recovery-123"
	transition := runtimecontrol.StateEpochTransition{
		RecoveryID: &recoveryID,
		OldEpoch:   &oldEpoch, OldRevision: &oldRevision,
		NewEpoch: "ffeeddccbbaa99887766554433221100", NewRevision: 4,
		Reason: runtimecontrol.StateEpochReasonStateLoss, StateLossConfirmed: true,
		DetectedAt: detectedAt, NotBefore: notBefore,
	}
	evidence := approvedRecoveryEvidence(transition, "change-123", checkedAt, recordedAt)
	raw, err := evidence.Marshal()
	if err != nil {
		t.Fatalf("marshal approved evidence: %v", err)
	}
	decoded, err := runtimecontrol.DecodeStateEpochRecoveryEvidence(raw)
	if err != nil {
		t.Fatalf("decode approved evidence: %v", err)
	}
	if err := decoded.ValidateCommit(transition, "change-123", recordedAt.Add(time.Minute)); err != nil {
		t.Fatalf("validate approved evidence: %v", err)
	}
	if err := decoded.ValidateCommit(transition, "change-124", recordedAt.Add(time.Minute)); err == nil {
		t.Fatal("changed operator reference must be rejected")
	}
	if err := decoded.ValidateCommit(transition, "change-123", recordedAt.Add(16*time.Minute)); err == nil {
		t.Fatal("stale recovery evidence must be rejected")
	}
	mismatched := evidence
	mismatched.RecoveryID = "another-recovery"
	if err := mismatched.ValidateCommit(transition, "change-123", recordedAt.Add(time.Minute)); err == nil {
		t.Fatal("evidence bound to another recovery_id must be rejected")
	}

	badWindow := evidence
	tooEarly := notBefore.Add(-time.Second)
	badWindow.Gates.Window.CheckedAt = &tooEarly
	if err := badWindow.ValidateCommit(transition, "change-123", recordedAt.Add(time.Minute)); err == nil {
		t.Fatal("window gate before not_before must be rejected")
	}
}

func TestStateEpochRecoveryEvidenceRejectsUnsafeShapes(t *testing.T) {
	now := time.Date(2026, time.July, 23, 3, 0, 0, 0, time.UTC)
	transition := testRecoveryTransition(now.Add(-time.Hour), now)
	collecting, err := runtimecontrol.NewCollectingStateEpochRecoveryEvidence(transition, "operator:42", now)
	if err != nil {
		t.Fatalf("new collecting evidence: %v", err)
	}
	raw, err := collecting.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["credential"] = "must-not-fit-schema"
	unsafeRaw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimecontrol.DecodeStateEpochRecoveryEvidence(unsafeRaw); err == nil {
		t.Fatal("unknown evidence field must be rejected")
	}

	approved := approvedRecoveryEvidence(transition, "operator:42", now, now)
	upperHash := strings.Repeat("A", 64)
	approved.Gates.Control.SummaryHash = &upperHash
	if err := approved.ValidateShape(); err == nil {
		t.Fatal("uppercase digest must be rejected")
	}
	if _, err := runtimecontrol.NewCollectingStateEpochRecoveryEvidence(transition, "bad operator ref", now); err == nil {
		t.Fatal("operator reference with spaces must be rejected")
	}
}

func approvedRecoveryEvidence(
	transition runtimecontrol.StateEpochTransition,
	operatorRef string,
	checkedAt, recordedAt time.Time,
) runtimecontrol.StateEpochRecoveryEvidence {
	hash := strings.Repeat("a", 64)
	passed := runtimecontrol.StateEpochRecoveryGate{
		Status: runtimecontrol.StateEpochRecoveryGatePassed, CheckedAt: &checkedAt, SummaryHash: &hash,
	}
	return runtimecontrol.StateEpochRecoveryEvidence{
		SchemaVersion:   runtimecontrol.StateEpochRecoveryEvidenceSchemaVersion,
		RecoveryID:      *transition.RecoveryID,
		CurrentRevision: *transition.OldRevision,
		Reason:          transition.Reason,
		DetectedAt:      transition.DetectedAt,
		NotBefore:       transition.NotBefore,
		OperatorRef:     operatorRef,
		Status:          runtimecontrol.StateEpochRecoveryEvidenceApproved,
		RecordedAt:      recordedAt,
		Gates: runtimecontrol.StateEpochRecoveryGates{
			IngressClosed: passed,
			Drain:         passed, Window: passed, BreakerCooldown: passed, Permission: passed,
			Control: passed, OfflineScripts: passed, MaintenanceProbe: passed,
		},
	}
}

func testRecoveryTransition(detectedAt, notBefore time.Time) runtimecontrol.StateEpochTransition {
	oldEpoch := "00112233445566778899aabbccddeeff"
	oldRevision := int64(3)
	recoveryID := "recovery-test"
	return runtimecontrol.StateEpochTransition{
		RecoveryID: &recoveryID,
		OldEpoch:   &oldEpoch, OldRevision: &oldRevision,
		NewEpoch: "ffeeddccbbaa99887766554433221100", NewRevision: 4,
		Reason: runtimecontrol.StateEpochReasonStateLoss, StateLossConfirmed: true,
		DetectedAt: detectedAt, NotBefore: notBefore,
	}
}

func passedReleaseEvidence(
	transition runtimecontrol.StateEpochTransition,
	checkedAt time.Time,
) runtimecontrol.StateEpochReleaseEvidence {
	return runtimecontrol.StateEpochReleaseEvidence{
		SchemaVersion: runtimecontrol.StateEpochReleaseEvidenceSchemaVersion,
		RecoveryID:    *transition.RecoveryID,
		Revision:      transition.NewRevision,
		Status:        runtimecontrol.StateEpochReleaseEvidencePassed,
		CheckedAt:     checkedAt,
		SummaryHash:   strings.Repeat("d", 64),
	}
}

func TestStateEpochReleaseEvidenceRequiresPostCommitSmoke(t *testing.T) {
	transition := testRecoveryTransition(
		time.Date(2026, time.July, 22, 1, 0, 0, 0, time.UTC),
		time.Date(2026, time.July, 22, 2, 0, 0, 0, time.UTC),
	)
	activatedAt := time.Date(2026, time.July, 22, 3, 0, 0, 0, time.UTC)
	evidence := passedReleaseEvidence(transition, activatedAt.Add(time.Second))
	if err := evidence.ValidateRelease(transition, activatedAt, activatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("validate release evidence: %v", err)
	}
	evidence.CheckedAt = activatedAt.Add(-time.Nanosecond)
	if err := evidence.ValidateRelease(transition, activatedAt, activatedAt.Add(time.Minute)); err == nil {
		t.Fatal("pre-commit smoke must not release maintenance lock")
	}
	evidence = passedReleaseEvidence(transition, activatedAt.Add(time.Second))
	evidence.RecoveryID = "another-recovery"
	if err := evidence.ValidateRelease(transition, activatedAt, activatedAt.Add(time.Minute)); err == nil {
		t.Fatal("different recovery_id must not release maintenance lock")
	}
	evidence = passedReleaseEvidence(transition, activatedAt.Add(time.Second))
	if err := evidence.ValidateRelease(transition, activatedAt, activatedAt.Add(16*time.Minute)); err == nil {
		t.Fatal("stale post-commit smoke must not release maintenance lock")
	}
}
