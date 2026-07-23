package runtimecontrol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	StateEpochRecoveryEvidenceSchemaVersion = 1
	maximumStateEpochEvidenceAge            = 15 * time.Minute
)

type StateEpochRecoveryEvidenceStatus string

const (
	StateEpochRecoveryEvidenceCollecting StateEpochRecoveryEvidenceStatus = "collecting"
	StateEpochRecoveryEvidenceApproved   StateEpochRecoveryEvidenceStatus = "approved"
)

type StateEpochRecoveryGateStatus string

const (
	StateEpochRecoveryGatePending StateEpochRecoveryGateStatus = "pending"
	StateEpochRecoveryGatePassed  StateEpochRecoveryGateStatus = "passed"
)

// StateEpochRecoveryGate keeps only a classified result, timestamp and digest.
// The underlying probe output must stay in the restricted maintenance system.
type StateEpochRecoveryGate struct {
	Status      StateEpochRecoveryGateStatus `json:"status"`
	CheckedAt   *time.Time                   `json:"checked_at"`
	SummaryHash *string                      `json:"summary_hash"`
}

type StateEpochRecoveryGates struct {
	IngressClosed    StateEpochRecoveryGate `json:"ingress_closed"`
	Drain            StateEpochRecoveryGate `json:"drain"`
	Window           StateEpochRecoveryGate `json:"window"`
	BreakerCooldown  StateEpochRecoveryGate `json:"breaker_cooldown"`
	Permission       StateEpochRecoveryGate `json:"permission"`
	Control          StateEpochRecoveryGate `json:"control"`
	OfflineScripts   StateEpochRecoveryGate `json:"offline_scripts"`
	MaintenanceProbe StateEpochRecoveryGate `json:"maintenance_probe"`
}

// StateEpochRecoveryEvidence is the only durable authorization accepted by a
// non-bootstrap epoch Commit. Its fixed schema deliberately has no place for
// URLs, credentials, request/response bodies or Redis operation identifiers.
type StateEpochRecoveryEvidence struct {
	SchemaVersion   int                              `json:"schema_version"`
	RecoveryID      string                           `json:"recovery_id"`
	CurrentRevision int64                            `json:"current_revision"`
	Reason          StateEpochReason                 `json:"reason"`
	DetectedAt      time.Time                        `json:"detected_at"`
	NotBefore       time.Time                        `json:"not_before"`
	OperatorRef     string                           `json:"operator_ref"`
	Status          StateEpochRecoveryEvidenceStatus `json:"status"`
	RecordedAt      time.Time                        `json:"recorded_at"`
	Gates           StateEpochRecoveryGates          `json:"gates"`
}

func NewCollectingStateEpochRecoveryEvidence(
	transition StateEpochTransition,
	operatorRef string,
	now time.Time,
) (StateEpochRecoveryEvidence, error) {
	if err := transition.Validate(); err != nil || transition.RecoveryID == nil || transition.OldRevision == nil ||
		(transition.Reason != StateEpochReasonStateLoss && transition.Reason != StateEpochReasonRestore) {
		return StateEpochRecoveryEvidence{}, errors.New("runtimecontrol: collecting recovery evidence requires a non-bootstrap transition")
	}
	pending := StateEpochRecoveryGate{Status: StateEpochRecoveryGatePending}
	evidence := StateEpochRecoveryEvidence{
		SchemaVersion:   StateEpochRecoveryEvidenceSchemaVersion,
		RecoveryID:      *transition.RecoveryID,
		CurrentRevision: *transition.OldRevision,
		Reason:          transition.Reason,
		DetectedAt:      transition.DetectedAt.UTC(),
		NotBefore:       transition.NotBefore.UTC(),
		OperatorRef:     operatorRef,
		Status:          StateEpochRecoveryEvidenceCollecting,
		RecordedAt:      now.UTC(),
		Gates: StateEpochRecoveryGates{
			IngressClosed: pending,
			Drain:         pending, Window: pending, BreakerCooldown: pending,
			Permission: pending, Control: pending, OfflineScripts: pending,
			MaintenanceProbe: pending,
		},
	}
	if err := evidence.ValidateShape(); err != nil {
		return StateEpochRecoveryEvidence{}, err
	}
	return evidence, nil
}

