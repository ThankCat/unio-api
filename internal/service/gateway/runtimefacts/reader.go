// Package runtimefacts 从 PostgreSQL 强一致读取 Gateway 新准入所需的 P4 运行态版本事实。
package runtimefacts

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	coreruntimecontrol "github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

var (
	ErrRuntimeSyncRequired = errors.New("gateway runtime facts require synchronization")
	ErrRuntimeStateLost    = errors.New("gateway runtime state is not ready")
)

type Queries interface {
	GetAppSettingRecord(ctx context.Context, key string) (sqlc.GetAppSettingRecordRow, error)
	GetGatewayAdmissionControlRevisions(ctx context.Context) (sqlc.GetGatewayAdmissionControlRevisionsRow, error)
	GetGatewayRoutingControlRevisions(ctx context.Context) (sqlc.GetGatewayRoutingControlRevisionsRow, error)
}

type Integrity struct {
	Epoch    string
	Revision int64
}

type AdmissionRevisions struct {
	Integrity
	RouteRateLimits   int64
	ChannelRateLimits int64
	Concurrency       int64
}

type RoutingRevisions struct {
	Integrity
	CircuitBreaker int64
	RoutingBalance int64
}

type Reader struct {
	queries Queries
}

func NewReader(queries Queries) *Reader {
	if queries == nil {
		panic("runtimefacts: queries are required")
	}
	return &Reader{queries: queries}
}

// Integrity 强一致读取 PostgreSQL 完整性 epoch 保留行。既有 token/permit 的生命周期操作只依赖
// 该事实，不应因其它 runtime-control 行缺失而无法收口资源。
func (r *Reader) Integrity(ctx context.Context) (Integrity, error) {
	row, err := r.queries.GetAppSettingRecord(ctx, coreruntimecontrol.RuntimeStateEpochKey)
	if err != nil {
		return Integrity{}, classifyReadError(err, "integrity")
	}
	if row.Key != coreruntimecontrol.RuntimeStateEpochKey {
		return Integrity{}, runtimeSyncError("runtime state epoch row is invalid", fmt.Errorf("key=%q", row.Key))
	}
	return readyIntegrity(row.Value, row.Revision)
}

// Admission 在一条 SQL 中取得 epoch、线路/渠道默认限流与全局并发 revision。
func (r *Reader) Admission(ctx context.Context) (AdmissionRevisions, error) {
	row, err := r.queries.GetGatewayAdmissionControlRevisions(ctx)
	if err != nil {
		return AdmissionRevisions{}, classifyReadError(err, "admission")
	}
	integrity, err := readyIntegrity(row.RuntimeStateEpochValue, row.RuntimeStateEpochRevision)
	if err != nil {
		return AdmissionRevisions{}, err
	}
	if err := validRevisions(
		row.RouteRateLimitDefaultsRevision,
		row.ChannelRateLimitDefaultsRevision,
		row.ConcurrencyDefaultsRevision,
	); err != nil {
		return AdmissionRevisions{}, runtimeSyncError("admission control revision is invalid", err)
	}
	return AdmissionRevisions{
		Integrity:         integrity,
		RouteRateLimits:   row.RouteRateLimitDefaultsRevision,
		ChannelRateLimits: row.ChannelRateLimitDefaultsRevision,
		Concurrency:       row.ConcurrencyDefaultsRevision,
	}, nil
}

// Routing 在一条 SQL 中取得 epoch 与 circuit-breaker/routing-balance revision。
func (r *Reader) Routing(ctx context.Context) (RoutingRevisions, error) {
	row, err := r.queries.GetGatewayRoutingControlRevisions(ctx)
	if err != nil {
		return RoutingRevisions{}, classifyReadError(err, "routing")
	}
	integrity, err := readyIntegrity(row.RuntimeStateEpochValue, row.RuntimeStateEpochRevision)
	if err != nil {
		return RoutingRevisions{}, err
	}
	if err := validRevisions(row.CircuitBreakerRevision, row.RoutingBalanceRevision); err != nil {
		return RoutingRevisions{}, runtimeSyncError("routing control revision is invalid", err)
	}
	return RoutingRevisions{
		Integrity:      integrity,
		CircuitBreaker: row.CircuitBreakerRevision,
		RoutingBalance: row.RoutingBalanceRevision,
	}, nil
}

func readyIntegrity(raw []byte, revision int64) (Integrity, error) {
	if revision < 1 {
		return Integrity{}, runtimeSyncError("runtime state epoch revision is invalid", fmt.Errorf("revision=%d", revision))
	}
	epoch, err := coreruntimecontrol.DecodeStateEpoch(raw)
	if err != nil {
		return Integrity{}, runtimeSyncError("runtime state epoch payload is invalid", err)
	}
	if epoch.State != coreruntimecontrol.StateEpochReady {
		return Integrity{}, failure.Wrap(
			failure.CodeGatewayRuntimeStateLost,
			ErrRuntimeStateLost,
			failure.WithMessage("gateway runtime state is recovering"),
			failure.WithField("runtime_state_revision", revision),
		)
	}
	return Integrity{Epoch: epoch.Epoch, Revision: revision}, nil
}

func validRevisions(revisions ...int64) error {
	for _, revision := range revisions {
		if revision < 1 {
			return fmt.Errorf("revision=%d", revision)
		}
	}
	return nil
}

func classifyReadError(err error, target string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return runtimeSyncError(target+" runtime facts are missing", err)
	}
	return failure.Wrap(
		failure.CodeDependencyPostgresUnavailable,
		err,
		failure.WithMessage("gateway runtime facts database read failed"),
		failure.WithField("runtime_fact_target", target),
	)
}

func runtimeSyncError(message string, cause error) error {
	return failure.Wrap(
		failure.CodeGatewayRuntimeSyncRequired,
		fmt.Errorf("%w: %v", ErrRuntimeSyncRequired, cause),
		failure.WithMessage(message),
	)
}
