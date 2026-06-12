package capability

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Model 是能力架构 Layer 1 的模型元数据，承载目录展示与按模型预授权所需事实。
//
// 价格基线字段仅用于 catalog 展示，绝不用于计费（计费以 prices/channel_cost_prices 为准）。
type Model struct {
	ID                       int64
	ModelID                  string
	DisplayName              string
	OwnedBy                  string
	Status                   string
	CanonicalID              *string
	Lab                      *string
	ContextWindowTokens      *int64
	MaxOutputTokens          *int64
	InputPriceUSDPerMTokens  *string
	OutputPriceUSDPerMTokens *string
	ReleaseDate              *time.Time
	Source                   Source
	RemovedUpstreamAt        *time.Time
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// ModelCapability 是能力架构 Layer 2 的「模型 × 能力」声明。
type ModelCapability struct {
	ModelID      int64
	Key          Key
	SupportLevel SupportLevel
	Limits       json.RawMessage
	Source       Source
	CreatedAt    time.Time
	UpdatedAt    time.Time
	UpdatedBy    *string
}

// ChannelOverride 是能力架构 Layer 3 的渠道收紧策略（只能做减法）。
type ChannelOverride struct {
	ChannelID    int64
	Key          Key
	SupportLevel SupportLevel
	Limits       json.RawMessage
	Reason       *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	UpdatedBy    *string
}

// SyncJobStatus 是 models.dev 能力同步任务的状态机取值。
type SyncJobStatus string

const (
	// SyncJobStatusPending 表示任务已创建但未开始。
	SyncJobStatusPending SyncJobStatus = "pending"

	// SyncJobStatusRunning 表示任务执行中。
	SyncJobStatusRunning SyncJobStatus = "running"

	// SyncJobStatusSucceeded 表示任务成功结束。
	SyncJobStatusSucceeded SyncJobStatus = "succeeded"

	// SyncJobStatusFailed 表示任务失败结束。
	SyncJobStatusFailed SyncJobStatus = "failed"
)

// SyncJob 是一次能力同步任务的审计记录。
type SyncJob struct {
	ID         int64
	Source     Source
	Status     SyncJobStatus
	StartedAt  *time.Time
	FinishedAt *time.Time
	Stats      json.RawMessage
	ErrorText  *string
	CreatedAt  time.Time
}

// UpsertModelCapabilityParams 是写入模型能力声明的入参。
type UpsertModelCapabilityParams struct {
	ModelID      int64
	Key          Key
	SupportLevel SupportLevel
	Limits       json.RawMessage
	Source       Source
	UpdatedBy    *string
}

// UpsertChannelOverrideParams 是写入渠道能力收紧策略的入参。
type UpsertChannelOverrideParams struct {
	ChannelID    int64
	Key          Key
	SupportLevel SupportLevel
	Limits       json.RawMessage
	Reason       *string
	UpdatedBy    *string
}

// Store 提供能力架构三层数据与同步任务的读写能力，core 类型不暴露 sqlc row。
type Store interface {
	LookupModelByID(ctx context.Context, id int64) (Model, error)
	LookupModelByModelID(ctx context.Context, modelID string) (Model, error)

	ListModelCapabilities(ctx context.Context, modelID int64) ([]ModelCapability, error)
	ListModelsByCapability(ctx context.Context, key Key) ([]ModelCapability, error)
	UpsertModelCapability(ctx context.Context, params UpsertModelCapabilityParams) (ModelCapability, error)
	DeleteModelCapability(ctx context.Context, modelID int64, key Key) error

	ListChannelOverrides(ctx context.Context, channelID int64) ([]ChannelOverride, error)
	UpsertChannelOverride(ctx context.Context, params UpsertChannelOverrideParams) (ChannelOverride, error)
	DeleteChannelOverride(ctx context.Context, channelID int64, key Key) error

	CreateSyncJob(ctx context.Context, source Source) (SyncJob, error)
	MarkSyncJobRunning(ctx context.Context, id int64) (SyncJob, error)
	MarkSyncJobSucceeded(ctx context.Context, id int64, stats json.RawMessage) (SyncJob, error)
	MarkSyncJobFailed(ctx context.Context, id int64, errorText string) (SyncJob, error)
	GetLatestSyncJob(ctx context.Context, source Source) (SyncJob, error)
	ListSyncJobs(ctx context.Context, limit int32) ([]SyncJob, error)
}

// sqlcStore 是 Store 的 sqlc 实现。
type sqlcStore struct {
	queries *sqlc.Queries
}

// NewStore 创建 sqlc 支撑的能力数据访问层。
func NewStore(queries *sqlc.Queries) Store {
	return &sqlcStore{queries: queries}
}

func (s *sqlcStore) LookupModelByID(ctx context.Context, id int64) (Model, error) {
	row, err := s.queries.LookupModelByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Model{}, capabilityNotFound("lookup model by id")
		}

		return Model{}, capabilityStoreFailure(err, "lookup model by id")
	}

	return modelFromSQLC(row), nil
}

