package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
)

func TestParseBeginCommandRequiresExplicitConfirmations(t *testing.T) {
	base := []string{
		"begin",
		"--reason", "state_loss",
		"--detected-at", "2026-07-22T01:00:00Z",
		"--not-before", "2026-07-23T01:00:00Z",
		"--operator-ref", "change-123",
		"--recovery-id", "recovery-123",
		"--expected-current-revision", "7",
	}
	if _, err := parseMaintenanceCommand(base); err == nil {
		t.Fatal("begin without confirmations must be rejected")
	}
	args := append(append([]string{}, base...), "--confirm-state-loss", "--confirm-external-ingress-blocked")
	command, err := parseMaintenanceCommand(args)
	if err != nil {
		t.Fatalf("parse valid begin: %v", err)
	}
	if command.kind != commandBegin || command.begin.Reason != runtimecontrol.StateEpochReasonStateLoss ||
		command.begin.RecoveryID != "recovery-123" || command.begin.ExpectedCurrentRevision != 7 ||
		!command.begin.StateLossConfirmed || !command.begin.ExternalIngressBlockedConfirmed {
		t.Fatalf("unexpected begin command: %+v", command)
	}

	restore := append([]string{}, args...)
	restore[2] = "restore"
	command, err = parseMaintenanceCommand(restore)
	if err != nil || command.begin.Reason != runtimecontrol.StateEpochReasonRestore {
		t.Fatalf("parse restore begin: command=%+v err=%v", command, err)
	}
}

func TestParseMaintenanceCommandRejectsUnsafeValues(t *testing.T) {
	tests := [][]string{
		{},
		{"unknown"},
		{"begin", "--reason", "bootstrap", "--detected-at", "2026-07-22T01:00:00Z", "--not-before", "2026-07-23T01:00:00Z", "--operator-ref", "x", "--confirm-state-loss", "--confirm-external-ingress-blocked"},
		{"begin", "--reason", "state_loss", "--detected-at", "2026-07-23T01:00:00Z", "--not-before", "2026-07-22T01:00:00Z", "--operator-ref", "x", "--confirm-state-loss", "--confirm-external-ingress-blocked"},
		{"begin", "--reason", "state_loss", "--detected-at", "2026-07-22T01:00:00Z", "--not-before", "2026-07-23T01:00:01Z", "--operator-ref", "x", "--recovery-id", "recovery-x", "--expected-current-revision", "1", "--confirm-state-loss", "--confirm-external-ingress-blocked"},
		{"commit"},
		{"commit", "--evidence-file", "evidence.json", "--recovery-id", "recovery-1", "--revision", "1"},
		{"release", "--evidence-file", "evidence.json", "--recovery-id", "recovery-1", "--revision", "1"},
	}
	for _, args := range tests {
		if _, err := parseMaintenanceCommand(args); err == nil {
			t.Fatalf("unsafe command accepted: %v", args)
		}
	}
}

func TestParseCommitAndReleaseRequireBoundIdentity(t *testing.T) {
	for _, name := range []string{"commit", "release"} {
		command, err := parseMaintenanceCommand([]string{
			name, "--evidence-file", "evidence.json", "--recovery-id", "recovery-123", "--revision", "8",
		})
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if command.recoveryID != "recovery-123" || command.revision != 8 || command.evidenceFile != "evidence.json" {
			t.Fatalf("unexpected %s command: %+v", name, command)
		}
	}
}

func TestReadRecoveryEvidenceHasHardSizeLimit(t *testing.T) {
	dir := t.TempDir()
	validPath := filepath.Join(dir, "valid.json")
	if err := os.WriteFile(validPath, []byte(`{"status":"approved"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if raw, err := readRecoveryEvidence(validPath); err != nil || !strings.Contains(string(raw), "approved") {
		t.Fatalf("read valid evidence: raw=%q err=%v", raw, err)
	}
	largePath := filepath.Join(dir, "large.json")
	if err := os.WriteFile(largePath, []byte(strings.Repeat("x", maximumRecoveryEvidenceBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRecoveryEvidence(largePath); err == nil {
		t.Fatal("oversized evidence must be rejected")
	}
}
