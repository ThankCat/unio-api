package modelcatalog

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

const (
	// licenseID 与 attribution 与 docs/datasources/MODELS_DEV_LICENSE.md 对齐，随同步任务落审计。
	licenseID   = "MIT"
	attribution = "Model metadata sourced from models.dev (© 2025 models.dev, MIT License)."

	maxSyncErrorDetailBytes = 2048
)

// RawFeed 是 models.dev 一次拉取的原始字节（models.json 必需，api.json 价格可空）。
type RawFeed struct {
	ModelsJSON []byte
	APIJSON    []byte
}

// Fetcher 拉取 models.dev 原始数据；HTTP 实现注入，便于测试以 fixture 替身。
type Fetcher interface {
	Fetch(ctx context.Context) (RawFeed, error)
}

// Options 控制单次同步行为。
type Options struct {
	// DryRun 为 true 时只计算合并计划、不写任何库（含 sync_job），供 sync-models --dry-run 预演。
	DryRun bool
}

// Result 是一次同步的结果摘要，供调用方与审计使用。
type Result struct {
	DryRun              bool
	FeedModels          int
	Inserted            int
	Updated             int
	Skipped             int
	Removed             int
	CapabilitiesSeeded  int
	ManualConflicts     []string
	RemovedCanonicalIDs []string
	Fingerprint         string
}

// syncStats 是写入 model_capability_sync_jobs.stats_json 的审计载荷（含 license 指纹）。
type syncStats struct {
	License            string   `json:"license"`
	Attribution        string   `json:"attribution"`
	SourceFingerprint  string   `json:"source_fingerprint"`
	FeedModels         int      `json:"feed_models"`
	Inserted           int      `json:"inserted"`
	Updated            int      `json:"updated"`
	Skipped            int      `json:"skipped"`
	CapabilitiesSeeded int      `json:"capabilities_seeded"`
	ManualConflicts    []string `json:"manual_conflicts"`
	Removed            []string `json:"removed"`
}

// Syncer 编排 models.dev 同步：拉取 → 解析 → 合并规划 → 落库 → 记 sync_job（含 license 审计）。
type Syncer struct {
	fetcher Fetcher
	store   SyncStore
}

// NewSyncer 创建 models.dev 同步编排器。
func NewSyncer(fetcher Fetcher, store SyncStore) *Syncer {
	if fetcher == nil {
		panic("modelcatalog: fetcher is required")
	}
	if store == nil {
		panic("modelcatalog: store is required")
	}

	return &Syncer{fetcher: fetcher, store: store}
}

// Sync 执行一次 models.dev 同步。DryRun 模式只返回合并计划摘要，不写库。
func (s *Syncer) Sync(ctx context.Context, opts Options) (Result, error) {
	raw, err := s.fetcher.Fetch(ctx)
	if err != nil {
		return Result{}, failure.Wrap(failure.CodeModelCatalogStoreFailed, err, failure.WithMessage("fetch models.dev feed"))
	}

	feed, err := ParseFeed(raw.ModelsJSON, raw.APIJSON)
	if err != nil {
		return Result{}, err
	}

	existing, err := s.store.ListCanonicalModels(ctx)
	if err != nil {
		return Result{}, err
	}

	plan := PlanSync(feed, existing)

	if opts.DryRun {
		return dryRunResult(feed, plan), nil
	}

	return s.apply(ctx, feed, plan)
}

func dryRunResult(feed Feed, plan Plan) Result {
	capabilitiesSeeded := 0
	for _, model := range plan.Inserts {
		capabilitiesSeeded += len(model.CoarseCapabilities)
	}

	return Result{
		DryRun:              true,
		FeedModels:          len(feed.Models),
		Inserted:            len(plan.Inserts),
		Updated:             len(plan.Updates),
		Skipped:             len(plan.Conflicts),
		Removed:             len(plan.Removals),
		CapabilitiesSeeded:  capabilitiesSeeded,
		ManualConflicts:     plan.Conflicts,
		RemovedCanonicalIDs: plan.Removals,
		Fingerprint:         feed.Fingerprint,
	}
}

func (s *Syncer) apply(ctx context.Context, feed Feed, plan Plan) (Result, error) {
	jobID, err := s.store.CreateSyncJob(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := s.store.MarkSyncJobRunning(ctx, jobID); err != nil {
		return Result{}, err
	}

	result, applyErr := s.applyPlan(ctx, feed, plan)
	if applyErr != nil {
		if markErr := s.store.MarkSyncJobFailed(ctx, jobID, truncateError(applyErr)); markErr != nil {
			return Result{}, markErr
		}
		return Result{}, applyErr
	}

	stats := syncStats{
		License:            licenseID,
		Attribution:        attribution,
		SourceFingerprint:  feed.Fingerprint,
		FeedModels:         result.FeedModels,
		Inserted:           result.Inserted,
		Updated:            result.Updated,
		Skipped:            result.Skipped,
		CapabilitiesSeeded: result.CapabilitiesSeeded,
		ManualConflicts:    result.ManualConflicts,
		Removed:            result.RemovedCanonicalIDs,
	}
	statsJSON, err := json.Marshal(stats)
	if err != nil {
		return Result{}, failure.Wrap(failure.CodeModelCatalogStoreFailed, err, failure.WithMessage("marshal sync stats"))
	}
	if err := s.store.MarkSyncJobSucceeded(ctx, jobID, statsJSON); err != nil {
		return Result{}, err
	}

	return result, nil
}

func (s *Syncer) applyPlan(ctx context.Context, feed Feed, plan Plan) (Result, error) {
	result := Result{
		FeedModels:          len(feed.Models),
		ManualConflicts:     append([]string(nil), plan.Conflicts...),
		Skipped:             len(plan.Conflicts),
		RemovedCanonicalIDs: make([]string, 0, len(plan.Removals)),
		Fingerprint:         feed.Fingerprint,
	}

	for _, model := range plan.Inserts {
		modelID, applied, err := s.store.UpsertSeedModel(ctx, model)
		if err != nil {
			return Result{}, err
		}
		if !applied {
			result.Skipped++
			result.ManualConflicts = append(result.ManualConflicts, model.CanonicalID)
			continue
		}
		result.Inserted++
		for _, decl := range model.CoarseCapabilities {
			if err := s.store.UpsertCoarseCapability(ctx, modelID, decl); err != nil {
				return Result{}, err
			}
			result.CapabilitiesSeeded++
		}
	}

	for _, model := range plan.Updates {
		_, applied, err := s.store.UpsertSeedModel(ctx, model)
		if err != nil {
			return Result{}, err
		}
		if !applied {
			result.Skipped++
			result.ManualConflicts = append(result.ManualConflicts, model.CanonicalID)
			continue
		}
		result.Updated++
	}

	for _, canonicalID := range plan.Removals {
		applied, err := s.store.MarkSeedModelRemoved(ctx, canonicalID)
		if err != nil {
			return Result{}, err
		}
		if applied {
			result.Removed++
			result.RemovedCanonicalIDs = append(result.RemovedCanonicalIDs, canonicalID)
		}
	}

	return result, nil
}

func truncateError(err error) string {
	detail := strings.TrimSpace(err.Error())
	if len(detail) <= maxSyncErrorDetailBytes {
		return detail
	}
	return detail[:maxSyncErrorDetailBytes] + "...[truncated]"
}