func (s *sqlcStore) LookupModelByModelID(ctx context.Context, modelID string) (Model, error) {
	row, err := s.queries.LookupModelByModelID(ctx, modelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Model{}, capabilityNotFound("lookup model by model id")
		}

		return Model{}, capabilityStoreFailure(err, "lookup model by model id")
	}

	return modelFromSQLC(row), nil
}

func (s *sqlcStore) ListModelCapabilities(ctx context.Context, modelID int64) ([]ModelCapability, error) {
	rows, err := s.queries.ListModelCapabilities(ctx, modelID)
	if err != nil {
		return nil, capabilityStoreFailure(err, "list model capabilities")
	}

	items := make([]ModelCapability, 0, len(rows))
	for _, row := range rows {
		items = append(items, modelCapabilityFromSQLC(row))
	}

	return items, nil
}

func (s *sqlcStore) ListModelsByCapability(ctx context.Context, key Key) ([]ModelCapability, error) {
	rows, err := s.queries.ListModelsByCapability(ctx, string(key))
	if err != nil {
		return nil, capabilityStoreFailure(err, "list models by capability")
	}

	items := make([]ModelCapability, 0, len(rows))
	for _, row := range rows {
		items = append(items, modelCapabilityFromSQLC(row))
	}

	return items, nil
}

func (s *sqlcStore) UpsertModelCapability(ctx context.Context, params UpsertModelCapabilityParams) (ModelCapability, error) {
	if !IsRegisteredKey(params.Key) {
		return ModelCapability{}, capabilityInvalidKey(params.Key)
	}
	if !IsValidSupportLevel(params.SupportLevel) {
		return ModelCapability{}, capabilityInvalidSupportLevel(params.SupportLevel)
	}
	if !IsValidCapabilitySource(params.Source) {
		return ModelCapability{}, capabilityInvalidSource(params.Source)
	}

	row, err := s.queries.UpsertModelCapability(ctx, sqlc.UpsertModelCapabilityParams{
		ModelID:       params.ModelID,
		CapabilityKey: string(params.Key),
		SupportLevel:  string(params.SupportLevel),
		Limits:        limitsToBytes(params.Limits),
		Source:        string(params.Source),
		UpdatedBy:     optionalText(params.UpdatedBy),
	})
	if err != nil {
		return ModelCapability{}, capabilityStoreFailure(err, "upsert model capability")
	}

	return modelCapabilityFromSQLC(row), nil
}

func (s *sqlcStore) DeleteModelCapability(ctx context.Context, modelID int64, key Key) error {
	err := s.queries.DeleteModelCapability(ctx, sqlc.DeleteModelCapabilityParams{
		ModelID:       modelID,
		CapabilityKey: string(key),
	})
	if err != nil {
		return capabilityStoreFailure(err, "delete model capability")
	}

	return nil
}

func (s *sqlcStore) ListChannelOverrides(ctx context.Context, channelID int64) ([]ChannelOverride, error) {
	rows, err := s.queries.ListChannelOverrides(ctx, channelID)
	if err != nil {
		return nil, capabilityStoreFailure(err, "list channel overrides")
	}

	items := make([]ChannelOverride, 0, len(rows))
	for _, row := range rows {
		items = append(items, channelOverrideFromSQLC(row))
	}

	return items, nil
}

func (s *sqlcStore) UpsertChannelOverride(ctx context.Context, params UpsertChannelOverrideParams) (ChannelOverride, error) {
	if !IsRegisteredKey(params.Key) {
		return ChannelOverride{}, capabilityInvalidKey(params.Key)
	}
	if !IsValidChannelOverrideLevel(params.SupportLevel) {
		return ChannelOverride{}, capabilityInvalidSupportLevel(params.SupportLevel)
	}

	row, err := s.queries.UpsertChannelOverride(ctx, sqlc.UpsertChannelOverrideParams{
		ChannelID:     params.ChannelID,
		CapabilityKey: string(params.Key),
		SupportLevel:  string(params.SupportLevel),
		Limits:        limitsToBytes(params.Limits),
		Reason:        optionalText(params.Reason),
		UpdatedBy:     optionalText(params.UpdatedBy),
	})
	if err != nil {
		return ChannelOverride{}, capabilityStoreFailure(err, "upsert channel override")
	}

	return channelOverrideFromSQLC(row), nil
}

