package modelrouting

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
)

// Candidate 是某模型的一条候选渠道，合并了数据库硬事实与 Redis 运行态。
//
// 指针字段在运行态不可读时为 nil：那与「值为 0」不是一回事。并发 0 意味着此刻没有请求，
// 而读不到意味着不知道——把后者显示成前者会让管理员以为渠道闲着。
type Candidate struct {
	ChannelID       int64
	ChannelName     string
	ChannelStatus   string
	ProviderID      int64
	ProviderName    string
	ProviderStatus  string
	UpstreamModel   string
	BindingStatus   string
	CredentialValid bool
	Priority        int32

	// RuntimeStatus 是 SnapshotMany 给出的候选判定（current / open / rate_limited ...）。
	// 降级路径下为空，改由 BreakerState 等分项描述。
	RuntimeStatus       string
	BreakerState        string
	BreakerOpenRemainMs *int64
	ConcurrencyUsed     *int64
	ConcurrencyLimit    *int64
	CooldownRemainingMs *int64
	PermissionPaused    bool
	// RPM 是当前 UTC 分钟桶的观测请求数，不是滚动 60 秒。
	RPM *int64
}

// CandidateView 是实时候选视图。
type CandidateView struct {
	// RuntimeAvailable 为 false 表示 Redis 运行态没能读齐，候选里只有数据库事实。
	RuntimeAvailable bool
	// RuntimeErrorCode 说明降级原因，供界面提示。
	RuntimeErrorCode string
	Candidates       []Candidate
	ObservedAt       time.Time
}

