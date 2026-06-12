package capability

import (
	"context"

	core "github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/core/modelcatalog"
)

const (
	defaultSyncJobLimit = 20
	maxSyncJobLimit     = 50
)

// Syncer 是 models.dev 同步编排能力（由 modelcatalog.Syncer 满足）。
type Syncer interface {
	Sync(ctx context.Context, opts modelcatalog.Options) (modelcatalog.Result, error)
}

// SyncJobStore 是同步任务历史的只读能力（由 core/capability.Store 满足）。
type SyncJobStore interface {
	ListSyncJobs(ctx context.Context, limit int32) ([]core.SyncJob, error)
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

// ListJobs 倒序返回最近的同步任务（limit 越界回退默认值并夹紧上限）。
func (s *SyncService) ListJobs(ctx context.Context, limit int32) ([]core.SyncJob, error) {
	if limit <= 0 {
		limit = defaultSyncJobLimit
	}
	if limit > maxSyncJobLimit {
		limit = maxSyncJobLimit
	}
	jobs, err := s.store.ListSyncJobs(ctx, limit)
	if err != nil {
		return nil, storeFailed(err, "list sync jobs")
	}
	return jobs, nil
}

// Trigger 触发一次 models.dev 同步；dryRun 只返回合并计划摘要，不写任何库。
func (s *SyncService) Trigger(ctx context.Context, dryRun bool) (modelcatalog.Result, error) {
	return s.syncer.Sync(ctx, modelcatalog.Options{DryRun: dryRun})
}
