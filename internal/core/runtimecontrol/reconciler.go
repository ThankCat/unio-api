package runtimecontrol

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func pgTimestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// TargetResolver 依据 durable operation 行还原对应的 Redis ControlTarget（reconciler 用）。
type TargetResolver func(op sqlc.RuntimeControlOperation) (breakerstore.ControlTarget, bool)

// ReconcileControlAPI 是 reconciler 额外需要的 recovery-only 原子能力。
// 普通 Publisher 不能调用 Recover*；只有先读取 PostgreSQL durable operation 的恢复路径可用。
type ReconcileControlAPI interface {
	ControlAPI
	ReadControl(ctx context.Context, target breakerstore.ControlTarget, expectedRevision int64) (breakerstore.ControlSnapshot, error)
	RecoverCommittedControl(ctx context.Context, target breakerstore.ControlTarget, token string, currentRevision, nextRevision int64, payload string) (int64, error)
	RecoverAbortedControl(ctx context.Context, target breakerstore.ControlTarget, token string, currentRevision, nextRevision int64, pendingPayloadHash, currentPayload string) error
}

// Reconciler 对账未终结的 runtime_control_operations（§4.5.7、§5.3.16）：
//   - db_committed：Redis Commit 尚未确认 → 重试 CommitControl，成功后 operation→committed；
//   - preparing|prepared：业务 revision 未提交 → AbortControl + operation→aborted；
//   - epoch kind：跳过（由专用维护 use-case 处理，普通 reconciler 不碰）。
//
// 它不重建 marker，也不恢复 readiness；只把「响应丢失」的普通 control 发布收口到 PostgreSQL 权威事实。
type Reconciler struct {
	pool     *pgxpool.Pool
	control  ReconcileControlAPI
	resolve  TargetResolver
	retainMs int64
}

// NewReconciler 创建 reconciler。resolve 把 op 行映射到 ControlTarget。
func NewReconciler(pool *pgxpool.Pool, control ReconcileControlAPI, resolve TargetResolver) *Reconciler {
	if pool == nil || control == nil || resolve == nil {
		panic("runtimecontrol: reconciler requires pool, control and resolver")
	}
	return &Reconciler{pool: pool, control: control, resolve: resolve, retainMs: 24 * 60 * 60 * 1000}
}

// PayloadResolver 依据 PostgreSQL 当前业务行还原 active payload 正文。
// db_committed 时它必须是 next revision 的 payload；preparing|prepared 时必须是 current revision 的 payload。
type PayloadResolver func(ctx context.Context, op sqlc.RuntimeControlOperation) (payload string, ok bool, err error)

// ReconcileWithPayload 是携带 payload 还原的完整收口：db_committed→CommitControl→committed；
// preparing|prepared→AbortControl→aborted。payload 由 caller 依据业务当前事实还原（§4.5.7）。
func (r *Reconciler) ReconcileWithPayload(ctx context.Context, resolvePayload PayloadResolver) (int, error) {
	q := sqlc.New(r.pool)
	ops, err := q.ListNonterminalRuntimeControlOperations(ctx)
	if err != nil {
		return 0, err
	}
	handled := 0
	for _, op := range ops {
		if op.Kind == "runtime_state_epoch" {
			continue
		}
		target, ok := r.resolve(op)
		if !ok {
			return handled, fmt.Errorf("runtimecontrol: cannot resolve target for %s operation %s", op.Kind, op.Token)
		}
		payload, ok, err := resolvePayload(ctx, op)
		if err != nil {
			return handled, err
		}
		if !ok {
			return handled, fmt.Errorf("runtimecontrol: cannot resolve durable payload for operation %s", op.Token)
		}
		snapshot, err := r.control.ReadControl(ctx, target, 0)
		if err != nil {
			return handled, err
		}
		switch op.State {
		case "db_committed":
			if breakerstore.HashPayload(payload) != op.PayloadHash {
				return handled, fmt.Errorf("runtimecontrol: db_committed payload hash mismatch for operation %s", op.Token)
			}
			if snapshot.PendingRevision != 0 && (snapshot.PendingRevision != op.NextRevision || breakerstore.HashPayload(snapshot.PendingPayload) != op.PayloadHash) {
				return handled, fmt.Errorf("runtimecontrol: conflicting redis pending state for operation %s", op.Token)
			}
			if _, err := r.control.RecoverCommittedControl(ctx, target, op.Token, op.CurrentRevision, op.NextRevision, payload); err != nil {
				return handled, err
			}
			if err := markOperationTerminal(ctx, q, op, "committed"); err != nil {
				return handled, err
			}
			handled++
		case "preparing", "prepared":
			// 业务未提交：payload 是 PostgreSQL current active；待撤销的新 payload 只能取 Redis pending，
			// 不能误用旧业务 payload 去算 pending hash。
			if snapshot.PendingRevision != 0 && (snapshot.PendingRevision != op.NextRevision || breakerstore.HashPayload(snapshot.PendingPayload) != op.PayloadHash) {
				return handled, fmt.Errorf("runtimecontrol: conflicting redis pending state for operation %s", op.Token)
			}
			if err := r.control.RecoverAbortedControl(ctx, target, op.Token, op.CurrentRevision, op.NextRevision, op.PayloadHash, payload); err != nil {
				return handled, err
			}
			if err := markOperationTerminal(ctx, q, op, "aborted"); err != nil {
				return handled, err
			}
			handled++
		default:
			return handled, fmt.Errorf("runtimecontrol: unsupported nonterminal state %q for operation %s", op.State, op.Token)
		}
	}
	return handled, nil
}

func markOperationTerminal(ctx context.Context, q *sqlc.Queries, op sqlc.RuntimeControlOperation, state string) error {
	var (
		rows int64
		err  error
	)
	switch state {
	case "committed":
		rows, err = q.MarkRuntimeControlOperationCommitted(ctx, sqlc.MarkRuntimeControlOperationCommittedParams{
			Token: op.Token, PayloadHash: op.PayloadHash,
		})
	case "aborted":
		rows, err = q.MarkRuntimeControlOperationAborted(ctx, sqlc.MarkRuntimeControlOperationAbortedParams{
			Token: op.Token, PayloadHash: op.PayloadHash,
		})
	default:
		return fmt.Errorf("runtimecontrol: invalid terminal state %q", state)
	}
	if err != nil {
		return err
	}
	if rows == 1 {
		return nil
	}
	current, getErr := q.GetRuntimeControlOperationByToken(ctx, op.Token)
	if getErr == nil && current.PayloadHash == op.PayloadHash && current.State == state {
		return nil // 另一 reconciler 已完成同一终态。
	}
	return fmt.Errorf("runtimecontrol: operation %s did not transition to %s", op.Token, state)
}

// CleanupTerminal 删除早于保留期（默认 24h）的终态操作。
func (r *Reconciler) CleanupTerminal(ctx context.Context, now time.Time) (int64, error) {
	q := sqlc.New(r.pool)
	cutoff := now.Add(-time.Duration(r.retainMs) * time.Millisecond)
	return q.DeleteTerminalRuntimeControlOperationsBefore(ctx, pgTimestamptz(cutoff))
}
