package modelcatalog

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const (
	// licenseID 与 attribution 与 THIRD_PARTY_NOTICES.md 对齐，随同步任务落审计。
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

// LogoFetcher 是 Fetcher 的可选扩展：能抓出品方图标的实现才需要满足它。
// 做成可选是为了让只关心元数据的测试替身不必实现图标抓取。
type LogoFetcher interface {
	FetchLabLogo(ctx context.Context, slug string) (string, error)
}

// logoRefreshInterval 是图标的重抓间隔。厂商 logo 极少变动，
// 抓一次管很久；间隔太短只是白费上游带宽。
const logoRefreshInterval = 30 * 24 * time.Hour

// Options 控制单次同步行为。
type Options struct {
	// DryRun 为 true 时只计算合并计划、不写任何库（含 sync_job），供 sync-models --dry-run 预演。
	DryRun bool
}

// Result 是一次同步的结果摘要，供调用方与审计使用（阶段 14：目录口径）。
type Result struct {
	DryRun              bool
	FeedModels          int
	Upserted            int
	Removed             int
	CapabilityHints     int
	RemovedCanonicalIDs []string
	Fingerprint         string
	// LogosSynced 是本次实际抓到内容的出品方图标数（不含「确认上游没有」的）。
	LogosSynced int
}

// syncStats 是写入 model_capability_sync_jobs.stats_json 的审计载荷（含 license 指纹）。
type syncStats struct {
	License           string   `json:"license"`
	Attribution       string   `json:"attribution"`
	SourceFingerprint string   `json:"source_fingerprint"`
	FeedModels        int      `json:"feed_models"`
	Upserted          int      `json:"upserted"`
	Removed           int      `json:"removed"`
	CapabilityHints   int      `json:"capability_hints"`
	RemovedCanonical  []string `json:"removed_canonical_ids"`
	LogosSynced       int      `json:"logos_synced"`
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

	existing, err := s.store.ListCatalogEntries(ctx)
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
	capabilityHints := 0
	for _, model := range plan.Upserts {
		capabilityHints += len(model.CoarseCapabilities)
	}

	return Result{
		DryRun:              true,
		FeedModels:          len(feed.Models),
		Upserted:            len(plan.Upserts),
		Removed:             len(plan.Removals),
		CapabilityHints:     capabilityHints,
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
		License:           licenseID,
		Attribution:       attribution,
		SourceFingerprint: feed.Fingerprint,
		FeedModels:        result.FeedModels,
		Upserted:          result.Upserted,
		Removed:           result.Removed,
		CapabilityHints:   result.CapabilityHints,
		RemovedCanonical:  result.RemovedCanonicalIDs,
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
		RemovedCanonicalIDs: make([]string, 0, len(plan.Removals)),
		Fingerprint:         feed.Fingerprint,
	}

	for _, model := range plan.Upserts {
		if err := s.store.UpsertCatalogEntry(ctx, model); err != nil {
			return Result{}, err
		}
		result.Upserted++
		result.CapabilityHints += len(model.CoarseCapabilities)
	}

	for _, canonicalID := range plan.Removals {
		applied, err := s.store.MarkCatalogRemovedUpstream(ctx, canonicalID)
		if err != nil {
			return Result{}, err
		}
		if applied {
			result.Removed++
			result.RemovedCanonicalIDs = append(result.RemovedCanonicalIDs, canonicalID)
		}
	}

	logos, err := s.syncLabLogos(ctx, feed)
	if err != nil {
		return Result{}, err
	}
	result.LogosSynced = logos

	return result, nil
}

// syncLabLogos 登记 feed 里出现的出品方，并补齐缺失或过期的图标。
//
// 图标是纯展示资产，单个抓取失败不该让整次目录同步失败——那会让「上游少了一个 svg」
// 升级成「模型目录同步不了」。所以这里逐个 best-effort，失败的留到下次同步再试。
func (s *Syncer) syncLabLogos(ctx context.Context, feed Feed) (int, error) {
	labs := make(map[string]struct{}, 16)
	for _, model := range feed.Models {
		if model.Lab != "" {
			labs[model.Lab] = struct{}{}
		}
	}
	slugs := make([]string, 0, len(labs))
	for slug := range labs {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		if err := s.store.UpsertLab(ctx, slug); err != nil {
			return 0, err
		}
	}

	fetcher, ok := s.fetcher.(LogoFetcher)
	if !ok {
		return 0, nil
	}
	stale, err := s.store.ListLabsNeedingLogo(ctx, time.Now().Add(-logoRefreshInterval))
	if err != nil {
		return 0, err
	}

	synced := 0
	for _, slug := range stale {
		svg, fetchErr := fetcher.FetchLabLogo(ctx, slug)
		if fetchErr != nil {
			continue
		}
		// 空串也要落库并打时间戳：它记录「已经确认上游没有」，
		// 否则每次同步都会为同一批缺图标的出品方重新发一轮请求。
		if err := s.store.SaveLabLogo(ctx, slug, svg); err != nil {
			return 0, err
		}
		if svg != "" {
			synced++
		}
	}
	return synced, nil
}

func truncateError(err error) string {
	detail := strings.TrimSpace(err.Error())
	if len(detail) <= maxSyncErrorDetailBytes {
		return detail
	}
	return detail[:maxSyncErrorDetailBytes] + "...[truncated]"
}
