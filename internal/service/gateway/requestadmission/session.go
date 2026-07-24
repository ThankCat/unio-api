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
	ErrReserveConflict  = errors.New("request admission token reserve conflicts with its first result")
	ErrUnknownAdmission = errors.New("request admission token is unknown")
)

// Store is the narrow BreakerStore contract owned by an ingress request session.
type Store interface {
	AcquireRequestAdmission(context.Context, breakerstore.RequestAdmissionInput) (breakerstore.RequestAdmissionResult, error)
	ReserveRequestTokens(context.Context, string, int64, int64, int64, string, int64) (breakerstore.ReserveResult, error)
	RenewRequestAdmission(context.Context, string, int64, int64, string, int64) (breakerstore.RequestAdmissionLifecycleOutcome, error)
	FinishRequestAdmission(context.Context, string, int64, int64, int64, string, int64) (breakerstore.RequestAdmissionLifecycleOutcome, error)
	SnapshotMany(context.Context, breakerstore.SnapshotManyInput) (breakerstore.SnapshotManyResult, error)
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

// UsageSession is the only request-admission capability exposed to gateway services.
// Finalization deliberately is not part of this interface.
type UsageSession interface {
	Reserve(context.Context, int64) error
	PublishAuthoritativeUsage(int64) bool
}

// AttemptTokenSession lets the future lifecycle permit manager bind the opaque request token
// without exposing a raw request-admission ID getter to protocol services.
type AttemptTokenSession interface {
	BindAttempt(*breakerstore.AcquireAttemptInput) error
}

// CandidateSnapshotSession owns the frozen admission revisions and injects fresh routing revisions
// without exposing the request-admission ID to protocol or lifecycle callers.
type CandidateSnapshotSession interface {
	SnapshotMany(context.Context, int64, []breakerstore.SnapshotCandidateInput) (breakerstore.SnapshotManyResult, error)
}

// AcquiredSession is retained only by the HTTP route wrapper.
type AcquiredSession interface {
	Usage() UsageSession
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
	TPMLimitOverride         *int64
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
		TPMLimitOverride:          cloneLimit(identity.TPMLimitOverride),
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
		m.logger.Warn("request admission rejected",
			zap.Int64("route_id", identity.RouteID),
			zap.Int64("user_id", identity.UserID),
			zap.String("outcome", string(result.Outcome)),
			zap.String("limited_dimension", result.LimitedDimension),
			zap.String("sync_target", result.SyncTarget),
		)
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
	if m.metrics != nil {
		m.metrics.AddRequestAdmissionActive(1)
	}
	s.startRenewer()
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

	reserveMu       sync.Mutex
	reserveObserved bool
	reserveEstimate int64
	reserveErr      error

	usageMu          sync.Mutex
	authoritativeTPM *int64
	finalized        bool

	finalizeOnce sync.Once
	finalizeErr  error
}

func (s *session) Usage() UsageSession { return s }

func (s *session) StopRenewer() { s.stopRenewer() }

// BindAttempt injects the opaque request token only after TPM Reserve succeeded with the same
// conservative estimate. It does not perform candidate admission itself.
func (s *session) BindAttempt(input *breakerstore.AcquireAttemptInput) error {
	if input == nil {
		return reserveConflictError()
	}
	s.reserveMu.Lock()
	defer s.reserveMu.Unlock()
	if !s.reserveObserved || s.reserveErr != nil || s.reserveEstimate != input.EstimatedInputTokens {
		return reserveConflictError()
	}
	s.usageMu.Lock()
	finalized := s.finalized
	s.usageMu.Unlock()
	if finalized {
		return failure.Wrap(
			failure.CodeGatewayRuntimeSyncRequired,
			ErrUnknownAdmission,
			failure.WithMessage("request admission session is already finalized"),
		)
	}
	if input.RequestAdmissionID != "" && input.RequestAdmissionID != s.requestID {
		return reserveConflictError()
	}
	input.RequestAdmissionID = s.requestID
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
		ChannelRateRevision:       admission.ChannelRateLimits,
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

// Reserve performs the request-level TPM reservation once. The first observed result is
// retained locally as well as by Redis so a service cannot reinterpret limited as allowed.
func (s *session) Reserve(ctx context.Context, estimatedTokens int64) error {
	s.reserveMu.Lock()
	defer s.reserveMu.Unlock()

	if estimatedTokens < 0 {
		return failure.Wrap(
			failure.CodeGatewayRuntimeSyncRequired,
			ErrReserveConflict,
			failure.WithMessage("request admission token estimate is invalid"),
		)
	}
	if s.reserveObserved {
		if s.reserveEstimate != estimatedTokens {
			return reserveConflictError()
		}
		return s.reserveErr
	}

	integrity, err := s.facts.Integrity(ctx)
	if err != nil {
		s.recordOperation("reserve", admissionErrorResult(err))
		s.reserveObserved = true
		s.reserveEstimate = estimatedTokens
		s.reserveErr = err
		return err
	}
	result, err := s.store.ReserveRequestTokens(
		ctx, s.requestID, s.routeID, s.userID, estimatedTokens, integrity.Epoch, integrity.Revision,
	)
	s.reserveObserved = true
	s.reserveEstimate = estimatedTokens
	if err != nil {
		s.recordOperation("reserve", admissionErrorResult(err))
		s.reserveErr = err
		return err
	}
	s.recordOperation("reserve", string(result))

	switch result {
	case breakerstore.ReserveReserved:
		return nil
	case breakerstore.ReserveLimited:
		s.reserveErr = failure.New(
			failure.CodeRateLimitExceeded,
			failure.WithMessage("request token rate limit exceeded"),
			failure.WithField("dimension", "tpm"),
		)
	case breakerstore.ReserveConflict:
		s.reserveErr = reserveConflictError()
	case breakerstore.ReserveUnknown:
		s.reserveErr = failure.Wrap(
			failure.CodeGatewayRuntimeSyncRequired,
			ErrUnknownAdmission,
			failure.WithMessage("request admission token is unavailable"),
		)
	case breakerstore.ReserveStoreUnavailable:
		s.reserveErr = failure.New(
			failure.CodeGatewayBreakerStoreUnavailable,
			failure.WithMessage("request admission store is unavailable"),
		)
	case breakerstore.ReserveRuntimeStateLost:
		s.reserveErr = failure.New(
			failure.CodeGatewayRuntimeStateLost,
			failure.WithMessage("request admission runtime state is unavailable"),
		)
	case breakerstore.ReserveStaleEpoch:
		s.reserveErr = failure.Wrap(
			failure.CodeGatewayRuntimeSyncRequired,
			breakerstore.ErrStaleIntegrityEpoch,
			failure.WithMessage("request admission token integrity epoch is stale"),
		)
	default:
		s.reserveErr = failure.Wrap(
			failure.CodeGatewayRuntimeSyncRequired,
			ErrUnknownAdmission,
			failure.WithMessage("request admission reserve returned an invalid outcome"),
		)
	}
	s.logger.Warn("request admission reserve rejected",
		zap.Int64("route_id", s.routeID),
		zap.Int64("user_id", s.userID),
		zap.Int64("estimated_tokens", estimatedTokens),
		zap.String("outcome", string(result)),
	)
	return s.reserveErr
}

// PublishAuthoritativeUsage records the first non-negative, non-partial cache-aware TPM
// value published after durable settlement. Later publications cannot replace it.
func (s *session) PublishAuthoritativeUsage(actualTPM int64) bool {
	if actualTPM < 0 {
		return false
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.finalized || s.authoritativeTPM != nil {
		return false
	}
	actual := actualTPM
	s.authoritativeTPM = &actual
	return true
}

// Finalize stops and joins the renewer before invoking the token's only terminal API.
// sync.Once makes duplicate route cleanup return the first terminal result without another Store call.
func (s *session) Finalize(ctx context.Context) error {
	s.finalizeOnce.Do(func() {
		s.stopRenewer()
		if s.metrics != nil {
			defer s.metrics.AddRequestAdmissionActive(-1)
		}

		authoritativeTPM := int64(-1)
		s.usageMu.Lock()
		s.finalized = true
		if s.authoritativeTPM != nil {
			authoritativeTPM = *s.authoritativeTPM
		}
		s.usageMu.Unlock()

		var outcome breakerstore.RequestAdmissionLifecycleOutcome
		var terminalErr error
		storeResultUnknown := false
		for attempt := 0; attempt < requestTerminalTries; attempt++ {
			integrity, err := s.facts.Integrity(ctx)
			if err == nil {
				outcome, err = s.store.FinishRequestAdmission(
					ctx,
					s.requestID,
					s.routeID,
					s.userID,
					authoritativeTPM,
					integrity.Epoch,
					integrity.Revision,
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
			s.logger.Warn("request admission finish rejected",
				zap.Int64("route_id", s.routeID),
				zap.Int64("user_id", s.userID),
				zap.String("outcome", string(outcome)),
			)
		}
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
					s.logger.Warn("request admission renew failed", fields...)
				}
			}
		}
	}()
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
	writeLimitFingerprint(h, "tpm", in.TPMLimitOverride)
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

func reserveConflictError() error {
	return failure.Wrap(
		failure.CodeGatewayBreakerPermitConflict,
		ErrReserveConflict,
		failure.WithMessage("request admission token reserve conflicts with its first result"),
	)
}

func requestLifecycleError(operation string, outcome breakerstore.RequestAdmissionLifecycleOutcome) error {
	code := failure.CodeGatewayRuntimeSyncRequired
	if outcome == breakerstore.RequestLifecycleRuntimeStateLost {
		code = failure.CodeGatewayRuntimeStateLost
	} else if outcome == breakerstore.RequestLifecycleConflict {
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

type usageSessionContextKey struct{}

type contextSessions struct {
	usage    UsageSession
	attempt  AttemptTokenSession
	snapshot CandidateSnapshotSession
}

// ContextWithUsageSession exposes only Reserve/Publish to downstream services.
func ContextWithUsageSession(ctx context.Context, usageSession UsageSession) context.Context {
	if usageSession == nil {
		return ctx
	}
	bundle := contextSessions{usage: usageSession}
	if attempt, ok := usageSession.(AttemptTokenSession); ok {
		bundle.attempt = attempt
	}
	if snapshot, ok := usageSession.(CandidateSnapshotSession); ok {
		bundle.snapshot = snapshot
	}
	return context.WithValue(ctx, usageSessionContextKey{}, bundle)
}

// UsageSessionFromContext returns the narrow request-admission service capability.
func UsageSessionFromContext(ctx context.Context) (UsageSession, bool) {
	bundle, ok := ctx.Value(usageSessionContextKey{}).(contextSessions)
	return bundle.usage, ok && bundle.usage != nil
}

// BindAttemptInput is reserved for the lifecycle permit manager. It supplies the opaque request
// token and validates the request-level Reserve invariant without exposing an ID getter.
func BindAttemptInput(ctx context.Context, input *breakerstore.AcquireAttemptInput) error {
	bundle, ok := ctx.Value(usageSessionContextKey{}).(contextSessions)
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
	bundle, ok := ctx.Value(usageSessionContextKey{}).(contextSessions)
	if !ok || bundle.snapshot == nil {
		return breakerstore.SnapshotManyResult{}, false, nil
	}
	result, err := bundle.snapshot.SnapshotMany(ctx, modelID, candidates)
	return result, true, err
}

// Reserve uses the request session when this request was admitted. A missing session is a
// runtime wiring error because generation origins must never fall back to the old Guard.
func Reserve(ctx context.Context, estimatedTokens int64) error {
	s, ok := UsageSessionFromContext(ctx)
	if !ok {
		return failure.Wrap(
			failure.CodeGatewayRuntimeSyncRequired,
			ErrUnknownAdmission,
			failure.WithMessage("request admission session is missing"),
		)
	}
	return s.Reserve(ctx, estimatedTokens)
}

// ReserveIfPresent keeps direct service unit tests and non-HTTP maintenance callers neutral;
// every production generation route installs the session before reaching the service.
func ReserveIfPresent(ctx context.Context, estimatedTokens int64) error {
	s, ok := UsageSessionFromContext(ctx)
	if !ok {
		return nil
	}
	return s.Reserve(ctx, estimatedTokens)
}

// PublishAuthoritativeUsage publishes settlement usage when a session exists. Recovery
// workers legitimately run without the original HTTP request session and are a no-op here.
func PublishAuthoritativeUsage(ctx context.Context, actualTPM int64) bool {
	s, ok := UsageSessionFromContext(ctx)
	if !ok {
		return false
	}
	return s.PublishAuthoritativeUsage(actualTPM)
}
