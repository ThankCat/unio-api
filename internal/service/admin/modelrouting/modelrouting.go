// Package modelrouting 提供模型维度的选路可观测性：此刻的候选运行态，以及历史选路的分布统计。
//
// 选路没有独立的配置实体（ADR-0020），负载均衡与候选排序只能从模型侧观测。数据层是完整的
// （routing_decision_traces 记录了每次选路的完整过程，Redis 里有实时的熔断与并发），
// 缺的是入口。这个包补的就是入口。
//
// 它与 modelops 分开：那个包只依赖 sqlc，是纯粹的历史聚合；这里要读 Redis 运行态，
// 依赖 breakerstore 与 runtimefacts。
package modelrouting

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
)

// StatsWindowCap 是选路聚合的时间窗上限。
//
// trace_payload.candidates 装的是全池非 archived 渠道而非该模型的绑定渠道，
// 展开后的元素数约等于「选路次数 × 全池渠道数」；这张表又没有 TTL 与分区。
// 因此窗口必须封顶，超出部分截断并如实告诉调用方，而不是硬算到超时。
const StatsWindowCap = 24 * time.Hour

// Store 是选路观测所需的只读存储能力（由 *sqlc.Queries 满足）。
type Store interface {
	LookupModelByID(ctx context.Context, id int64) (sqlc.Model, error)
	ModelRoutingSelectionStats(ctx context.Context, arg sqlc.ModelRoutingSelectionStatsParams) ([]sqlc.ModelRoutingSelectionStatsRow, error)
	ModelRoutingOutcomeStats(ctx context.Context, arg sqlc.ModelRoutingOutcomeStatsParams) ([]sqlc.ModelRoutingOutcomeStatsRow, error)
	ModelRoutingExclusionStats(ctx context.Context, arg sqlc.ModelRoutingExclusionStatsParams) ([]sqlc.ModelRoutingExclusionStatsRow, error)
	ModelRoutingTraceList(ctx context.Context, arg sqlc.ModelRoutingTraceListParams) ([]sqlc.ModelRoutingTraceListRow, error)
	ModelRoutingTraceCount(ctx context.Context, arg sqlc.ModelRoutingTraceCountParams) (int64, error)
	ListChannelNames(ctx context.Context, ids []int64) ([]sqlc.ListChannelNamesRow, error)
	// ModelRuntimePool 是选路诊断用的全池视图（不过滤），候选视图在 Go 侧筛出启用绑定。
	// 复用它而不是新写查询，口径才不会和选路诊断分叉。
	ModelRuntimePool(ctx context.Context, arg sqlc.ModelRuntimePoolParams) ([]sqlc.ModelRuntimePoolRow, error)
	LiveTrafficChannels(ctx context.Context) ([]sqlc.LiveTrafficChannelsRow, error)
	LiveTrafficModels(ctx context.Context, arg sqlc.LiveTrafficModelsParams) ([]sqlc.LiveTrafficModelsRow, error)
}

// Service 提供模型选路观测。
type Service struct {
	store   Store
	runtime RuntimeReader
	breaker BreakerReader
	tpm     TPMReader
}

// NewService 创建选路观测服务。runtime/breaker/tpm 只用于实时视图；
// 传 nil 时统计类接口照常工作，实时视图会明确报告运行态不可用而不是假装正常。
func NewService(store Store, runtime RuntimeReader, breaker BreakerReader, tpm TPMReader) *Service {
	return &Service{store: store, runtime: runtime, breaker: breaker, tpm: tpm}
}

// SelectionStat 是一条渠道在窗口内被选中的次数。ChannelID 为 0 表示这些选路没能选出渠道。
type SelectionStat struct {
	ChannelID   int64
	ChannelName string
	Selections  int64
}

// OutcomeStat 是选路终态分布的一项。
type OutcomeStat struct {
	FinalResult string
	Occurrences int64
}

// ExclusionStat 是「候选为什么没被用上」的一项。
type ExclusionStat struct {
	Reason      string
	Occurrences int64
	// SampleChannelID 是该原因下命中最多的渠道，用于在界面上指认主要是谁。
	SampleChannelID   int64
	SampleChannelName string
	ChannelsTouched   int64
}

// Stats 是选路结果分布的完整响应。
type Stats struct {
	// From/To 是实际统计窗口，可能比请求的窗口短。
	From time.Time
	To   time.Time
	// WindowTruncated 表示请求窗口超出 StatsWindowCap 已被截断。
	WindowTruncated bool

	Selections      []SelectionStat
	TotalSelections int64
	Outcomes        []OutcomeStat
	Exclusions      []ExclusionStat
	TotalExclusions int64
}

