package calibration

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ThankCat/unio-api/internal/core/capability"
)

const (
	defaultBatchSize        = 1000
	defaultMaxChangesPerRun = 200
)

// Options 控制一次校正执行。DryRun=true 时只计算计划、不写库（不增量、不推进 watermark、不落决策）。
type Options struct {
	DryRun bool
}

// Result 汇报一次校正执行结果。
type Result struct {
	ScannedAttempts int
	MaxAttemptID    int64
	DryRun          bool
	Plan            Plan
	// Degradations 是本轮发现的「已声明强证据能力近期证据塌陷」上游退化告警候选（只告警，不改库）。
	Degradations []Degradation
}

// Calibrator 编排能力自动校正：增量扫描成功流量 → 聚合 rollup → 推进 watermark → 决策 → 自动补/建议。
type Calibrator struct {
	store            Store
	thresholds       Thresholds
	batchSize        int32
	maxChangesPerRun int
	now              func() time.Time
}

// NewCalibrator 创建校正编排器。batchSize<=0 / maxChangesPerRun<=0 时回退默认值。
func NewCalibrator(store Store, thresholds Thresholds, batchSize int32, maxChangesPerRun int) *Calibrator {
	if store == nil {
		panic("calibration: store is required")
	}
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	if maxChangesPerRun <= 0 {
		maxChangesPerRun = defaultMaxChangesPerRun
	}
	return &Calibrator{
		store:            store,
		thresholds:       thresholds,
		batchSize:        batchSize,
		maxChangesPerRun: maxChangesPerRun,
		now:              time.Now,
	}
}

type obsKey struct {
	ModelID   int64
	ChannelID int64
	Key       capability.Key
}

type obsDelta struct {
	success  int64
	evidence int64
}

// Run 执行一轮校正。
func (c *Calibrator) Run(ctx context.Context, opts Options) (Result, error) {
	now := c.now()
	since := now.Add(-c.thresholds.Lookback)

	existing, err := c.store.ListObservations(ctx)
	if err != nil {
		return Result{}, err
	}

	watermark, err := c.store.Watermark(ctx)
	if err != nil {
		return Result{}, err
	}

	deltas, scanned, maxID, err := c.scanDeltas(ctx, watermark, since)
	if err != nil {
		return Result{}, err
	}

	if !opts.DryRun {
		if err := c.persistDeltas(ctx, deltas); err != nil {
			return Result{}, err
		}
		if maxID > watermark {
			if err := c.store.SetWatermark(ctx, maxID); err != nil {
				return Result{}, err
			}
		}
	}

	effective := mergeObservations(existing, deltas, now)

	models, err := c.modelContexts(ctx)
	if err != nil {
		return Result{}, err
	}

	plan := BuildPlan(effective, models, c.thresholds, now)

	// 退化告警基于「本轮新窗口」的增量观测（非历史累计），才能反映近期证据是否塌陷。
	degradations := DetectDegradations(deltasToObservations(deltas, now), models, c.thresholds)

	if !opts.DryRun {
		if err := c.applyPlan(ctx, plan); err != nil {
			return Result{}, err
		}
	}

	return Result{
		ScannedAttempts: scanned,
		MaxAttemptID:    maxID,
		DryRun:          opts.DryRun,
		Plan:            plan,
		Degradations:    degradations,
	}, nil
}

// deltasToObservations 把本轮增量聚合转成 Observation 列表（供退化检测复用纯函数）。
func deltasToObservations(deltas map[obsKey]*obsDelta, now time.Time) []Observation {
	out := make([]Observation, 0, len(deltas))
	for k, d := range deltas {
		out = append(out, Observation{
			ModelID:   k.ModelID,
			ChannelID: k.ChannelID,
			Key:       k.Key,
			Success:   d.success,
			Evidence:  d.evidence,
			LastSeen:  now,
		})
	}
	return out
}