func (s *sqlcStore) DeleteChannelOverride(ctx context.Context, channelID int64, key Key) error {
	err := s.queries.DeleteChannelOverride(ctx, sqlc.DeleteChannelOverrideParams{
		ChannelID:     channelID,
		CapabilityKey: string(key),
	})
	if err != nil {
		return capabilityStoreFailure(err, "delete channel override")
	}

	return nil
}

func (s *sqlcStore) CreateSyncJob(ctx context.Context, source Source) (SyncJob, error) {
	if !IsValidSyncJobSource(source) {
		return SyncJob{}, capabilityInvalidSource(source)
	}

	row, err := s.queries.CreateSyncJob(ctx, string(source))
	if err != nil {
		return SyncJob{}, capabilityStoreFailure(err, "create sync job")
	}

	return syncJobFromSQLC(row), nil
}

func (s *sqlcStore) MarkSyncJobRunning(ctx context.Context, id int64) (SyncJob, error) {
	row, err := s.queries.MarkSyncJobRunning(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SyncJob{}, capabilityNotFound("mark sync job running")
		}

		return SyncJob{}, capabilityStoreFailure(err, "mark sync job running")
	}

	return syncJobFromSQLC(row), nil
}

func (s *sqlcStore) MarkSyncJobSucceeded(ctx context.Context, id int64, stats json.RawMessage) (SyncJob, error) {
	row, err := s.queries.MarkSyncJobSucceeded(ctx, sqlc.MarkSyncJobSucceededParams{
		StatsJson: limitsToBytes(stats),
		ID:        id,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SyncJob{}, capabilityNotFound("mark sync job succeeded")
		}

		return SyncJob{}, capabilityStoreFailure(err, "mark sync job succeeded")
	}

	return syncJobFromSQLC(row), nil
}

func (s *sqlcStore) MarkSyncJobFailed(ctx context.Context, id int64, errorText string) (SyncJob, error) {
	row, err := s.queries.MarkSyncJobFailed(ctx, sqlc.MarkSyncJobFailedParams{
		ErrorText: nullableText(errorText),
		ID:        id,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SyncJob{}, capabilityNotFound("mark sync job failed")
		}

		return SyncJob{}, capabilityStoreFailure(err, "mark sync job failed")
	}

	return syncJobFromSQLC(row), nil
}

func (s *sqlcStore) GetLatestSyncJob(ctx context.Context, source Source) (SyncJob, error) {
	row, err := s.queries.GetLatestSyncJob(ctx, string(source))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SyncJob{}, capabilityNotFound("get latest sync job")
		}

		return SyncJob{}, capabilityStoreFailure(err, "get latest sync job")
	}

	return syncJobFromSQLC(row), nil
}

func (s *sqlcStore) ListSyncJobs(ctx context.Context, limit int32) ([]SyncJob, error) {
	rows, err := s.queries.ListSyncJobs(ctx, limit)
	if err != nil {
		return nil, capabilityStoreFailure(err, "list sync jobs")
	}

	items := make([]SyncJob, 0, len(rows))
	for _, row := range rows {
		items = append(items, syncJobFromSQLC(row))
	}

	return items, nil
}

// modelFromSQLC 将 sqlc model row 转成能力领域 Model。
func modelFromSQLC(row sqlc.Model) Model {
	return Model{
		ID:                       row.ID,
		ModelID:                  row.ModelID,
		DisplayName:              row.DisplayName,
		OwnedBy:                  row.OwnedBy,
		Status:                   row.Status,
		CanonicalID:              textPtr(row.CanonicalID),
		Lab:                      textPtr(row.Lab),
		ContextWindowTokens:      int64Ptr(row.ContextWindowTokens),
		MaxOutputTokens:          int64Ptr(row.MaxOutputTokens),
		InputPriceUSDPerMTokens:  numericDecimalString(row.InputPriceUsdPerMillionTokens),
		OutputPriceUSDPerMTokens: numericDecimalString(row.OutputPriceUsdPerMillionTokens),
		ReleaseDate:              datePtr(row.ReleaseDate),
		Source:                   Source(row.Source),
		RemovedUpstreamAt:        timePtr(row.RemovedUpstreamAt),
		CreatedAt:                row.CreatedAt.Time,
		UpdatedAt:                row.UpdatedAt.Time,
	}
}

// modelCapabilityFromSQLC 将 sqlc 行转成领域 ModelCapability。
func modelCapabilityFromSQLC(row sqlc.ModelCapability) ModelCapability {
	return ModelCapability{
		ModelID:      row.ModelID,
		Key:          Key(row.CapabilityKey),
		SupportLevel: SupportLevel(row.SupportLevel),
		Limits:       limitsFromBytes(row.Limits),
		Source:       Source(row.Source),
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
		UpdatedBy:    textPtr(row.UpdatedBy),
	}
}