// Candidates 返回某模型此刻的候选运行态。
//
// 先走 SnapshotMany（带 (channel, model) 权限与 revision 对齐判定，信息最全）；
// 它整批失败时降级到 ObserveChannels——后者不校验 revision，读到什么算什么。
// 观测接口不跟随选路的 fail-closed 语义：那里失败是在保护准入决策，这里失败只会让管理员什么都看不到。
func (s *Service) Candidates(ctx context.Context, modelID int64) (CandidateView, error) {
	model, err := s.store.LookupModelByID(ctx, modelID)
	if err != nil {
		return CandidateView{}, opsutil.StoreFailed(err, "lookup model for routing candidates")
	}
	pool, err := s.store.ModelRuntimePool(ctx, sqlc.ModelRuntimePoolParams{
		ModelID: model.ModelID,
		AtTime:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		return CandidateView{}, opsutil.StoreFailed(err, "list model runtime pool")
	}

	bindings := enabledBindings(pool)
	if len(bindings) == 0 {
		return CandidateView{RuntimeAvailable: true, Candidates: nil, ObservedAt: time.Now()}, nil
	}
	if s.breaker == nil || s.runtime == nil {
		return buildCandidateView(bindings, nil, nil, nil, false, "runtime_reader_unavailable")
	}

	channelIDs := make([]int64, 0, len(bindings))
	for _, row := range bindings {
		channelIDs = append(channelIDs, row.ChannelID)
	}
	// 样本读失败只影响 RPM 一列，不影响主体，因此单独忽略错误。
	samples, sampleErr := s.breaker.AggregateChannelSamples(ctx, channelIDs)
	if sampleErr != nil {
		samples = nil
	}

	snapshots, snapshotErr := s.snapshotCandidates(ctx, modelID, bindings)
	if snapshotErr == nil {
		return buildCandidateView(bindings, snapshots, nil, samples, true, "")
	}

	observed, observeErr := s.breaker.ObserveChannels(ctx, channelIDs)
	if observeErr != nil {
		// 两条路都读不到时才如实说运行态不可用，候选仍返回数据库事实。
		return buildCandidateView(bindings, nil, nil, samples, false, degradeReason(observeErr))
	}
	return buildCandidateView(bindings, nil, observed, samples, false, degradeReason(snapshotErr))
}

func (s *Service) snapshotCandidates(
	ctx context.Context,
	modelID int64,
	bindings []sqlc.ModelRuntimePoolRow,
) (map[int64]breakerstore.CandidateSnapshot, error) {
	routing, err := s.runtime.Routing(ctx)
	if err != nil {
		return nil, err
	}
	admission, err := s.runtime.Admission(ctx)
	if err != nil {
		return nil, err
	}

	inputs := make([]breakerstore.SnapshotCandidateInput, 0, len(bindings))
	for _, row := range bindings {
		inputs = append(inputs, breakerstore.SnapshotCandidateInput{
			ProviderID:              row.ProviderID,
			ChannelID:               row.ChannelID,
			OriginRevision:          row.ProviderOriginRevision,
			ProviderStatusRevision:  row.ProviderStatusRevision,
			ChannelConfigRevision:   row.ChannelConfigRevision,
			ChannelCapacityRevision: row.ChannelCapacityRevision,
		})
	}
	result, err := s.breaker.SnapshotMany(ctx, breakerstore.SnapshotManyInput{
		IntegrityEpoch:            routing.Integrity.Epoch,
		IntegrityRevision:         routing.Integrity.Revision,
		GlobalConcurrencyRevision: admission.Concurrency,
		CircuitBreakerRevision:    routing.CircuitBreaker,
		RoutingBalanceRevision:    routing.RoutingBalance,
		ModelID:                   modelID,
		Candidates:                inputs,
	})
	if err != nil {
		return nil, err
	}
	if len(result.Candidates) != len(bindings) {
		return nil, errors.New("snapshot candidate count does not match the request")
	}
	out := make(map[int64]breakerstore.CandidateSnapshot, len(result.Candidates))
	for index, snapshot := range result.Candidates {
		out[bindings[index].ChannelID] = snapshot
	}
	return out, nil
}

// enabledBindings 从全池诊断结果里筛出该模型的启用绑定。
// ModelRuntimePool 故意不过滤（它要解释「为什么没进候选」），但候选视图只关心真正的候选。
func enabledBindings(pool []sqlc.ModelRuntimePoolRow) []sqlc.ModelRuntimePoolRow {
	out := make([]sqlc.ModelRuntimePoolRow, 0, len(pool))
	for _, row := range pool {
		if row.BindingStatus == "enabled" {
			out = append(out, row)
		}
	}
	return out
}

func buildCandidateView(
	bindings []sqlc.ModelRuntimePoolRow,
	snapshots map[int64]breakerstore.CandidateSnapshot,
	observed []breakerstore.ObservedChannelRuntime,
	samples map[int64]breakerstore.ChannelSampleWindow,
	runtimeAvailable bool,
	runtimeErrorCode string,
) (CandidateView, error) {
	observedByChannel := make(map[int64]breakerstore.ObservedChannelRuntime, len(observed))
	for _, row := range observed {
		observedByChannel[row.ChannelID] = row
	}

	out := CandidateView{
		RuntimeAvailable: runtimeAvailable,
		RuntimeErrorCode: runtimeErrorCode,
		Candidates:       make([]Candidate, 0, len(bindings)),
		ObservedAt:       time.Now(),
	}
	for _, row := range bindings {
		candidate := Candidate{
			ChannelID:       row.ChannelID,
			ChannelName:     row.ChannelName,
			ChannelStatus:   row.ChannelStatus,
			ProviderID:      row.ProviderID,
			ProviderName:    row.ProviderName,
			ProviderStatus:  row.ProviderStatus,
			BindingStatus:   row.BindingStatus,
			CredentialValid: row.CredentialValid,
			Priority:        row.Priority,
		}
		if sample, ok := samples[row.ChannelID]; ok {
			rpm := sample.RPM
			candidate.RPM = &rpm
		}

		if snapshot, ok := snapshots[row.ChannelID]; ok {
			applySnapshot(&candidate, snapshot)
		} else if observation, ok := observedByChannel[row.ChannelID]; ok {
			applyObservation(&candidate, observation)
		}
		out.Candidates = append(out.Candidates, candidate)
	}
	return out, nil
}

func applySnapshot(candidate *Candidate, snapshot breakerstore.CandidateSnapshot) {
	candidate.RuntimeStatus = string(snapshot.Status)
	candidate.PermissionPaused = snapshot.ModelPermissionPaused
	used := snapshot.Concurrency.Used
	limit := snapshot.Concurrency.Limit
	candidate.ConcurrencyUsed = &used
	candidate.ConcurrencyLimit = &limit
	cooldown := snapshot.CooldownRemainingMs
	candidate.CooldownRemainingMs = &cooldown
	candidate.BreakerState = string(snapshot.Channel.State)
	if snapshot.Channel.OpenRemainingMs > 0 {
		remaining := snapshot.Channel.OpenRemainingMs
		candidate.BreakerOpenRemainMs = &remaining
	}
}

func applyObservation(candidate *Candidate, observation breakerstore.ObservedChannelRuntime) {
	if observation.ConcurrencyKnown() {
		used := observation.Concurrency.Used
		candidate.ConcurrencyUsed = &used
	}
	if observation.CapacityKnown {
		limit := observation.Concurrency.Limit
		candidate.ConcurrencyLimit = &limit
	}
	cooldown := observation.CooldownRemainingMs
	candidate.CooldownRemainingMs = &cooldown
	candidate.BreakerState = string(observation.Breaker.State)
	if observation.Breaker.OpenRemainingMs > 0 {
		remaining := observation.Breaker.OpenRemainingMs
		candidate.BreakerOpenRemainMs = &remaining
	}
}

// degradeReason 把 SnapshotMany 的失败翻译成可展示的短码。
func degradeReason(err error) string {
	if code := failure.CodeOf(err); code != "" {
		return string(code)
	}
	return "runtime_snapshot_failed"
}
