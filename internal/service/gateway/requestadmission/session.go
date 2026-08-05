// Package requestadmission owns one customer request's ingress admission token lifecycle.
package requestadmission

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/logfields"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/runtimefacts"
)

const (
	defaultOperationTimeout = 2 * time.Second
	defaultRenewInterval    = 10 * time.Second
	minimumRenewInterval    = 10 * time.Millisecond
	requestTerminalTries    = 2
)

var (
	ErrInvalidIdentity  = errors.New("request admission identity is invalid")
	ErrBindConflict     = errors.New("attempt binding conflicts with its request admission token")
	ErrUnknownAdmission = errors.New("request admission token is unknown")
)

// Store is the narrow BreakerStore contract owned by an ingress request session.
type Store interface {
	AcquireRequestAdmission(context.Context, breakerstore.RequestAdmissionInput) (breakerstore.RequestAdmissionResult, error)
	RenewRequestAdmission(context.Context, string, int64, int64, string, int64) (breakerstore.RequestAdmissionLifecycleOutcome, error)
	FinishRequestAdmission(context.Context, string, int64, int64, string, int64) (breakerstore.RequestAdmissionLifecycleOutcome, error)
	SnapshotMany(context.Context, breakerstore.SnapshotManyInput) (breakerstore.SnapshotManyResult, error)
	AggregateChannelSamples(context.Context, []int64) (map[int64]breakerstore.ChannelSampleWindow, error)
}

// RuntimeFactsReader strongly reads the PostgreSQL revisions expected by a new admission.
type RuntimeFactsReader interface {
	Integrity(context.Context) (runtimefacts.Integrity, error)
	Admission(context.Context) (runtimefacts.AdmissionRevisions, error)
	Routing(context.Context) (runtimefacts.RoutingRevisions, error)
}

// MetricsRecorder is the bounded observability contract for request-token ownership.
// IDs and error text deliberately stay out of metric labels.
type MetricsRecorder interface {
	IncRequestAdmissionOperation(operation, result string)
	AddRequestAdmissionActive(delta float64)
}

// RequestSession is the only request-admission capability exposed to gateway services.
// Finalization deliberately is not part of it: only the HTTP route wrapper may close a token.
//
// 这里没有任何 token 用量方法：TPM 不参与准入，观测走独立的 obs:tpm 分钟桶（§8）。
type RequestSession interface {
	AttemptTokenSession
	CandidateSnapshotSession
}

// AttemptTokenSession lets the lifecycle permit manager bind the opaque request token
// without exposing a raw request-admission ID getter to protocol services.
type AttemptTokenSession interface {
	BindAttempt(*breakerstore.AcquireAttemptInput) error
}

// CandidateSnapshotSession owns the frozen admission revisions and injects fresh routing revisions
// without exposing the request-admission ID to protocol or lifecycle callers.
// AggregateChannelSamples 提供 30 分钟评分样本聚合（§12）：观测口径，与 admission 硬门槛解耦。
type CandidateSnapshotSession interface {
	SnapshotMany(context.Context, int64, []breakerstore.SnapshotCandidateInput) (breakerstore.SnapshotManyResult, error)
	AggregateChannelSamples(context.Context, []int64) (map[int64]breakerstore.ChannelSampleWindow, error)
}

// AcquiredSession is retained only by the HTTP route wrapper.
type AcquiredSession interface {
	Request() RequestSession
	StopRenewer()
	Finalize(context.Context) error
}

// Identity is the immutable, trusted request identity used to acquire one token.
// Limit overrides come directly from the authenticated route snapshot: nil inherits the
// Redis active global control, zero is explicitly unlimited, and a positive value is a limit.
type Identity struct {
	RouteID int64
	UserID  int64
	Scope   string

	RPMLimitOverride         *int64
	RPDLimitOverride         *int64
	ConcurrencyLimitOverride *int64
}

// AcquireResult keeps business denial separate from Store/infrastructure errors.
type AcquireResult struct {
	Outcome          breakerstore.RequestAdmissionOutcome
	LimitedDimension string
	SyncTarget       string
	Session          AcquiredSession
}

// ManagerOptions configures bounded token operations. RenewInterval is primarily useful
// for deterministic tests; zero derives an interval from the server-authoritative lease.
type ManagerOptions struct {
	Logger           *zap.Logger
	Metrics          MetricsRecorder
	OperationTimeout time.Duration
	RenewInterval    time.Duration
}

// Manager acquires request tokens and creates their request-scoped session owner.
type Manager struct {
	store            Store
	facts            RuntimeFactsReader
	logger           *zap.Logger
	metrics          MetricsRecorder
	operationTimeout time.Duration
	renewInterval    time.Duration
	now              func() time.Time
	newID            func() string
}