func DecodeStateEpochRecoveryEvidence(raw []byte) (StateEpochRecoveryEvidence, error) {
	var evidence StateEpochRecoveryEvidence
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		return StateEpochRecoveryEvidence{}, fmt.Errorf("runtimecontrol: decode state epoch recovery evidence: %w", err)
	}
	if err := ensureRecoveryEvidenceJSONEOF(decoder); err != nil {
		return StateEpochRecoveryEvidence{}, err
	}
	evidence.RecordedAt = evidence.RecordedAt.UTC()
	evidence.DetectedAt = evidence.DetectedAt.UTC()
	evidence.NotBefore = evidence.NotBefore.UTC()
	normalizeRecoveryGateTimes(&evidence.Gates)
	if err := evidence.ValidateShape(); err != nil {
		return StateEpochRecoveryEvidence{}, err
	}
	return evidence, nil
}

func (e StateEpochRecoveryEvidence) ValidateShape() error {
	if e.SchemaVersion != StateEpochRecoveryEvidenceSchemaVersion {
		return errors.New("runtimecontrol: unsupported state epoch recovery evidence schema")
	}
	if !validRecoveryID(e.RecoveryID) {
		return errors.New("runtimecontrol: invalid state epoch recovery_id")
	}
	if e.CurrentRevision < 1 {
		return errors.New("runtimecontrol: invalid state epoch recovery current_revision")
	}
	if e.Reason != StateEpochReasonStateLoss && e.Reason != StateEpochReasonRestore {
		return errors.New("runtimecontrol: invalid state epoch recovery reason")
	}
	if e.DetectedAt.IsZero() || e.NotBefore.IsZero() || e.NotBefore.Before(e.DetectedAt) ||
		e.NotBefore.After(e.DetectedAt.Add(maximumStateEpochRecoveryDelay)) {
		return errors.New("runtimecontrol: invalid state epoch recovery evidence window")
	}
	if !validOperatorReference(e.OperatorRef) {
		return errors.New("runtimecontrol: invalid state epoch recovery operator reference")
	}
	if e.RecordedAt.IsZero() {
		return errors.New("runtimecontrol: state epoch recovery evidence requires recorded_at")
	}

	gates := e.gateList()
	switch e.Status {
	case StateEpochRecoveryEvidenceCollecting:
		for _, gate := range gates {
			if gate.Status != StateEpochRecoveryGatePending || gate.CheckedAt != nil || gate.SummaryHash != nil {
				return errors.New("runtimecontrol: collecting recovery evidence requires pending gates")
			}
		}
	case StateEpochRecoveryEvidenceApproved:
		for _, gate := range gates {
			if gate.Status != StateEpochRecoveryGatePassed || gate.CheckedAt == nil || gate.CheckedAt.IsZero() ||
				gate.SummaryHash == nil || !validSHA256(*gate.SummaryHash) {
				return errors.New("runtimecontrol: approved recovery evidence requires passed gates with timestamp and digest")
			}
		}
	default:
		return errors.New("runtimecontrol: invalid state epoch recovery evidence status")
	}
	return nil
}

