// Package runtimecontrol 是 P4 可恢复发布的应用层编排（§4.5、§5.3.16）：把 PostgreSQL
// runtime_control_operations 状态机（preparing→prepared→db_committed→committed）与 Redis BreakerStore
// 的 Prepare/Commit/Abort 串成一次原子可恢复的控制发布。Admin 与 Worker 共用同一 Publisher/Reconciler。
//
// 顺序（§4.4/§4.5）：先写 PostgreSQL preparing operation；Redis Prepare 校验 next=current+1 与 payload hash；
// prepare 成功后 CAS operation→prepared；随后在同一 PostgreSQL 事务提交业务行（值/revision）并把 operation
// →db_committed；最后 Redis Commit 激活 control 并把 operation→committed。数据库未提交时只能 Abort；
// 数据库已提交但 Redis commit 响应丢失时返回 runtime_sync_pending，由 reconciler 依据 PostgreSQL 事实重试 Commit。
package runtimecontrol

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// ControlAPI 是 Publisher 依赖的 Redis 控制能力子集（由 *breakerstore.Store 实现）。
type ControlAPI interface {
	PrepareControl(ctx context.Context, target breakerstore.ControlTarget, token string, currentRevision, nextRevision int64, payload string) (breakerstore.ControlPrepareResult, int64, error)
	CommitControl(ctx context.Context, target breakerstore.ControlTarget, token, payload string) (int64, error)
	AbortControl(ctx context.Context, target breakerstore.ControlTarget, token, payload string) error
}

// Kind 是发布目标类别，与 runtime_control_operations.kind 一致（epoch 由专用维护 use-case 处理，不走本 Publisher）。
const (
	KindChannelAdmissionLimits = "channel_admission_limits"
	KindAppSetting             = "app_setting"
)

// PublishState 是一次发布的最终应用态。
type PublishState string

const (
	// PublishCommitted：Redis 与 PostgreSQL 均已提交并终结。
	PublishCommitted PublishState = "committed"
	// PublishRuntimeSyncPending：业务行已提交但 Redis Commit 未确认，交由 reconciler 收口。
	PublishRuntimeSyncPending PublishState = "runtime_sync_pending"
	// PublishAborted：业务未提交，已安全撤销。
	PublishAborted PublishState = "aborted"
)

// PublishRequest 描述一次控制发布。
type PublishRequest struct {
	Kind    string
	Target  breakerstore.ControlTarget
	Token   string
	Payload string // 规范化目标 payload（其 SHA-256 作为 payload_hash）

	CurrentRevision int64
	NextRevision    int64

	// 目标定位（与 kind 对应，用于 durable operation 行）。
	ChannelID  *int64
	SettingKey *string

	// BusinessCommit 在同一 PostgreSQL 业务事务中提交业务行（app_settings 值/revision、
	// channels admission_limits_revision 等）。它与 operation→db_committed 一起原子提交。
	BusinessCommit func(ctx context.Context, tx pgx.Tx) error
}

// PublishResult 汇报发布结果。
type PublishResult struct {
	State          PublishState
	ActiveRevision int64
}

// Publisher 编排 durable control 发布。
type Publisher struct {
	pool    *pgxpool.Pool
	control ControlAPI
}

// NewPublisher 创建 Publisher。
func NewPublisher(pool *pgxpool.Pool, control ControlAPI) *Publisher {
	if pool == nil || control == nil {
		panic("runtimecontrol: publisher requires pool and control")
	}
	return &Publisher{pool: pool, control: control}
}