// NewManager creates the protocol-independent request admission manager.
func NewManager(store Store, facts RuntimeFactsReader, opts ManagerOptions) *Manager {
	if store == nil {
		panic("requestadmission: store is required")
	}
	if facts == nil {
		panic("requestadmission: runtime facts reader is required")
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.OperationTimeout <= 0 {
		opts.OperationTimeout = defaultOperationTimeout
	}
	return &Manager{
		store:            store,
		facts:            facts,
		logger:           opts.Logger,
		metrics:          opts.Metrics,
		operationTimeout: opts.OperationTimeout,
		renewInterval:    opts.RenewInterval,
		now:              time.Now,
		newID:            uuid.NewString,
	}
}

// Acquire reads one coherent PostgreSQL revision snapshot, asks Redis for the token, and
// starts the token renewer only after an allowed result.
func (m *Manager) Acquire(ctx context.Context, identity Identity) (AcquireResult, error) {
	if identity.RouteID <= 0 || identity.UserID <= 0 || identity.Scope == "" {
		err := failure.Wrap(
			failure.CodeGatewayRuntimeSyncRequired,
			ErrInvalidIdentity,
			failure.WithMessage("request admission identity is invalid"),
		)
		m.recordOperation("acquire", admissionErrorResult(err))
		return AcquireResult{}, err
	}

	admission, err := m.facts.Admission(ctx)
	if err != nil {
		m.recordOperation("acquire", admissionErrorResult(err))
		return AcquireResult{}, err
	}

	requestAdmissionID := m.newID()
	input := breakerstore.RequestAdmissionInput{
		RequestAdmissionID:        requestAdmissionID,
		RouteID:                   identity.RouteID,
		UserID:                    identity.UserID,
		IntegrityEpoch:            admission.Epoch,
		IntegrityRevision:         admission.Revision,
		RouteRateRevision:         admission.RouteRateLimits,
		GlobalConcurrencyRevision: admission.Concurrency,
		RPMLimitOverride:          cloneLimit(identity.RPMLimitOverride),
		RPDLimitOverride:          cloneLimit(identity.RPDLimitOverride),
		ConcurrencyLimitOverride:  cloneLimit(identity.ConcurrencyLimitOverride),
	}
	input.Fingerprint = admissionFingerprint(input, identity.Scope)

	result, err := m.store.AcquireRequestAdmission(ctx, input)
	if err != nil {
		m.recordOperation("acquire", admissionErrorResult(err))
		return AcquireResult{}, err
	}
	m.recordOperation("acquire", string(result.Outcome))
	out := AcquireResult{
		Outcome:          result.Outcome,
		LimitedDimension: result.LimitedDimension,
		SyncTarget:       result.SyncTarget,
	}
	if result.Outcome != breakerstore.RequestAllowed {
		fields := []zap.Field{
			zap.Int64("route_id", identity.RouteID),
			zap.Int64("user_id", identity.UserID),
			zap.String("outcome", string(result.Outcome)),
			zap.String("limited_dimension", result.LimitedDimension),
			zap.String("sync_target", result.SyncTarget),
		}
		if requestFields, ok := logfields.FromContext(ctx); ok {
			fields = append(requestFields.ZapFields(), fields...)
		}
		logging.Warn(m.logger, "admission", "request", "request admission rejected", fields...)
		return out, nil
	}

	interval := m.renewInterval
	if interval <= 0 {
		interval = time.Duration(result.RenewIntervalMs) * time.Millisecond
		if interval <= 0 {
			interval = deriveRenewInterval(m.now(), result.LeaseUntilMs)
		}
	}
	s := &session{
		store:                      m.store,
		facts:                      m.facts,
		logger:                     m.logger,
		metrics:                    m.metrics,
		requestID:                  requestAdmissionID,
		routeID:                    identity.RouteID,
		userID:                     identity.UserID,
		integrity:                  admission.Integrity,
		routeRateRevision:          admission.RouteRateLimits,
		requestConcurrencyRevision: admission.Concurrency,
		operationTimeout:           m.operationTimeout,
		renewInterval:              interval,
		stop:                       make(chan struct{}),
		renewerDone:                make(chan struct{}),
	}
	if requestFields, ok := logfields.FromContext(ctx); ok {
		s.logFields = requestFields
	}
	if m.metrics != nil {
		m.metrics.AddRequestAdmissionActive(1)
	}
	s.startRenewer()
	logging.Debug(m.logger, "admission", "request", "request admission acquired",
		s.logData(
			zap.Int64("route_id", identity.RouteID), zap.Int64("user_id", identity.UserID),
			zap.String("scope", identity.Scope), zap.Int64("lease_until", result.LeaseUntilMs),
			zap.Int64("renew_interval_ms", interval.Milliseconds()),
			zap.Int64("rate_revision", admission.RouteRateLimits),
			zap.Int64("concurrency_revision", admission.Concurrency),
		)...,
	)
	out.Session = s
	return out, nil
}

func (m *Manager) recordOperation(operation, result string) {
	if m != nil && m.metrics != nil {
		m.metrics.IncRequestAdmissionOperation(operation, result)
	}
}

type session struct {
	store                      Store
	facts                      RuntimeFactsReader
	logger                     *zap.Logger
	logFields                  *logfields.Fields
	metrics                    MetricsRecorder
	requestID                  string
	routeID                    int64
	userID                     int64
	integrity                  runtimefacts.Integrity
	routeRateRevision          int64
	requestConcurrencyRevision int64
	operationTimeout           time.Duration
	renewInterval              time.Duration

	stopOnce    sync.Once
	stop        chan struct{}
	renewerDone chan struct{}

	stateMu   sync.Mutex
	finalized bool

	finalizeOnce sync.Once
	finalizeErr  error
}

func (s *session) Request() RequestSession { return s }

func (s *session) StopRenewer() { s.stopRenewer() }

// BindAttempt injects the opaque request token into one candidate admission.
// 它只校验 token 未终态且没有绑到别的请求上：输入估算不再需要预占，因此也不再比较。
func (s *session) BindAttempt(input *breakerstore.AcquireAttemptInput) error {
	if input == nil {
		return bindConflictError()
	}
	s.stateMu.Lock()
	finalized := s.finalized
	s.stateMu.Unlock()
	if finalized {
		return failure.Wrap(
			failure.CodeGatewayRuntimeSyncRequired,
			ErrUnknownAdmission,
			failure.WithMessage("request admission session is already finalized"),
		)
	}
	if input.RequestAdmissionID != "" && input.RequestAdmissionID != s.requestID {
		return bindConflictError()
	}
	input.RequestAdmissionID = s.requestID
	input.RouteID = s.routeID
	return nil
}

// SnapshotMany keeps only the ingress-frozen route-rate revision for observability. Channel-rate,
// channel concurrency, breaker, and balance revisions are read for the candidate phase. A concurrent
// integrity epoch change is rejected before Redis.
func (s *session) SnapshotMany(ctx context.Context, modelID int64, candidates []breakerstore.SnapshotCandidateInput) (breakerstore.SnapshotManyResult, error) {
	admission, err := s.facts.Admission(ctx)
	if err != nil {
		return breakerstore.SnapshotManyResult{}, err
	}
	routing, err := s.facts.Routing(ctx)
	if err != nil {
		return breakerstore.SnapshotManyResult{}, err
	}
	if admission.Integrity != s.integrity || routing.Integrity != s.integrity {
		return breakerstore.SnapshotManyResult{}, failure.New(
			failure.CodeGatewayRuntimeStateLost,
			failure.WithMessage("candidate snapshot integrity epoch changed after request admission"),
		)
	}
	result, err := s.store.SnapshotMany(ctx, breakerstore.SnapshotManyInput{
		IntegrityEpoch:            s.integrity.Epoch,
		IntegrityRevision:         s.integrity.Revision,
		GlobalConcurrencyRevision: admission.Concurrency,
		CircuitBreakerRevision:    routing.CircuitBreaker,
		RoutingBalanceRevision:    routing.RoutingBalance,
		ModelID:                   modelID,
		Candidates:                candidates,
	})
	if err != nil {
		return breakerstore.SnapshotManyResult{}, err
	}
	result.RouteRateRevision = s.routeRateRevision
	return result, nil
}

// AggregateChannelSamples 读取候选渠道最近 30 分钟评分样本聚合（§12）。
// 观测与 admission 解耦：不校验 integrity epoch，也不因读失败阻断选路。
func (s *session) AggregateChannelSamples(ctx context.Context, channelIDs []int64) (map[int64]breakerstore.ChannelSampleWindow, error) {
	return s.store.AggregateChannelSamples(ctx, channelIDs)
}

// Finalize stops and joins the renewer before invoking the token's only terminal API.
// sync.Once makes duplicate route cleanup return the first terminal result without another Store call.
func (s *session) Finalize(ctx context.Context) error {
	s.finalizeOnce.Do(func() {
		s.stopRenewer()
		if s.metrics != nil {
			defer s.metrics.AddRequestAdmissionActive(-1)
		}

		s.stateMu.Lock()
		s.finalized = true
		s.stateMu.Unlock()

		var outcome breakerstore.RequestAdmissionLifecycleOutcome
		var terminalErr error
		storeResultUnknown := false
		for attempt := 0; attempt < requestTerminalTries; attempt++ {
			integrity, err := s.facts.Integrity(ctx)
			if err == nil {
				outcome, err = s.store.FinishRequestAdmission(
					ctx, s.requestID, s.routeID, s.userID, integrity.Epoch, integrity.Revision,
				)
				if err != nil && retryableLifecycleError(err) {
					storeResultUnknown = true
				}
			}
			if err == nil {
				terminalErr = nil
				break
			}
			terminalErr = err
			if !retryableLifecycleError(err) {
				break
			}
		}
		if terminalErr != nil {
			s.finalizeErr = terminalErr
			result := admissionErrorResult(terminalErr)
			if storeResultUnknown {
				result = "result_unknown"
			}
			s.recordOperation("finish", result)
			return
		}
		s.recordOperation("finish", string(outcome))
		if outcome != breakerstore.RequestLifecycleFinished && outcome != breakerstore.RequestLifecycleTerminal {
			s.finalizeErr = requestLifecycleError("finish", outcome)
			logging.Error(s.logger, "admission", "request", "request admission finalize rejected",
				s.logData(
					zap.Int64("route_id", s.routeID), zap.Int64("user_id", s.userID),
					zap.String("outcome", string(outcome)),
				)...,
			)
			return
		}
		logging.Debug(s.logger, "admission", "request", "request admission finalized",
			s.logData(zap.String("outcome", string(outcome)))...,
		)
	})
	return s.finalizeErr
}

func (s *session) startRenewer() {
	go func() {
		defer close(s.renewerDone)
		ticker := time.NewTicker(s.renewInterval)
		defer ticker.Stop()

		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				renewCtx, cancel := context.WithTimeout(context.Background(), s.operationTimeout)
				integrity, err := s.facts.Integrity(renewCtx)
				result := ""
				if err == nil {
					var outcome breakerstore.RequestAdmissionLifecycleOutcome
					outcome, err = s.store.RenewRequestAdmission(
						renewCtx,
						s.requestID,
						s.routeID,
						s.userID,
						integrity.Epoch,
						integrity.Revision,
					)
					if err == nil {
						result = string(outcome)
					}
					if err == nil && outcome != breakerstore.RequestLifecycleRenewed {
						err = requestLifecycleError("renew", outcome)
					}
				}
				cancel()
				if result == "" {
					result = admissionErrorResult(err)
				}
				s.recordOperation("renew", result)
				if err != nil {
					fields := failure.LogFields(err)
					logging.Warn(s.logger, "admission", "request", "request admission renew failed", s.logData(fields...)...)
				}
			}
		}
	}()
}

