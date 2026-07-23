package runtimecontrol

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const RuntimeStateEpochKey = "gateway.runtime_state_epoch"

type StateEpochState string

const (
	StateEpochRecovering StateEpochState = "recovering"
	StateEpochReady      StateEpochState = "ready"
)

type StateEpochReason string

const (
	StateEpochReasonBootstrap StateEpochReason = "bootstrap"
	StateEpochReasonStateLoss StateEpochReason = "state_loss"
	StateEpochReasonRestore   StateEpochReason = "restore"
)

// StateEpoch 是 PostgreSQL 保留行中的运行态完整性事实。Revision 由 app_settings 行单独保存。
type StateEpoch struct {
	Epoch       string           `json:"epoch"`
	State       StateEpochState  `json:"state"`
	Reason      StateEpochReason `json:"reason"`
	ActivatedAt *time.Time       `json:"activated_at"`
}

// NewRecoveringStateEpoch 生成一个 128-bit 随机 epoch，初始态固定为 recovering。
func NewRecoveringStateEpoch(reason StateEpochReason) (StateEpoch, error) {
	if !validStateEpochReason(reason) {
		return StateEpoch{}, fmt.Errorf("runtimecontrol: invalid state epoch reason %q", reason)
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return StateEpoch{}, fmt.Errorf("runtimecontrol: generate state epoch: %w", err)
	}
	return StateEpoch{
		Epoch:  hex.EncodeToString(raw[:]),
		State:  StateEpochRecovering,
		Reason: reason,
	}, nil
}

// DecodeStateEpoch 严格解码保留行，拒绝未知字段、非 128-bit epoch 和不合法状态组合。
func DecodeStateEpoch(raw []byte) (StateEpoch, error) {
	var value StateEpoch
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return StateEpoch{}, fmt.Errorf("runtimecontrol: decode state epoch: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return StateEpoch{}, err
	}
	if err := value.Validate(); err != nil {
		return StateEpoch{}, err
	}
	return value, nil
}

func (s StateEpoch) Validate() error {
	epoch := strings.ReplaceAll(s.Epoch, "-", "")
	decoded, err := hex.DecodeString(epoch)
	if err != nil || len(decoded) != 16 {
		return errors.New("runtimecontrol: state epoch must be a 128-bit hexadecimal value")
	}
	if s.State != StateEpochRecovering && s.State != StateEpochReady {
		return fmt.Errorf("runtimecontrol: invalid state epoch state %q", s.State)
	}
	if !validStateEpochReason(s.Reason) {
		return fmt.Errorf("runtimecontrol: invalid state epoch reason %q", s.Reason)
	}
	if s.State == StateEpochRecovering && s.ActivatedAt != nil {
		return errors.New("runtimecontrol: recovering state epoch must not have activated_at")
	}
	if s.State == StateEpochReady && s.ActivatedAt == nil {
		return errors.New("runtimecontrol: ready state epoch requires activated_at")
	}
	return nil
}

func (s StateEpoch) Marshal() (json.RawMessage, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("runtimecontrol: encode state epoch: %w", err)
	}
	return raw, nil
}

// ReadyAt 在不改变 epoch/reason 的前提下构造 ready 事实。
func (s StateEpoch) ReadyAt(activatedAt time.Time) (StateEpoch, error) {
	if s.State != StateEpochRecovering || s.ActivatedAt != nil {
		return StateEpoch{}, errors.New("runtimecontrol: only a recovering epoch can become ready")
	}
	activatedAt = activatedAt.UTC()
	s.State = StateEpochReady
	s.ActivatedAt = &activatedAt
	if err := s.Validate(); err != nil {
		return StateEpoch{}, err
	}
	return s, nil
}

func validStateEpochReason(reason StateEpochReason) bool {
	return reason == StateEpochReasonBootstrap || reason == StateEpochReasonStateLoss || reason == StateEpochReasonRestore
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("runtimecontrol: state epoch contains trailing JSON value")
	}
	return fmt.Errorf("runtimecontrol: decode trailing state epoch data: %w", err)
}