// scanDeltas 增量扫描成功尝试，按 (模型, 渠道, 能力) 聚合成功/证据增量。
func (c *Calibrator) scanDeltas(ctx context.Context, watermark int64, since time.Time) (map[obsKey]*obsDelta, int, int64, error) {
	deltas := make(map[obsKey]*obsDelta)
	scanned := 0
	maxID := watermark
	after := watermark

	for {
		rows, err := c.store.ScanSucceeded(ctx, after, since, c.batchSize)
		if err != nil {
			return nil, 0, 0, err
		}
		if len(rows) == 0 {
			break
		}

		for _, row := range rows {
			scanned++
			if row.AttemptID > maxID {
				maxID = row.AttemptID
			}
			after = row.AttemptID

			for _, key := range row.RequiredCapabilities {
				if !capability.IsRegisteredKey(key) {
					continue
				}
				k := obsKey{ModelID: row.ModelID, ChannelID: row.ChannelID, Key: key}
				d := deltas[k]
				if d == nil {
					d = &obsDelta{}
					deltas[k] = d
				}
				d.success++
				if AttemptHasEvidence(key, row.FinishClass, row.CacheReadTokens, row.ReasoningTokens) {
					d.evidence++
				}
			}
		}

		if len(rows) < int(c.batchSize) {
			break
		}
	}

	return deltas, scanned, maxID, nil
}

func (c *Calibrator) persistDeltas(ctx context.Context, deltas map[obsKey]*obsDelta) error {
	for k, d := range deltas {
		if err := c.store.IncrementObservation(ctx, k.ModelID, k.ChannelID, k.Key, d.success, d.evidence); err != nil {
			return err
		}
	}
	return nil
}

// mergeObservations 把已存 rollup 与本轮增量合并成「有效观测」供决策（dry-run 也据此预览，不依赖落库）。
func mergeObservations(existing []Observation, deltas map[obsKey]*obsDelta, now time.Time) []Observation {
	merged := make(map[obsKey]Observation, len(existing))
	for _, obs := range existing {
		merged[obsKey{ModelID: obs.ModelID, ChannelID: obs.ChannelID, Key: obs.Key}] = obs
	}

	for k, d := range deltas {
		obs := merged[k]
		obs.ModelID = k.ModelID
		obs.ChannelID = k.ChannelID
		obs.Key = k.Key
		obs.Success += d.success
		obs.Evidence += d.evidence
		obs.LastSeen = now
		merged[k] = obs
	}

	out := make([]Observation, 0, len(merged))
	for _, obs := range merged {
		out = append(out, obs)
	}
	return out
}

func (c *Calibrator) modelContexts(ctx context.Context) (map[int64]ModelContext, error) {
	models, err := c.store.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := c.store.EnabledChannelCounts(ctx)
	if err != nil {
		return nil, err
	}
	declared, err := c.store.DeclaredKeys(ctx)
	if err != nil {
		return nil, err
	}
	dismissed, err := c.store.DismissedKeys(ctx)
	if err != nil {
		return nil, err
	}

	out := make(map[int64]ModelContext, len(models))
	for _, m := range models {
		out[m.ID] = ModelContext{
			Mode:            m.Mode,
			EnabledChannels: counts[m.ID],
			Declared:        declared[m.ID],
			Dismissed:       dismissed[m.ID],
		}
	}
	return out, nil
}

// applyPlan 落库决策：先自动补、再建议，总写入数受 maxChangesPerRun 封顶（防写风暴）。
func (c *Calibrator) applyPlan(ctx context.Context, plan Plan) error {
	changes := 0
	for _, d := range plan.AutoApply {
		if changes >= c.maxChangesPerRun {
			return nil
		}
		rationale, err := json.Marshal(d.Rationale)
		if err != nil {
			return err
		}
		if err := c.store.ApplyAutoCapability(ctx, d.ModelID, d.Key, rationale); err != nil {
			return err
		}
		changes++
	}
	for _, d := range plan.Suggestions {
		if changes >= c.maxChangesPerRun {
			return nil
		}
		rationale, err := json.Marshal(d.Rationale)
		if err != nil {
			return err
		}
		if err := c.store.RecordSuggestion(ctx, d.ModelID, d.Key, d.Level, d.EvidenceKind, rationale); err != nil {
			return err
		}
		changes++
	}
	return nil
}