func (s *session) logData(fields ...zap.Field) []zap.Field {
	if s != nil && s.logFields != nil {
		return append(s.logFields.ZapFields(), fields...)
	}
	return fields
}

func (s *session) recordOperation(operation, result string) {
	if s != nil && s.metrics != nil {
		s.metrics.IncRequestAdmissionOperation(operation, result)
	}
}

func admissionErrorResult(err error) string {
	switch failure.CodeOf(err) {
	case failure.CodeGatewayRuntimeStateLost:
		return "runtime_state_lost"
	case failure.CodeGatewayRuntimeSyncRequired:
		return "runtime_sync_required"
	case failure.CodeGatewayBreakerStoreUnavailable, failure.CodeDependencyRedisUnavailable:
		return "store_unavailable"
	case failure.CodeDependencyPostgresUnavailable:
		return "postgres_unavailable"
	default:
		return "error"
	}
}

func retryableLifecycleError(err error) bool {
	if err == nil {
		return false
	}
	code := failure.CodeOf(err)
	return code == failure.CodeGatewayBreakerStoreUnavailable ||
		code == failure.CodeDependencyRedisUnavailable ||
		code == failure.CodeDependencyPostgresUnavailable ||
		errors.Is(err, breakerstore.ErrStoreUnavailable)
}