func (e StateEpochRecoveryEvidence) ValidateCommit(
	transition StateEpochTransition,
	expectedOperatorRef string,
	now time.Time,
) error {
	if err := e.ValidateDurableBinding(transition, expectedOperatorRef); err != nil {
		return err
	}
	now = now.UTC()
	if now.Before(transition.NotBefore) {
		return errors.New("runtimecontrol: state epoch recovery not_before has not been reached")
	}
	if e.RecordedAt.After(now) {
		return errors.New("runtimecontrol: state epoch recovery recorded_at is outside the recovery window")
	}
	if now.Sub(e.RecordedAt) > maximumStateEpochEvidenceAge {
		return errors.New("runtimecontrol: state epoch recovery evidence is stale")
	}
	for _, gate := range e.gateList() {
		if gate.CheckedAt.After(now) {
			return errors.New("runtimecontrol: state epoch recovery gate timestamp is outside the recovery window")
		}
	}
	if now.Sub(e.Gates.IngressClosed.CheckedAt.UTC()) > maximumStateEpochEvidenceAge {
		return errors.New("runtimecontrol: state epoch recovery ingress_closed gate is stale")
	}
	return nil
}

func (e StateEpochRecoveryEvidence) ValidateDurableBinding(
	transition StateEpochTransition,
	expectedOperatorRef string,
) error {
	if err := e.ValidateShape(); err != nil {
		return err
	}
	if e.Status != StateEpochRecoveryEvidenceApproved {
		return errors.New("runtimecontrol: state epoch recovery evidence is not approved")
	}
	if e.OperatorRef != expectedOperatorRef {
		return errors.New("runtimecontrol: state epoch recovery operator reference changed")
	}
	if transition.RecoveryID == nil || transition.OldRevision == nil ||
		e.RecoveryID != *transition.RecoveryID || e.CurrentRevision != *transition.OldRevision ||
		e.Reason != transition.Reason || !e.DetectedAt.Equal(transition.DetectedAt) ||
		!e.NotBefore.Equal(transition.NotBefore) {
		return errors.New("runtimecontrol: state epoch recovery evidence identity does not match transition")
	}
	if e.RecordedAt.Before(transition.DetectedAt) {
		return errors.New("runtimecontrol: state epoch recovery recorded_at is outside the recovery window")
	}
	for _, gate := range e.gateList() {
		checkedAt := gate.CheckedAt.UTC()
		if checkedAt.Before(transition.DetectedAt) || checkedAt.After(e.RecordedAt) {
			return errors.New("runtimecontrol: state epoch recovery gate timestamp is outside the recovery window")
		}
	}
	if e.Gates.Window.CheckedAt.Before(transition.NotBefore) {
		return errors.New("runtimecontrol: state epoch recovery window gate predates not_before")
	}
	return nil
}

func (e StateEpochRecoveryEvidence) Marshal() (json.RawMessage, error) {
	if err := e.ValidateShape(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("runtimecontrol: encode state epoch recovery evidence: %w", err)
	}
	return raw, nil
}

func (e StateEpochRecoveryEvidence) gateList() []StateEpochRecoveryGate {
	return []StateEpochRecoveryGate{
		e.Gates.IngressClosed,
		e.Gates.Drain,
		e.Gates.Window,
		e.Gates.BreakerCooldown,
		e.Gates.Permission,
		e.Gates.Control,
		e.Gates.OfflineScripts,
		e.Gates.MaintenanceProbe,
	}
}

func normalizeRecoveryGateTimes(gates *StateEpochRecoveryGates) {
	for _, gate := range []*StateEpochRecoveryGate{
		&gates.IngressClosed,
		&gates.Drain,
		&gates.Window,
		&gates.BreakerCooldown,
		&gates.Permission,
		&gates.Control,
		&gates.OfflineScripts,
		&gates.MaintenanceProbe,
	} {
		if gate.CheckedAt != nil {
			checkedAt := gate.CheckedAt.UTC()
			gate.CheckedAt = &checkedAt
		}
	}
}

func validOperatorReference(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		if i > 0 && strings.ContainsRune("._:/@-", r) {
			continue
		}
		return false
	}
	return true
}

func validRecoveryID(value string) bool {
	return validOperatorReference(value)
}

func validSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func ensureRecoveryEvidenceJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("runtimecontrol: state epoch recovery evidence contains trailing JSON value")
	}
	return fmt.Errorf("runtimecontrol: decode trailing state epoch recovery evidence data: %w", err)
}
