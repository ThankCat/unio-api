package modelcatalog

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// syncJobSource 是 models.dev 同步任务在 model_capability_sync_jobs.source 的取值。
const syncJobSource = string(capability.SourceModelsDev)

// LatestSyncJob 是最近一次同步任务的精简快照，供 worker cron 门控判断。
type LatestSyncJob struct {
	Found      bool
	Status     capability.SyncJobStatus
	FinishedAt *time.Time
}

// SyncStore 提供 models.dev 同步所需的目录读写与同步任务生命周期能力（阶段 14：只写目录，不碰运行时 models）。
type SyncStore interface {
	// ListCatalogEntries 列出库内已有目录条目（含下架标记），供推导「feed 不含 → 标记下架」。
	ListCatalogEntries(ctx context.Context) ([]ExistingCatalogEntry, error)
	// UpsertCatalogEntry 全量 upsert 一条目录条目（元数据 + 指纹），并整体替换其能力提示。
	UpsertCatalogEntry(ctx context.Context, model CanonicalModel) error
	// MarkCatalogRemovedUpstream 标记上游已下架的目录条目；applied=false 表示无需更新（已标记）。
	MarkCatalogRemovedUpstream(ctx context.Context, canonicalID string) (applied bool, err error)

	CreateSyncJob(ctx context.Context) (jobID int64, err error)
	MarkSyncJobRunning(ctx context.Context, jobID int64) error
	MarkSyncJobSucceeded(ctx context.Context, jobID int64, stats []byte) error
	MarkSyncJobFailed(ctx context.Context, jobID int64, errText string) error
	LatestSyncJob(ctx context.Context) (LatestSyncJob, error)
}

// syncQueriesStore 是 SyncStore 的 sqlc 实现。
type syncQueriesStore struct {
	queries *sqlc.Queries
}

// NewSyncStore 创建 sqlc 支撑的 models.dev 同步数据访问层。
func NewSyncStore(queries *sqlc.Queries) SyncStore {
	return &syncQueriesStore{queries: queries}
}

func (s *syncQueriesStore) ListCatalogEntries(ctx context.Context) ([]ExistingCatalogEntry, error) {
	rows, err := s.queries.ListModelCatalogCanonicalIDs(ctx)
	if err != nil {
		return nil, catalogFailure(err, "list catalog entries")
	}

	items := make([]ExistingCatalogEntry, 0, len(rows))
	for _, row := range rows {
		items = append(items, ExistingCatalogEntry{
			CanonicalID: row.CanonicalID,
			Removed:     row.RemovedUpstreamAt.Valid,
		})
	}

	return items, nil
}

func (s *syncQueriesStore) UpsertCatalogEntry(ctx context.Context, model CanonicalModel) error {
	inputPrice, err := numericFromDecimal(model.InputPrice)
	if err != nil {
		return catalogFailure(err, "parse input price")
	}
	outputPrice, err := numericFromDecimal(model.OutputPrice)
	if err != nil {
		return catalogFailure(err, "parse output price")
	}

	if _, err := s.queries.UpsertModelCatalogEntry(ctx, sqlc.UpsertModelCatalogEntryParams{
		CanonicalID:                    model.CanonicalID,
		Lab:                            model.Lab,
		DisplayName:                    model.DisplayName,
		ContextWindowTokens:            int8Value(model.ContextTokens),
		MaxOutputTokens:                int8Value(model.MaxOutputTokens),
		InputPriceUsdPerMillionTokens:  inputPrice,
		OutputPriceUsdPerMillionTokens: outputPrice,
		ReleaseDate:                    dateValue(model.ReleaseDate),
		Fingerprint:                    model.Fingerprint,
	}); err != nil {
		return catalogFailure(err, "upsert catalog entry")
	}

	// 能力提示整体替换：先清空再按声明重写，保证与最新 feed 一致。
	if err := s.queries.DeleteModelCatalogCapabilities(ctx, model.CanonicalID); err != nil {
		return catalogFailure(err, "delete catalog capabilities")
	}
	for _, decl := range model.CoarseCapabilities {
		if err := s.queries.InsertModelCatalogCapability(ctx, sqlc.InsertModelCatalogCapabilityParams{
			CanonicalID:   model.CanonicalID,
			CapabilityKey: string(decl.Key),
			SupportLevel:  string(decl.SupportLevel),
			Limits:        decl.Limits,
		}); err != nil {
			return catalogFailure(err, "insert catalog capability")
		}
	}

	return nil
}

func (s *syncQueriesStore) MarkCatalogRemovedUpstream(ctx context.Context, canonicalID string) (bool, error) {
	affected, err := s.queries.MarkModelCatalogRemovedUpstream(ctx, canonicalID)
	if err != nil {
		return false, catalogFailure(err, "mark catalog removed upstream")
	}

	return affected > 0, nil
}

func (s *syncQueriesStore) CreateSyncJob(ctx context.Context) (int64, error) {
	row, err := s.queries.CreateSyncJob(ctx, syncJobSource)
	if err != nil {
		return 0, catalogFailure(err, "create sync job")
	}

	return row.ID, nil
}

func (s *syncQueriesStore) MarkSyncJobRunning(ctx context.Context, jobID int64) error {
	if _, err := s.queries.MarkSyncJobRunning(ctx, jobID); err != nil {
		return catalogFailure(err, "mark sync job running")
	}

	return nil
}

func (s *syncQueriesStore) MarkSyncJobSucceeded(ctx context.Context, jobID int64, stats []byte) error {
	if _, err := s.queries.MarkSyncJobSucceeded(ctx, sqlc.MarkSyncJobSucceededParams{StatsJson: stats, ID: jobID}); err != nil {
		return catalogFailure(err, "mark sync job succeeded")
	}

	return nil
}

func (s *syncQueriesStore) MarkSyncJobFailed(ctx context.Context, jobID int64, errText string) error {
	if _, err := s.queries.MarkSyncJobFailed(ctx, sqlc.MarkSyncJobFailedParams{ErrorText: optionalText(errText), ID: jobID}); err != nil {
		return catalogFailure(err, "mark sync job failed")
	}

	return nil
}

func (s *syncQueriesStore) LatestSyncJob(ctx context.Context) (LatestSyncJob, error) {
	row, err := s.queries.GetLatestSyncJob(ctx, syncJobSource)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return LatestSyncJob{Found: false}, nil
		}
		return LatestSyncJob{}, catalogFailure(err, "get latest sync job")
	}

	out := LatestSyncJob{Found: true, Status: capability.SyncJobStatus(row.Status)}
	if row.FinishedAt.Valid {
		finished := row.FinishedAt.Time
		out.FinishedAt = &finished
	}

	return out, nil
}

func catalogFailure(err error, message string) error {
	return failure.Wrap(failure.CodeModelCatalogStoreFailed, err, failure.WithMessage(message))
}

func numericFromDecimal(value *string) (pgtype.Numeric, error) {
	if value == nil {
		return pgtype.Numeric{Valid: false}, nil
	}
	var numeric pgtype.Numeric
	if err := numeric.Scan(*value); err != nil {
		return pgtype.Numeric{}, err
	}
	return numeric, nil
}

func optionalText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{Valid: false}
	}
	return pgtype.Text{String: value, Valid: true}
}

func int8Value(value *int64) pgtype.Int8 {
	if value == nil {
		return pgtype.Int8{Valid: false}
	}
	return pgtype.Int8{Int64: *value, Valid: true}
}

func dateValue(value *time.Time) pgtype.Date {
	if value == nil {
		return pgtype.Date{Valid: false}
	}
	return pgtype.Date{Time: *value, Valid: true}
}
