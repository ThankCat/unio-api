package runtimecontrol

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

const maximumStateEpochRecoveryDelay = 24 * time.Hour

// StateEpochTransition 是 runtime_state_epoch operation 中不可变的 envelope。
// PostgreSQL 当前 state/activated_at、expected marker 和 recovery evidence 都不纳入它。
type StateEpochTransition struct {
	RecoveryID         *string          `json:"recovery_id"`
	OldEpoch           *string          `json:"old_epoch"`
	OldRevision        *int64           `json:"old_revision"`
	NewEpoch           string           `json:"new_epoch"`
	NewRevision        int64            `json:"new_revision"`
	Reason             StateEpochReason `json:"reason"`
	StateLossConfirmed bool             `json:"state_loss_confirmed"`
	DetectedAt         time.Time        `json:"detected_at"`
	NotBefore          time.Time        `json:"not_before"`
}

func NewBootstrapStateEpochTransition(now time.Time) (StateEpochTransition, error) {
	epoch, err := NewRecoveringStateEpoch(StateEpochReasonBootstrap)
	if err != nil {
		return StateEpochTransition{}, err
	}
	now = now.UTC()
	return StateEpochTransition{
		RecoveryID:         nil,
		NewEpoch:           epoch.Epoch,
		NewRevision:        1,
		Reason:             StateEpochReasonBootstrap,
		StateLossConfirmed: true,
		DetectedAt:         now,
		NotBefore:          now,
	}, nil
}

func NewStateEpochRecoveryTransition(
	old StateEpoch,
	oldRevision int64,
	recoveryID string,
	reason StateEpochReason,
	stateLossConfirmed bool,
	detectedAt time.Time,
	notBefore time.Time,
) (StateEpochTransition, error) {
	if old.State != StateEpochReady || old.ActivatedAt == nil || oldRevision < 1 {
		return StateEpochTransition{}, errors.New("runtimecontrol: recovery transition requires a ready old epoch")
	}
	if reason != StateEpochReasonStateLoss && reason != StateEpochReasonRestore {
		return StateEpochTransition{}, errors.New("runtimecontrol: recovery transition reason must be state_loss or restore")
	}
	newEpoch, err := NewRecoveringStateEpoch(reason)
	if err != nil {
		return StateEpochTransition{}, err
	}
	oldEpoch := old.Epoch
	transition := StateEpochTransition{
		RecoveryID:         &recoveryID,
		OldEpoch:           &oldEpoch,
		OldRevision:        &oldRevision,
		NewEpoch:           newEpoch.Epoch,
		NewRevision:        oldRevision + 1,
		Reason:             reason,
		StateLossConfirmed: stateLossConfirmed,
		DetectedAt:         detectedAt.UTC(),
		NotBefore:          notBefore.UTC(),
	}
	if err := transition.Validate(); err != nil {
		return StateEpochTransition{}, err
	}
	return transition, nil
}

func DecodeStateEpochTransition(raw []byte) (StateEpochTransition, error) {
	var transition StateEpochTransition
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&transition); err != nil {
		return StateEpochTransition{}, fmt.Errorf("runtimecontrol: decode state epoch transition: %w", err)
	}
	if err := ensureTransitionJSONEOF(decoder); err != nil {
		return StateEpochTransition{}, err
	}
	if err := transition.Validate(); err != nil {
		return StateEpochTransition{}, err
	}
	return transition, nil
}

func (t StateEpochTransition) Validate() error {
	if t.DetectedAt.IsZero() || t.NotBefore.IsZero() {
		return errors.New("runtimecontrol: state epoch transition requires detected_at and not_before")
	}
	if t.NotBefore.Before(t.DetectedAt) {
		return errors.New("runtimecontrol: state epoch transition not_before precedes detected_at")
	}
	if t.NotBefore.After(t.DetectedAt.Add(maximumStateEpochRecoveryDelay)) {
		return errors.New("runtimecontrol: state epoch transition not_before exceeds 24 hours after detected_at")
	}
	if !t.StateLossConfirmed {
		return errors.New("runtimecontrol: state epoch transition requires confirmed state loss")
	}
	if err := (StateEpoch{Epoch: t.NewEpoch, State: StateEpochRecovering, Reason: t.Reason}).Validate(); err != nil {
		return fmt.Errorf("runtimecontrol: invalid transition new epoch: %w", err)
	}

	switch t.Reason {
	case StateEpochReasonBootstrap:
		if t.RecoveryID != nil || t.OldEpoch != nil || t.OldRevision != nil || t.NewRevision != 1 {
			return errors.New("runtimecontrol: bootstrap transition requires nil old identity and revision 1")
		}
	case StateEpochReasonStateLoss, StateEpochReasonRestore:
		if t.RecoveryID == nil || !validRecoveryID(*t.RecoveryID) {
			return errors.New("runtimecontrol: non-bootstrap transition requires a valid recovery_id")
		}
		if t.OldEpoch == nil || t.OldRevision == nil || *t.OldRevision < 1 {
			return errors.New("runtimecontrol: non-bootstrap transition requires old identity")
		}
		if err := (StateEpoch{Epoch: *t.OldEpoch, State: StateEpochRecovering, Reason: t.Reason}).Validate(); err != nil {
			return fmt.Errorf("runtimecontrol: invalid transition old epoch: %w", err)
		}
		if t.NewRevision != *t.OldRevision+1 || t.NewEpoch == *t.OldEpoch {
			return errors.New("runtimecontrol: non-bootstrap transition requires a new epoch and revision old+1")
		}
	default:
		return fmt.Errorf("runtimecontrol: invalid state epoch transition reason %q", t.Reason)
	}
	return nil
}

func (t StateEpochTransition) Marshal() (json.RawMessage, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("runtimecontrol: encode state epoch transition: %w", err)
	}
	return raw, nil
}

func (t StateEpochTransition) OldIdentity() (string, int64) {
	if t.OldEpoch == nil || t.OldRevision == nil {
		return "", 0
	}
	return *t.OldEpoch, *t.OldRevision
}

func newStateEpochOperationToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("runtimecontrol: generate state epoch operation token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func ensureTransitionJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("runtimecontrol: state epoch transition contains trailing JSON value")
	}
	return fmt.Errorf("runtimecontrol: decode trailing state epoch transition data: %w", err)
}