func (s *session) stopRenewer() {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.renewerDone
}

func deriveRenewInterval(now time.Time, leaseUntilMs int64) time.Duration {
	remaining := time.UnixMilli(leaseUntilMs).Sub(now)
	if remaining <= 0 {
		return minimumRenewInterval
	}
	interval := remaining / 3
	if interval < minimumRenewInterval {
		return minimumRenewInterval
	}
	if interval > defaultRenewInterval {
		return defaultRenewInterval
	}
	return interval
}

func admissionFingerprint(in breakerstore.RequestAdmissionInput, scope string) string {
	h := sha256.New()
	fmt.Fprintf(h, "id=%s;route=%d;user=%d;scope=%s;epoch=%s;epoch_rev=%d;rate_rev=%d;concurrency_rev=%d;",
		in.RequestAdmissionID, in.RouteID, in.UserID, scope, in.IntegrityEpoch, in.IntegrityRevision,
		in.RouteRateRevision, in.GlobalConcurrencyRevision)
	writeLimitFingerprint(h, "rpm", in.RPMLimitOverride)
	writeLimitFingerprint(h, "rpd", in.RPDLimitOverride)
	writeLimitFingerprint(h, "concurrency", in.ConcurrencyLimitOverride)
	return fmt.Sprintf("%x", h.Sum(nil))
}

