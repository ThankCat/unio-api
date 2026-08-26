package modelrouting

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/runtimefacts"
)

// TxBeginner 提供事务能力（由 pgxpool 满足）。实时候选要在一次快照里读齐候选与运行态，
// 避免边读边变导致候选集与运行态对不上。
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// RuntimeReader 提供 SnapshotMany 需要的 epoch 与 control revision。
// Gateway 在请求会话里冻结这些值；Admin 没有会话，只能现读。
type RuntimeReader interface {
	Routing(context.Context) (runtimefacts.RoutingRevisions, error)
	Admission(context.Context) (runtimefacts.AdmissionRevisions, error)
}

// BreakerReader 是选路观测需要的 Redis 只读能力。
//
// SnapshotMany 带 (channel, model) 权限判断与 revision 对齐结论，是候选视图的首选；
// ObserveChannels 不带 model 语义也不校验 revision，用作 SnapshotMany 整批失败时的降级，
// 以及全局流量视图的唯一来源（那里没有单一 model）。
type BreakerReader interface {
	SnapshotMany(ctx context.Context, in breakerstore.SnapshotManyInput) (breakerstore.SnapshotManyResult, error)
	ObserveChannels(ctx context.Context, channelIDs []int64) ([]breakerstore.ObservedChannelRuntime, error)
	AggregateChannelSamples(ctx context.Context, channelIDs []int64) (map[int64]breakerstore.ChannelSampleWindow, error)
}

// TPMReader 读取自然 UTC 分钟桶的 token 观测。它是 best-effort 的：
// 读不到只让对应列留空，不影响流量视图的其余部分。
type TPMReader interface {
	TPMObservations(
		ctx context.Context,
		kind breakerstore.TPMObservationKind,
		ids []int64,
		minute int64,
	) (map[int64]breakerstore.TPMObservationSnapshot, error)
}
