package capability

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
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
	ContextWindowTokens      *int64
	MaxOutputTokens          *int64
	InputPriceUSDPerMTokens  *string
	OutputPriceUSDPerMTokens *string
	ReleaseDate              *time.Time
	Source                   Source
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// ModelCapability 是能力架构 Layer 2 的「模型 × 能力」声明（阶段 14 起去 source）。
type ModelCapability struct {
	ModelID      int64
	Key          Key
	SupportLevel SupportLevel
	Limits       json.RawMessage
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

// UpsertModelCapabilityParams 是写入模型能力声明的入参（阶段 14 起去 source）。
type UpsertModelCapabilityParams struct {
	ModelID      int64
	Key          Key
	SupportLevel SupportLevel
	Limits       json.RawMessage
	UpdatedBy    *string
}

// CapabilityKey 是能力 key 字典（capability_keys）的一行，合法 key 的真源（DEC-024）。
type CapabilityKey struct {
	Key           Key
	Domain        string
	DisplayName   string
	Description   string
	SortOrder     int32
	Deprecated    bool
	ProtocolScope ProtocolScope
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CreateCapabilityKeyParams 是新增能力 key 字典行的入参。
type CreateCapabilityKeyParams struct {
	Key           Key
	Domain        string
	DisplayName   string
	Description   string
	SortOrder     int32
	Deprecated    bool
	ProtocolScope ProtocolScope
}

// UpdateCapabilityKeyParams 是更新能力 key 字典元数据的入参（key 本身不可改）。
type UpdateCapabilityKeyParams struct {
	Key           Key
	Domain        string
	DisplayName   string
	Description   string
	SortOrder     int32
	Deprecated    bool
	ProtocolScope ProtocolScope
}

// Store 提供能力架构模型层数据与同步任务的读写能力，core 类型不暴露 sqlc row。
type Store interface {
	LookupModelByID(ctx context.Context, id int64) (Model, error)
	LookupModelByModelID(ctx context.Context, modelID string) (Model, error)

	ListModelCapabilities(ctx context.Context, modelID int64) ([]ModelCapability, error)
	ListModelsByCapability(ctx context.Context, key Key) ([]ModelCapability, error)
	UpsertModelCapability(ctx context.Context, params UpsertModelCapabilityParams) (ModelCapability, error)
	DeleteModelCapability(ctx context.Context, modelID int64, key Key) error

	ListCapabilityKeys(ctx context.Context) ([]CapabilityKey, error)
	GetCapabilityKey(ctx context.Context, key Key) (CapabilityKey, error)
	CreateCapabilityKey(ctx context.Context, params CreateCapabilityKeyParams) (CapabilityKey, error)
	UpdateCapabilityKey(ctx context.Context, params UpdateCapabilityKeyParams) (CapabilityKey, error)
	DeleteCapabilityKey(ctx context.Context, key Key) error
	CapabilityKeyExists(ctx context.Context, key Key) (bool, error)

	CreateSyncJob(ctx context.Context, source Source) (SyncJob, error)
	MarkSyncJobRunning(ctx context.Context, id int64) (SyncJob, error)
	MarkSyncJobSucceeded(ctx context.Context, id int64, stats json.RawMessage) (SyncJob, error)
	MarkSyncJobFailed(ctx context.Context, id int64, errorText string) (SyncJob, error)
	GetLatestSyncJob(ctx context.Context, source Source) (SyncJob, error)
	ListSyncJobs(ctx context.Context, arg sqlc.ListSyncJobsParams) ([]SyncJob, error)
	CountSyncJobs(ctx context.Context) (int64, error)
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
	if !IsValidSupportLevel(params.SupportLevel) {
		return ModelCapability{}, capabilityInvalidSupportLevel(params.SupportLevel)
	}

	row, err := s.queries.UpsertModelCapability(ctx, sqlc.UpsertModelCapabilityParams{
		ModelID:       params.ModelID,
		CapabilityKey: string(params.Key),
		SupportLevel:  string(params.SupportLevel),
		Limits:        limitsToBytes(params.Limits),
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

func (s *sqlcStore) ListCapabilityKeys(ctx context.Context) ([]CapabilityKey, error) {
	rows, err := s.queries.ListCapabilityKeys(ctx)
	if err != nil {
		return nil, capabilityStoreFailure(err, "list capability keys")
	}

	items := make([]CapabilityKey, 0, len(rows))
	for _, row := range rows {
		items = append(items, capabilityKeyFromSQLC(row))
	}

	return items, nil
}

func (s *sqlcStore) GetCapabilityKey(ctx context.Context, key Key) (CapabilityKey, error) {
	row, err := s.queries.GetCapabilityKey(ctx, string(key))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CapabilityKey{}, capabilityNotFound("capability key not found")
		}
		return CapabilityKey{}, capabilityStoreFailure(err, "get capability key")
	}
	return capabilityKeyFromGetRow(row), nil
}

func (s *sqlcStore) CreateCapabilityKey(ctx context.Context, params CreateCapabilityKeyParams) (CapabilityKey, error) {
	row, err := s.queries.CreateCapabilityKey(ctx, sqlc.CreateCapabilityKeyParams{
		Key:           string(params.Key),
		Domain:        params.Domain,
		DisplayName:   params.DisplayName,
		Description:   params.Description,
		SortOrder:     params.SortOrder,
		Deprecated:    params.Deprecated,
		ProtocolScope: string(params.ProtocolScope),
	})
	if err != nil {
		return CapabilityKey{}, capabilityStoreFailure(err, "create capability key")
	}
	return capabilityKeyFromCreateRow(row), nil
}

func (s *sqlcStore) UpdateCapabilityKey(ctx context.Context, params UpdateCapabilityKeyParams) (CapabilityKey, error) {
	row, err := s.queries.UpdateCapabilityKey(ctx, sqlc.UpdateCapabilityKeyParams{
		Key:           string(params.Key),
		Domain:        params.Domain,
		DisplayName:   params.DisplayName,
		Description:   params.Description,
		SortOrder:     params.SortOrder,
		Deprecated:    params.Deprecated,
		ProtocolScope: string(params.ProtocolScope),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CapabilityKey{}, capabilityNotFound("capability key not found")
		}
		return CapabilityKey{}, capabilityStoreFailure(err, "update capability key")
	}
	return capabilityKeyFromUpdateRow(row), nil
}

func (s *sqlcStore) DeleteCapabilityKey(ctx context.Context, key Key) error {
	err := s.queries.DeleteCapabilityKey(ctx, string(key))
	if err != nil {
		return capabilityStoreFailure(err, "delete capability key")
	}
	return nil
}

func (s *sqlcStore) CapabilityKeyExists(ctx context.Context, key Key) (bool, error) {
	exists, err := s.queries.CapabilityKeyExists(ctx, string(key))
	if err != nil {
		return false, capabilityStoreFailure(err, "capability key exists")
	}

	return exists, nil
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

func (s *sqlcStore) ListSyncJobs(ctx context.Context, arg sqlc.ListSyncJobsParams) ([]SyncJob, error) {
	rows, err := s.queries.ListSyncJobs(ctx, arg)
	if err != nil {
		return nil, capabilityStoreFailure(err, "list sync jobs")
	}

	items := make([]SyncJob, 0, len(rows))
	for _, row := range rows {
		items = append(items, syncJobFromSQLC(row))
	}

	return items, nil
}

func (s *sqlcStore) CountSyncJobs(ctx context.Context) (int64, error) {
	total, err := s.queries.CountSyncJobs(ctx)
	if err != nil {
		return 0, capabilityStoreFailure(err, "count sync jobs")
	}
	return total, nil
}

// modelFromSQLC 将 sqlc model row 转成能力领域 Model。
func modelFromSQLC(row sqlc.Model) Model {
	return Model{
		ID:                       row.ID,
		ModelID:                  row.ModelID,
		DisplayName:              row.DisplayName,
		OwnedBy:                  row.OwnedBy,
		Status:                   row.Status,
		ContextWindowTokens:      int64Ptr(row.ContextWindowTokens),
		MaxOutputTokens:          int64Ptr(row.MaxOutputTokens),
		InputPriceUSDPerMTokens:  numericDecimalString(row.InputPriceUsdPerMillionTokens),
		OutputPriceUSDPerMTokens: numericDecimalString(row.OutputPriceUsdPerMillionTokens),
		ReleaseDate:              datePtr(row.ReleaseDate),
		Source:                   Source(row.Source),
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
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
		UpdatedBy:    textPtr(row.UpdatedBy),
	}
}

// capabilityKeyFromSQLC 将 sqlc 行转成领域 CapabilityKey。
func capabilityKeyFromSQLC(row sqlc.ListCapabilityKeysRow) CapabilityKey {
	return capabilityKeyFromFields(
		row.Key, row.Domain, row.DisplayName, row.Description,
		row.SortOrder, row.Deprecated, row.ProtocolScope,
		row.CreatedAt, row.UpdatedAt,
	)
}

func capabilityKeyFromGetRow(row sqlc.GetCapabilityKeyRow) CapabilityKey {
	return capabilityKeyFromFields(
		row.Key, row.Domain, row.DisplayName, row.Description,
		row.SortOrder, row.Deprecated, row.ProtocolScope,
		row.CreatedAt, row.UpdatedAt,
	)
}

func capabilityKeyFromCreateRow(row sqlc.CreateCapabilityKeyRow) CapabilityKey {
	return capabilityKeyFromFields(
		row.Key, row.Domain, row.DisplayName, row.Description,
		row.SortOrder, row.Deprecated, row.ProtocolScope,
		row.CreatedAt, row.UpdatedAt,
	)
}

func capabilityKeyFromUpdateRow(row sqlc.UpdateCapabilityKeyRow) CapabilityKey {
	return capabilityKeyFromFields(
		row.Key, row.Domain, row.DisplayName, row.Description,
		row.SortOrder, row.Deprecated, row.ProtocolScope,
		row.CreatedAt, row.UpdatedAt,
	)
}

func capabilityKeyFromFields(
	key, domain, displayName, description string,
	sortOrder int32,
	deprecated bool,
	protocolScope string,
	createdAt, updatedAt pgtype.Timestamptz,
) CapabilityKey {
	return CapabilityKey{
		Key:           Key(key),
		Domain:        domain,
		DisplayName:   displayName,
		Description:   description,
		SortOrder:     sortOrder,
		Deprecated:    deprecated,
		ProtocolScope: NormalizeProtocolScope(protocolScope),
		CreatedAt:     createdAt.Time,
		UpdatedAt:     updatedAt.Time,
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
		failure.WithMessage("sync job source is invalid"),
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