type stringWriter interface {
	Write([]byte) (int, error)
}

func writeLimitFingerprint(w stringWriter, name string, value *int64) {
	encoded := "inherit"
	if value != nil {
		encoded = strconv.FormatInt(*value, 10)
	}
	_, _ = fmt.Fprintf(w, "%s=%s;", name, encoded)
}

func cloneLimit(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func bindConflictError() error {
	return failure.Wrap(
		failure.CodeGatewayBreakerPermitConflict,
		ErrBindConflict,
		failure.WithMessage("attempt binding conflicts with its request admission token"),
	)
}

func requestLifecycleError(operation string, outcome breakerstore.RequestAdmissionLifecycleOutcome) error {
	code := failure.CodeGatewayRuntimeSyncRequired
	switch outcome {
	case breakerstore.RequestLifecycleRuntimeStateLost:
		code = failure.CodeGatewayRuntimeStateLost
	case breakerstore.RequestLifecycleConflict:
		code = failure.CodeGatewayBreakerPermitConflict
	}
	return failure.Wrap(
		code,
		ErrUnknownAdmission,
		failure.WithMessage("request admission lifecycle operation was rejected"),
		failure.WithField("operation", operation),
		failure.WithField("outcome", string(outcome)),
	)
}

type requestSessionContextKey struct{}

type contextSessions struct {
	attempt  AttemptTokenSession
	snapshot CandidateSnapshotSession
}

// ContextWithRequestSession exposes only attempt binding and candidate snapshot to downstream services.
func ContextWithRequestSession(ctx context.Context, requestSession RequestSession) context.Context {
	if requestSession == nil {
		return ctx
	}
	return context.WithValue(ctx, requestSessionContextKey{}, contextSessions{
		attempt:  requestSession,
		snapshot: requestSession,
	})
}

// BindAttemptInput is the lifecycle permit manager's only way to supply the opaque request token.
func BindAttemptInput(ctx context.Context, input *breakerstore.AcquireAttemptInput) error {
	bundle, ok := ctx.Value(requestSessionContextKey{}).(contextSessions)
	if !ok || bundle.attempt == nil {
		return failure.Wrap(
			failure.CodeGatewayRuntimeSyncRequired,
			ErrUnknownAdmission,
			failure.WithMessage("request admission attempt capability is missing"),
		)
	}
	return bundle.attempt.BindAttempt(input)
}

// SnapshotManyIfPresent lets shared candidate preparation consume the request-owned snapshot
// capability. present=false is reserved for direct unit tests and maintenance callers.
func SnapshotManyIfPresent(ctx context.Context, modelID int64, candidates []breakerstore.SnapshotCandidateInput) (breakerstore.SnapshotManyResult, bool, error) {
	bundle, ok := ctx.Value(requestSessionContextKey{}).(contextSessions)
	if !ok || bundle.snapshot == nil {
		return breakerstore.SnapshotManyResult{}, false, nil
	}
	result, err := bundle.snapshot.SnapshotMany(ctx, modelID, candidates)
	return result, true, err
}

// AggregateChannelSamplesIfPresent 读取候选渠道最近 30 分钟评分样本聚合（§12）。
// 观测是 best-effort：读失败或无 session 时返回空聚合，评分按「无样本得满分」处理，不阻断选路。
func AggregateChannelSamplesIfPresent(ctx context.Context, channelIDs []int64) map[int64]breakerstore.ChannelSampleWindow {
	bundle, ok := ctx.Value(requestSessionContextKey{}).(contextSessions)
	if !ok || bundle.snapshot == nil || len(channelIDs) == 0 {
		return nil
	}
	windows, err := bundle.snapshot.AggregateChannelSamples(ctx, channelIDs)
	if err != nil {
		return nil
	}
	return windows
}
