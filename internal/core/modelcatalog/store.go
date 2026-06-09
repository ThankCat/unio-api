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

// SyncStore 提供 models.dev 同步所需的目录读写、粗能力写入与同步任务生命周期能力。
type SyncStore interface {
	ListCanonicalModels(ctx context.Context) ([]ExistingModel, error)
	// UpsertSeedModel 按 canonical_id upsert 种子模型；applied=false 表示命中 source=manual 守护未写入。
	UpsertSeedModel(ctx context.Context, model CanonicalModel) (modelID int64, applied bool, err error)
	// MarkSeedModelRemoved 标记上游已删除的种子模型；applied=false 表示无可标记行（manual/已标记）。
	MarkSeedModelRemoved(ctx context.Context, canonicalID string) (applied bool, err error)
	// UpsertCoarseCapability 写入 models.dev 粗能力位（source=models_dev）。
	UpsertCoarseCapability(ctx context.Context, modelID int64, decl capability.Declaration) error

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

func (s *syncQueriesStore) ListCanonicalModels(ctx context.Context) ([]ExistingModel, error) {
	rows, err := s.queries.ListCanonicalModels(ctx)
	if err != nil {
		return nil, catalogFailure(err, "list canonical models")
	}

	items := make([]ExistingModel, 0, len(rows))
	for _, row := range rows {
		items = append(items, ExistingModel{
			ID:          row.ID,
			CanonicalID: row.CanonicalID.String,
			Source:      row.Source,
			Removed:     row.RemovedUpstreamAt.Valid,
		})
	}

	return items, nil
}

func (s *syncQueriesStore) UpsertSeedModel(ctx context.Context, model CanonicalModel) (int64, bool, error) {
	inputPrice, err := numericFromDecimal(model.InputPrice)
	if err != nil {
		return 0, false, catalogFailure(err, "parse input price")
	}
	outputPrice, err := numericFromDecimal(model.OutputPrice)
	if err != nil {
		return 0, false, catalogFailure(err, "parse output price")
	}

	row, err := s.queries.UpsertSeedModelByCanonicalID(ctx, sqlc.UpsertSeedModelByCanonicalIDParams{
		ModelID:                        model.CanonicalID,
		DisplayName:                    model.DisplayName,
		OwnedBy:                        model.Lab,
		CanonicalID:                    textValue(model.CanonicalID),
		Lab:                            optionalText(model.Lab),
		ContextWindowTokens:            int8Value(model.ContextTokens),
		MaxOutputTokens:                int8Value(model.MaxOutputTokens),
		InputPriceUsdPerMillionTokens:  inputPrice,
		OutputPriceUsdPerMillionTokens: outputPrice,
		ReleaseDate:                    dateValue(model.ReleaseDate),
	})
	if err != nil {
		// source=manual/import 行命中 WHERE 守护时不更新、不返回行：视作未写入。
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, catalogFailure(err, "upsert seed model")
	}

	return row.ID, true, nil
}

func (s *syncQueriesStore) MarkSeedModelRemoved(ctx context.Context, canonicalID string) (bool, error) {
	_, err := s.queries.MarkSeedModelRemovedUpstream(ctx, textValue(canonicalID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, catalogFailure(err, "mark seed model removed")
	}

	return true, nil
}

func (s *syncQueriesStore) UpsertCoarseCapability(ctx context.Context, modelID int64, decl capability.Declaration) error {
	_, err := s.queries.UpsertModelCapability(ctx, sqlc.UpsertModelCapabilityParams{
		ModelID:       modelID,
		CapabilityKey: string(decl.Key),
		SupportLevel:  string(decl.SupportLevel),
		Limits:        nil,
		Source:        syncJobSource,
		UpdatedBy:     pgtype.Text{Valid: false},
	})
	if err != nil {
		return catalogFailure(err, "upsert coarse capability")
	}

	return nil
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

func textValue(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
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