// Stats 统计给定模型在时间窗内的选路结果与候选排除原因。
func (s *Service) Stats(ctx context.Context, modelID int64, from, to time.Time) (Stats, error) {
	model, err := s.store.LookupModelByID(ctx, modelID)
	if err != nil {
		return Stats{}, opsutil.StoreFailed(err, "lookup model for routing stats")
	}

	effectiveFrom, truncated := capWindow(from, to)
	out := Stats{From: effectiveFrom, To: to, WindowTruncated: truncated}

	selections, err := s.store.ModelRoutingSelectionStats(ctx, sqlc.ModelRoutingSelectionStatsParams{
		RequestedModelID: model.ModelID,
		FromTime:         tsParam(effectiveFrom),
		ToTime:           tsParam(to),
	})
	if err != nil {
		return Stats{}, opsutil.StoreFailed(err, "model routing selection stats")
	}
	outcomes, err := s.store.ModelRoutingOutcomeStats(ctx, sqlc.ModelRoutingOutcomeStatsParams{
		RequestedModelID: model.ModelID,
		FromTime:         tsParam(effectiveFrom),
		ToTime:           tsParam(to),
	})
	if err != nil {
		return Stats{}, opsutil.StoreFailed(err, "model routing outcome stats")
	}
	exclusions, err := s.store.ModelRoutingExclusionStats(ctx, sqlc.ModelRoutingExclusionStatsParams{
		RequestedModelID: model.ModelID,
		FromTime:         tsParam(effectiveFrom),
		ToTime:           tsParam(to),
	})
	if err != nil {
		return Stats{}, opsutil.StoreFailed(err, "model routing exclusion stats")
	}

	names, err := s.channelNames(ctx, selections, exclusions)
	if err != nil {
		return Stats{}, err
	}

	out.Selections = make([]SelectionStat, 0, len(selections))
	for _, row := range selections {
		stat := SelectionStat{Selections: row.Selections}
		if row.SelectedChannelID.Valid {
			stat.ChannelID = row.SelectedChannelID.Int64
			stat.ChannelName = names[stat.ChannelID]
		}
		out.TotalSelections += row.Selections
		out.Selections = append(out.Selections, stat)
	}

	out.Outcomes = make([]OutcomeStat, 0, len(outcomes))
	for _, row := range outcomes {
		out.Outcomes = append(out.Outcomes, OutcomeStat{
			FinalResult: row.FinalResult,
			Occurrences: row.Occurrences,
		})
	}

	out.Exclusions = make([]ExclusionStat, 0, len(exclusions))
	for _, row := range exclusions {
		out.TotalExclusions += row.Occurrences
		out.Exclusions = append(out.Exclusions, ExclusionStat{
			Reason:            row.ExcludedReason,
			Occurrences:       row.Occurrences,
			SampleChannelID:   row.SampleChannelID,
			SampleChannelName: names[row.SampleChannelID],
			ChannelsTouched:   row.ChannelsTouched,
		})
	}
	return out, nil
}

// TraceRow 是最近选路列表的一行。
type TraceRow struct {
	At        time.Time
	RequestID string
	// FinalResult 为空表示 trace 仍是 partial（请求进行中或进程崩溃遗留）。
	FinalResult         string
	CandidateCount      int32
	EligibleCount       int32
	SelectedChannelID   *int64
	SelectedChannelName string
	FallbackCount       int32
	CapacityWaitResult  string
}

// Traces 返回最近选路列表（分页）。这里不截断窗口：列级查询走索引，不展开 payload。
func (s *Service) Traces(ctx context.Context, modelID int64, from, to time.Time, limit, offset int32) ([]TraceRow, int64, error) {
	model, err := s.store.LookupModelByID(ctx, modelID)
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "lookup model for routing traces")
	}
	rows, err := s.store.ModelRoutingTraceList(ctx, sqlc.ModelRoutingTraceListParams{
		RequestedModelID: model.ModelID,
		FromTime:         tsParam(from),
		ToTime:           tsParam(to),
		PageLimit:        limit,
		PageOffset:       offset,
	})
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "model routing trace list")
	}
	total, err := s.store.ModelRoutingTraceCount(ctx, sqlc.ModelRoutingTraceCountParams{
		RequestedModelID: model.ModelID,
		FromTime:         tsParam(from),
		ToTime:           tsParam(to),
	})
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "model routing trace count")
	}

	channelIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row.SelectedChannelID.Valid {
			channelIDs = append(channelIDs, row.SelectedChannelID.Int64)
		}
	}
	names, err := s.lookupChannelNames(ctx, channelIDs)
	if err != nil {
		return nil, 0, err
	}

	out := make([]TraceRow, 0, len(rows))
	for _, row := range rows {
		item := TraceRow{
			At:                 row.CreatedAt.Time,
			RequestID:          row.RequestID,
			FinalResult:        row.FinalResult.String,
			CandidateCount:     row.CandidateCount,
			EligibleCount:      row.EligibleCount,
			FallbackCount:      row.FallbackCount,
			CapacityWaitResult: row.CapacityWaitResult.String,
		}
		if row.SelectedChannelID.Valid {
			id := row.SelectedChannelID.Int64
			item.SelectedChannelID = &id
			item.SelectedChannelName = names[id]
		}
		out = append(out, item)
	}
	return out, total, nil
}

// capWindow 把请求窗口收进 StatsWindowCap，并报告是否发生了截断。
func capWindow(from, to time.Time) (time.Time, bool) {
	if to.Sub(from) <= StatsWindowCap {
		return from, false
	}
	return to.Add(-StatsWindowCap), true
}

func (s *Service) channelNames(
	ctx context.Context,
	selections []sqlc.ModelRoutingSelectionStatsRow,
	exclusions []sqlc.ModelRoutingExclusionStatsRow,
) (map[int64]string, error) {
	ids := make([]int64, 0, len(selections)+len(exclusions))
	for _, row := range selections {
		if row.SelectedChannelID.Valid {
			ids = append(ids, row.SelectedChannelID.Int64)
		}
	}
	for _, row := range exclusions {
		if row.SampleChannelID > 0 {
			ids = append(ids, row.SampleChannelID)
		}
	}
	return s.lookupChannelNames(ctx, ids)
}

func (s *Service) lookupChannelNames(ctx context.Context, ids []int64) (map[int64]string, error) {
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return map[int64]string{}, nil
	}
	rows, err := s.store.ListChannelNames(ctx, unique)
	if err != nil {
		return nil, opsutil.StoreFailed(err, "list channel names")
	}
	out := make(map[int64]string, len(rows))
	for _, row := range rows {
		out[row.ID] = row.Name
	}
	return out, nil
}

func tsParam(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}