// Publish 执行一次完整的可恢复控制发布。
func (p *Publisher) Publish(ctx context.Context, req PublishRequest) (PublishResult, error) {
	if req.Kind != KindChannelAdmissionLimits && req.Kind != KindAppSetting {
		return PublishResult{}, failure.New(failure.CodeConfigInvalid, failure.WithMessage("runtimecontrol: unsupported publish kind"))
	}
	if req.Token == "" || req.BusinessCommit == nil {
		return PublishResult{}, failure.New(failure.CodeConfigInvalid, failure.WithMessage("runtimecontrol: token and business commit are required"))
	}
	if req.NextRevision != req.CurrentRevision+1 {
		return PublishResult{}, failure.New(failure.CodeConfigInvalid, failure.WithMessage("runtimecontrol: next revision must be current+1"))
	}

	payloadHash := breakerstore.HashPayload(req.Payload)
	q := sqlc.New(p.pool)

	// 1) 写 PostgreSQL preparing operation；同 token 重试时先核对不可变字段，再从 durable state 续接。
	op, err := q.CreateRuntimeControlOperation(ctx, sqlc.CreateRuntimeControlOperationParams{
		Token:           req.Token,
		Kind:            req.Kind,
		ChannelID:       int8OrNull(req.ChannelID),
		SettingKey:      textOrNull(req.SettingKey),
		CurrentRevision: req.CurrentRevision,
		NextRevision:    req.NextRevision,
		PayloadHash:     payloadHash,
	})
	if err != nil {
		op, err = q.GetRuntimeControlOperationByToken(ctx, req.Token)
		if err != nil {
			return PublishResult{}, failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: create or load operation"))
		}
	}
	if !sameOperation(op, req, payloadHash) {
		return PublishResult{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("runtimecontrol: operation token conflicts with immutable request"),
		)
	}
	switch op.State {
	case "committed":
		return PublishResult{State: PublishCommitted, ActiveRevision: req.NextRevision}, nil
	case "db_committed":
		activeRev, commitErr := p.control.CommitControl(ctx, req.Target, req.Token, req.Payload)
		if commitErr != nil {
			return PublishResult{State: PublishRuntimeSyncPending}, nil
		}
		rows, markErr := q.MarkRuntimeControlOperationCommitted(ctx, sqlc.MarkRuntimeControlOperationCommittedParams{
			Token: req.Token, PayloadHash: payloadHash,
		})
		if markErr != nil || rows != 1 {
			return PublishResult{State: PublishRuntimeSyncPending, ActiveRevision: activeRev}, nil
		}
		return PublishResult{State: PublishCommitted, ActiveRevision: activeRev}, nil
	case "aborted":
		return PublishResult{State: PublishAborted}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("runtimecontrol: operation is already aborted"),
		)
	case "preparing", "prepared":
		// 从对应崩溃点继续。
	default:
		return PublishResult{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("runtimecontrol: operation has invalid durable state"),
		)
	}

	// 2) Redis Prepare pending fence。
	prep, _, err := p.control.PrepareControl(ctx, req.Target, req.Token, req.CurrentRevision, req.NextRevision, req.Payload)
	if err != nil {
		// 基础设施故障：业务未提交，安全 Abort（DB + 尽力 Redis），上抛 503。
		_ = p.abort(ctx, q, req, payloadHash)
		return PublishResult{}, err
	}
	switch prep {
	case breakerstore.ControlPrepared:
		// 继续。
	case breakerstore.ControlPrepareCommitted:
		// Redis 已激活 next、但 durable operation 仍在业务提交前，属于不可能由本协议产生的分叉。
		// 绝不能再次执行 BusinessCommit 猜测数据库事实；保持非终态并交由严格 reconciler 隔离。
		return PublishResult{State: PublishRuntimeSyncPending, ActiveRevision: req.NextRevision}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("runtimecontrol: redis committed before durable business state"),
		)
	default:
		// stale/conflict/invalid：业务未提交，Abort。
		_ = p.abort(ctx, q, req, payloadHash)
		return PublishResult{State: PublishAborted}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage(fmt.Sprintf("runtimecontrol: prepare rejected (%s)", prep)),
		)
	}

	// 3) CAS operation preparing→prepared。
	if _, err := q.MarkRuntimeControlOperationPrepared(ctx, sqlc.MarkRuntimeControlOperationPreparedParams{Token: req.Token, PayloadHash: payloadHash}); err != nil {
		_ = p.abort(ctx, q, req, payloadHash)
		return PublishResult{}, failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: mark prepared"))
	}

	// 4) 业务事务：提交业务行 + operation→db_committed。
	if err := p.commitBusiness(ctx, req, payloadHash); err != nil {
		_ = p.abort(ctx, q, req, payloadHash)
		return PublishResult{}, err
	}

	// 5) Redis Commit 激活 control。
	activeRev, err := p.control.CommitControl(ctx, req.Target, req.Token, req.Payload)
	if err != nil {
		// 业务已提交但 Redis commit 未确认：返回 pending，由 reconciler 依据 db_committed 重试 Commit。
		return PublishResult{State: PublishRuntimeSyncPending}, nil
	}

	// 6) operation db_committed→committed。
	if _, err := q.MarkRuntimeControlOperationCommitted(ctx, sqlc.MarkRuntimeControlOperationCommittedParams{Token: req.Token, PayloadHash: payloadHash}); err != nil {
		return PublishResult{State: PublishRuntimeSyncPending, ActiveRevision: activeRev}, nil
	}
	return PublishResult{State: PublishCommitted, ActiveRevision: activeRev}, nil
}

// commitBusiness 在单个 PostgreSQL 事务中执行业务提交并把 operation 推进到 db_committed。
func (p *Publisher) commitBusiness(ctx context.Context, req PublishRequest, payloadHash string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: begin business tx"))
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := req.BusinessCommit(ctx, tx); err != nil {
		return err
	}
	qtx := sqlc.New(tx)
	rows, err := qtx.MarkRuntimeControlOperationDBCommitted(ctx, sqlc.MarkRuntimeControlOperationDBCommittedParams{Token: req.Token, PayloadHash: payloadHash})
	if err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: mark db_committed"))
	}
	if rows != 1 {
		return failure.New(failure.CodeConfigInvalid, failure.WithMessage("runtimecontrol: operation not in prepared state"))
	}
	if err := tx.Commit(ctx); err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: commit business tx"))
	}
	return nil
}

// abort 撤销未提交的发布：Redis AbortControl + PostgreSQL operation→aborted（best-effort）。
func (p *Publisher) abort(ctx context.Context, q *sqlc.Queries, req PublishRequest, payloadHash string) error {
	_ = p.control.AbortControl(ctx, req.Target, req.Token, req.Payload)
	_, err := q.MarkRuntimeControlOperationAborted(ctx, sqlc.MarkRuntimeControlOperationAbortedParams{Token: req.Token, PayloadHash: payloadHash})
	return err
}

func int8OrNull(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

func textOrNull(v *string) pgtype.Text {
	if v == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *v, Valid: true}
}

func sameOperation(op sqlc.RuntimeControlOperation, req PublishRequest, payloadHash string) bool {
	return op.Token == req.Token &&
		op.Kind == req.Kind &&
		op.CurrentRevision == req.CurrentRevision &&
		op.NextRevision == req.NextRevision &&
		op.PayloadHash == payloadHash &&
		sameNullableInt64(op.ChannelID, req.ChannelID) &&
		sameNullableString(op.SettingKey, req.SettingKey)
}

func sameNullableInt64(got pgtype.Int8, want *int64) bool {
	if want == nil {
		return !got.Valid
	}
	return got.Valid && got.Int64 == *want
}

func sameNullableString(got pgtype.Text, want *string) bool {
	if want == nil {
		return !got.Valid
	}
	return got.Valid && got.String == *want
}

var _ = errors.Is
