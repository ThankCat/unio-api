package runtimecontrol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	StateEpochReleaseEvidenceSchemaVersion = 1
	maximumStateEpochReleaseEvidenceAge    = 15 * time.Minute
)

type StateEpochReleaseEvidenceStatus string

const StateEpochReleaseEvidencePassed StateEpochReleaseEvidenceStatus = "passed"

// StateEpochReleaseEvidence proves that a real Gateway smoke ran after the new
// epoch became ready while the durable maintenance lock was still held.
type StateEpochReleaseEvidence struct {
	SchemaVersion int                             `json:"schema_version"`
	RecoveryID    string                          `json:"recovery_id"`
	Revision      int64                           `json:"revision"`
	Status        StateEpochReleaseEvidenceStatus `json:"status"`
	CheckedAt     time.Time                       `json:"checked_at"`
	SummaryHash   string                          `json:"summary_hash"`
}

func DecodeStateEpochReleaseEvidence(raw []byte) (StateEpochReleaseEvidence, error) {
	var evidence StateEpochReleaseEvidence
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		return StateEpochReleaseEvidence{}, fmt.Errorf("runtimecontrol: decode state epoch release evidence: %w", err)
	}
	if err := ensureReleaseEvidenceJSONEOF(decoder); err != nil {
		return StateEpochReleaseEvidence{}, err
	}
	evidence.CheckedAt = evidence.CheckedAt.UTC()
	if err := evidence.ValidateShape(); err != nil {
		return StateEpochReleaseEvidence{}, err
	}
	return evidence, nil
}

func (e StateEpochReleaseEvidence) ValidateShape() error {
	if e.SchemaVersion != StateEpochReleaseEvidenceSchemaVersion {
		return errors.New("runtimecontrol: unsupported state epoch release evidence schema")
	}
	if !validRecoveryID(e.RecoveryID) || e.Revision < 2 {
		return errors.New("runtimecontrol: invalid state epoch release identity")
	}
	if e.Status != StateEpochReleaseEvidencePassed || e.CheckedAt.IsZero() || !validSHA256(e.SummaryHash) {
		return errors.New("runtimecontrol: state epoch release requires passed post-commit smoke evidence")
	}
	return nil
}

func (e StateEpochReleaseEvidence) ValidateRelease(
	transition StateEpochTransition,
	activatedAt time.Time,
	now time.Time,
) error {
	if err := e.ValidateDurableBinding(transition, activatedAt); err != nil {
		return err
	}
	checkedAt := e.CheckedAt.UTC()
	if checkedAt.After(now.UTC()) {
		return errors.New("runtimecontrol: state epoch release smoke is outside the post-commit window")
	}
	if now.UTC().Sub(checkedAt) > maximumStateEpochReleaseEvidenceAge {
		return errors.New("runtimecontrol: state epoch release smoke evidence is stale")
	}
	return nil
}

func (e StateEpochReleaseEvidence) ValidateDurableBinding(
	transition StateEpochTransition,
	activatedAt time.Time,
) error {
	if err := e.ValidateShape(); err != nil {
		return err
	}
	if transition.RecoveryID == nil || e.RecoveryID != *transition.RecoveryID || e.Revision != transition.NewRevision {
		return errors.New("runtimecontrol: state epoch release evidence identity does not match transition")
	}
	checkedAt := e.CheckedAt.UTC()
	if checkedAt.Before(activatedAt.UTC()) {
		return errors.New("runtimecontrol: state epoch release smoke is outside the post-commit window")
	}
	return nil
}

func (e StateEpochReleaseEvidence) Marshal() (json.RawMessage, error) {
	if err := e.ValidateShape(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("runtimecontrol: encode state epoch release evidence: %w", err)
	}
	return raw, nil
}

func sameReleaseEvidence(left, right StateEpochReleaseEvidence) bool {
	leftRaw, leftErr := left.Marshal()
	rightRaw, rightErr := right.Marshal()
	return leftErr == nil && rightErr == nil && bytes.Equal(leftRaw, rightRaw)
}

func ensureReleaseEvidenceJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("runtimecontrol: state epoch release evidence contains trailing JSON value")
	}
	return fmt.Errorf("runtimecontrol: decode trailing state epoch release evidence data: %w", err)
}
