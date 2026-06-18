package calibration

import (
	"context"
	"time"

	"github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

// autoCalibrateActor 是自动校正写入 model_capabilities / suggestions 的来源标识（updated_by / decided_by）。
// admin 据此识别「自动加的」并可撤销；worker 据此保证「manual 永远优先」（不碰非本标识的声明行）。
const autoCalibrateActor = "auto_calibrate"

// AttemptEvidence 是一条成功尝试的能力使用与强证据线索（聚合输入）。
type AttemptEvidence struct {
	AttemptID            int64
	ModelID              int64
	ChannelID            int64
	FinishClass          string
	RequiredCapabilities []capability.Key
	CacheReadTokens      int64
	ReasoningTokens      int64
}

// ModelInfo 是启用模型及其自动校正档位。
type ModelInfo struct {
	ID      int64
	ModelID string
	Mode    Mode
}

// Store 提供能力自动校正所需的增量扫描、rollup 聚合、上下文读取与决策落库能力。
type Store interface {
	Watermark(ctx context.Context) (int64, error)
	SetWatermark(ctx context.Context, attemptID int64) error
	ScanSucceeded(ctx context.Context, afterAttemptID int64, since time.Time, maxRows int32) ([]AttemptEvidence, error)

	IncrementObservation(ctx context.Context, modelID, channelID int64, key capability.Key, successDelta, evidenceDelta int64) error
	ListObservations(ctx context.Context) ([]Observation, error)

	ListModels(ctx context.Context) ([]ModelInfo, error)
	EnabledChannelCounts(ctx context.Context) (map[int64]int, error)
	DeclaredKeys(ctx context.Context) (map[int64]map[capability.Key]struct{}, error)
	DismissedKeys(ctx context.Context) (map[int64]map[capability.Key]struct{}, error)

	ApplyAutoCapability(ctx context.Context, modelID int64, key capability.Key, rationale []byte) error
	RecordSuggestion(ctx context.Context, modelID int64, key capability.Key, level capability.SupportLevel, kind EvidenceKind, rationale []byte) error
}

type sqlcStore struct {
	queries *sqlc.Queries
}

// NewStore 创建 sqlc 支撑的能力自动校正数据访问层。
func NewStore(queries *sqlc.Queries) Store {
	return &sqlcStore{queries: queries}
}

func (s *sqlcStore) Watermark(ctx context.Context) (int64, error) {
	id, err := s.queries.GetCapabilityCalibrationWatermark(ctx)
	if err != nil {
		return 0, storeFailure(err, "get calibration watermark")
	}
	return id, nil
}

func (s *sqlcStore) SetWatermark(ctx context.Context, attemptID int64) error {
	if err := s.queries.SetCapabilityCalibrationWatermark(ctx, attemptID); err != nil {
		return storeFailure(err, "set calibration watermark")
	}
	return nil
}

func (s *sqlcStore) ScanSucceeded(ctx context.Context, afterAttemptID int64, since time.Time, maxRows int32) ([]AttemptEvidence, error) {
	rows, err := s.queries.ScanSucceededAttemptsForCalibration(ctx, sqlc.ScanSucceededAttemptsForCalibrationParams{
		AfterAttemptID: afterAttemptID,
		Since:          pgtype.Timestamptz{Time: since, Valid: true},
		MaxRows:        maxRows,
	})
	if err != nil {
		return nil, storeFailure(err, "scan succeeded attempts for calibration")
	}

	out := make([]AttemptEvidence, 0, len(rows))
	for _, row := range rows {
		keys := make([]capability.Key, 0, len(row.RequiredCapabilities))
		for _, k := range row.RequiredCapabilities {
			keys = append(keys, capability.Key(k))
		}
		finishClass := ""
		if row.FinishClass.Valid {
			finishClass = row.FinishClass.String
		}
		out = append(out, AttemptEvidence{
			AttemptID:            row.AttemptID,
			ModelID:              row.ModelID,
			ChannelID:            row.ChannelID,
			FinishClass:          finishClass,
			RequiredCapabilities: keys,
			CacheReadTokens:      row.CacheReadInputTokens,
			ReasoningTokens:      row.ReasoningOutputTokens,
		})
	}
	return out, nil
}

func (s *sqlcStore) IncrementObservation(ctx context.Context, modelID, channelID int64, key capability.Key, successDelta, evidenceDelta int64) error {
	err := s.queries.IncrementModelCapabilityObservation(ctx, sqlc.IncrementModelCapabilityObservationParams{
		ModelID:       modelID,
		ChannelID:     channelID,
		CapabilityKey: string(key),
		SuccessDelta:  successDelta,
		EvidenceDelta: evidenceDelta,
	})
	if err != nil {
		return storeFailure(err, "increment capability observation")
	}
	return nil
}

func (s *sqlcStore) ListObservations(ctx context.Context) ([]Observation, error) {
	rows, err := s.queries.ListModelCapabilityObservations(ctx)
	if err != nil {
		return nil, storeFailure(err, "list capability observations")
	}

	out := make([]Observation, 0, len(rows))
	for _, row := range rows {
		out = append(out, Observation{
			ModelID:   row.ModelID,
			ChannelID: row.ChannelID,
			Key:       capability.Key(row.CapabilityKey),
			Success:   row.SuccessCount,
			Evidence:  row.EvidenceCount,
			LastSeen:  row.LastSeenAt.Time,
		})
	}
	return out, nil
}

func (s *sqlcStore) ListModels(ctx context.Context) ([]ModelInfo, error) {
	rows, err := s.queries.ListModelsForCalibration(ctx)
	if err != nil {
		return nil, storeFailure(err, "list models for calibration")
	}

	out := make([]ModelInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, ModelInfo{
			ID:      row.ID,
			ModelID: row.ModelID,
			Mode:    Mode(row.CapabilityAutocalibrate),
		})
	}
	return out, nil
}

