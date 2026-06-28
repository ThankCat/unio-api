package capability

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	core "github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/core/modelcatalog"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// Syncer 是 models.dev 同步编排能力（由 modelcatalog.Syncer 满足）。
type Syncer interface {
	Sync(ctx context.Context, opts modelcatalog.Options) (modelcatalog.Result, error)
}

// SyncJobStore 是同步任务历史的只读能力（由 core/capability.Store 满足）。
type SyncJobStore interface {
	ListSyncJobs(ctx context.Context, arg sqlc.ListSyncJobsParams) ([]core.SyncJob, error)
	CountSyncJobs(ctx context.Context) (int64, error)
}

// ListJobsParams 是分页/排序列出同步任务的入参。
type ListJobsParams struct {
	SortField string
	SortDesc  bool
	Limit     int32
	Offset    int32
}

// SyncService 编排 models.dev 同步触发与任务历史展示。
//
// Trigger 在 admin 请求内联同步执行（DryRun 只算计划、不写库；实际应用由 Syncer 内部建并推进
// model_capability_sync_jobs）。models.dev 同步只在「新模型首次 insert」写粗能力位，既有模型
// 能力靠手工覆盖维护。
type SyncService struct {
	syncer Syncer
	store  SyncJobStore
}

// NewSyncService 创建同步管理服务。
func NewSyncService(syncer Syncer, store SyncJobStore) *SyncService {
	return &SyncService{syncer: syncer, store: store}
}

// ListJobs 倒序分页返回同步任务及总数。
func (s *SyncService) ListJobs(ctx context.Context, params ListJobsParams) ([]core.SyncJob, int64, error) {
	rows, err := s.store.ListSyncJobs(ctx, sqlc.ListSyncJobsParams{
		SortField:  textNarg(params.SortField),
		SortDesc:   boolNarg(params.SortDesc),
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "list sync jobs")
	}
	total, err := s.store.CountSyncJobs(ctx)
	if err != nil {
		return nil, 0, storeFailed(err, "count sync jobs")
	}
	return rows, total, nil
}

// Trigger 触发一次 models.dev 同步；dryRun 只返回合并计划摘要，不写任何库。
func (s *SyncService) Trigger(ctx context.Context, dryRun bool) (modelcatalog.Result, error) {
	return s.syncer.Sync(ctx, modelcatalog.Options{DryRun: dryRun})
}

func textNarg(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func boolNarg(v bool) pgtype.Bool {
	return pgtype.Bool{Bool: v, Valid: true}
}