// channelOverrideFromSQLC 将 sqlc 行转成领域 ChannelOverride。
func channelOverrideFromSQLC(row sqlc.ChannelCapabilityOverride) ChannelOverride {
	return ChannelOverride{
		ChannelID:    row.ChannelID,
		Key:          Key(row.CapabilityKey),
		SupportLevel: SupportLevel(row.SupportLevel),
		Limits:       limitsFromBytes(row.Limits),
		Reason:       textPtr(row.Reason),
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
		UpdatedBy:    textPtr(row.UpdatedBy),
	}
}

// syncJobFromSQLC 将 sqlc 行转成领域 SyncJob。
func syncJobFromSQLC(row sqlc.ModelCapabilitySyncJob) SyncJob {
	return SyncJob{
		ID:         row.ID,
		Source:     Source(row.Source),
		Status:     SyncJobStatus(row.Status),
		StartedAt:  timePtr(row.StartedAt),
		FinishedAt: timePtr(row.FinishedAt),
		Stats:      limitsFromBytes(row.StatsJson),
		ErrorText:  textPtr(row.ErrorText),
		CreatedAt:  row.CreatedAt.Time,
	}
}

func capabilityStoreFailure(err error, message string) error {
	return failure.Wrap(failure.CodeCapabilityStoreFailed, err, failure.WithMessage(message))
}

func capabilityNotFound(message string) error {
	return failure.New(failure.CodeCapabilityNotFound, failure.WithMessage(message))
}

func capabilityInvalidKey(key Key) error {
	return failure.New(
		failure.CodeCapabilityInvalidKey,
		failure.WithMessage("capability key is not registered"),
		failure.WithField("capability_key", string(key)),
	)
}

func capabilityInvalidSupportLevel(level SupportLevel) error {
	return failure.New(
		failure.CodeCapabilityInvalidSupportLevel,
		failure.WithMessage("capability support level is not allowed"),
		failure.WithField("support_level", string(level)),
	)
}

func capabilityInvalidSource(source Source) error {
	return failure.New(
		failure.CodeCapabilityInvalidSource,
		failure.WithMessage("capability source is invalid"),
		failure.WithField("source", string(source)),
	)
}

// limitsToBytes 把领域 JSON 入参转成可空 JSONB 写入值，空内容写 NULL。
func limitsToBytes(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}

	return []byte(raw)
}

// limitsFromBytes 把 JSONB 列读成领域 JSON，NULL 读成 nil。
func limitsFromBytes(value []byte) json.RawMessage {
	if len(value) == 0 {
		return nil
	}

	out := make(json.RawMessage, len(value))
	copy(out, value)
	return out
}

// optionalText 把可选字符串转成 pgtype.Text，nil 写 NULL。
func optionalText(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{Valid: false}
	}

	return pgtype.Text{String: *value, Valid: true}
}

// nullableText 把空字符串写成 NULL，避免保存无意义空值。
func nullableText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{Valid: false}
	}

	return pgtype.Text{String: value, Valid: true}
}

// textPtr 把 pgtype.Text 转成可选字符串。
func textPtr(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}

	out := value.String
	return &out
}

// int64Ptr 把 pgtype.Int8 转成可选 int64。
func int64Ptr(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}

	out := value.Int64
	return &out
}

// timePtr 把 pgtype.Timestamptz 转成可选 time.Time。
func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}

	out := value.Time
	return &out
}

// datePtr 把 pgtype.Date 转成可选 time.Time。
func datePtr(value pgtype.Date) *time.Time {
	if !value.Valid {
		return nil
	}

	out := value.Time
	return &out
}

// numericDecimalString 把 NUMERIC 精确格式化为十进制字符串（不用 float），NULL/NaN/Inf 返回 nil。
func numericDecimalString(value pgtype.Numeric) *string {
	if !value.Valid || value.NaN || value.InfinityModifier != pgtype.Finite {
		return nil
	}
	if value.Int == nil {
		zero := "0"
		return &zero
	}

	negative := value.Int.Sign() < 0
	digits := new(big.Int).Abs(value.Int).String()
	exp := int(value.Exp)

	var formatted string
	switch {
	case exp == 0:
		formatted = digits
	case exp > 0:
		formatted = digits + strings.Repeat("0", exp)
	default:
		scale := -exp
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		point := len(digits) - scale
		formatted = digits[:point] + "." + digits[point:]
	}

	if negative {
		formatted = "-" + formatted
	}

	return &formatted
}