func (s *sqlcStore) EnabledChannelCounts(ctx context.Context) (map[int64]int, error) {
	rows, err := s.queries.ListEnabledChannelModelCounts(ctx)
	if err != nil {
		return nil, storeFailure(err, "list enabled channel model counts")
	}

	out := make(map[int64]int, len(rows))
	for _, row := range rows {
		out[row.ModelID] = int(row.ChannelCount)
	}
	return out, nil
}

func (s *sqlcStore) DeclaredKeys(ctx context.Context) (map[int64]map[capability.Key]struct{}, error) {
	rows, err := s.queries.ListAllModelCapabilityKeys(ctx)
	if err != nil {
		return nil, storeFailure(err, "list all model capability keys")
	}

	out := make(map[int64]map[capability.Key]struct{})
	for _, row := range rows {
		set := out[row.ModelID]
		if set == nil {
			set = make(map[capability.Key]struct{})
			out[row.ModelID] = set
		}
		set[capability.Key(row.CapabilityKey)] = struct{}{}
	}
	return out, nil
}

func (s *sqlcStore) DismissedKeys(ctx context.Context) (map[int64]map[capability.Key]struct{}, error) {
	rows, err := s.queries.ListModelCapabilitySuggestionsByStatus(ctx, "dismissed")
	if err != nil {
		return nil, storeFailure(err, "list dismissed capability suggestions")
	}

	out := make(map[int64]map[capability.Key]struct{})
	for _, row := range rows {
		set := out[row.ModelID]
		if set == nil {
			set = make(map[capability.Key]struct{})
			out[row.ModelID] = set
		}
		set[capability.Key(row.CapabilityKey)] = struct{}{}
	}
	return out, nil
}

func (s *sqlcStore) ApplyAutoCapability(ctx context.Context, modelID int64, key capability.Key, rationale []byte) error {
	if !capability.IsRegisteredKey(key) {
		return capabilityInvalidKey(key)
	}

	if _, err := s.queries.UpsertModelCapability(ctx, sqlc.UpsertModelCapabilityParams{
		ModelID:       modelID,
		CapabilityKey: string(key),
		SupportLevel:  string(capability.SupportLevelFull),
		Limits:        nil,
		UpdatedBy:     pgtype.Text{String: autoCalibrateActor, Valid: true},
	}); err != nil {
		return storeFailure(err, "auto upsert model capability")
	}

	if _, err := s.queries.UpsertModelCapabilitySuggestion(ctx, sqlc.UpsertModelCapabilitySuggestionParams{
		ModelID:        modelID,
		CapabilityKey:  string(key),
		SuggestedLevel: string(capability.SupportLevelFull),
		EvidenceKind:   string(EvidenceStrong),
		Rationale:      rationaleOrEmpty(rationale),
		Status:         "accepted",
		DecidedAt:      pgtype.Timestamptz{Time: time.Now(), Valid: true},
		DecidedBy:      pgtype.Text{String: autoCalibrateActor, Valid: true},
	}); err != nil {
		return storeFailure(err, "record accepted suggestion")
	}
	return nil
}

func (s *sqlcStore) RecordSuggestion(ctx context.Context, modelID int64, key capability.Key, level capability.SupportLevel, kind EvidenceKind, rationale []byte) error {
	if _, err := s.queries.UpsertModelCapabilitySuggestion(ctx, sqlc.UpsertModelCapabilitySuggestionParams{
		ModelID:        modelID,
		CapabilityKey:  string(key),
		SuggestedLevel: string(level),
		EvidenceKind:   string(kind),
		Rationale:      rationaleOrEmpty(rationale),
		Status:         "pending",
		DecidedAt:      pgtype.Timestamptz{Valid: false},
		DecidedBy:      pgtype.Text{Valid: false},
	}); err != nil {
		return storeFailure(err, "record pending suggestion")
	}
	return nil
}

// rationaleOrEmpty 兜底空 rationale 为合法 JSON（NOT NULL JSONB 列）。
func rationaleOrEmpty(rationale []byte) []byte {
	if len(rationale) == 0 {
		return []byte("{}")
	}
	return rationale
}

func storeFailure(err error, message string) error {
	return failure.Wrap(failure.CodeCapabilityStoreFailed, err, failure.WithMessage(message))
}

func capabilityInvalidKey(key capability.Key) error {
	return failure.New(
		failure.CodeCapabilityInvalidKey,
		failure.WithMessage("capability key is not registered"),
		failure.WithField("capability_key", string(key)),
	)
}
