package lifecycle

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/runtimefacts"
)

var (
	errAttemptInvokePanic  = errors.New("attempt invoke panicked")
	errAttemptPermitFinish = errors.New("attempt permit finish failed")

	// ErrAttemptRuntimeFeedback 标识真实上游结果已完成 permit Finish，但 429/403 全局运行态反馈未能确认。
	// runner 必须据此终止 fallback，避免在 Redis 状态不确定时继续发送上游请求。
	ErrAttemptRuntimeFeedback = errors.New("attempt runtime feedback failed")
)

const (
	defaultAttemptPermitOperationTimeout = 2 * time.Second
	minimumAttemptPermitRenewInterval    = 10 * time.Millisecond
	attemptPermitTerminalTries           = 2
)

// AttemptPermitStore 是 lifecycle 使用的最小候选级 BreakerStore 契约。
type AttemptPermitStore interface {
	AcquireAttempt(context.Context, breakerstore.AcquireAttemptInput) (breakerstore.AttemptAdmission, error)
	Renew(context.Context, breakerstore.AttemptPermit) error
	Finish(context.Context, breakerstore.AttemptPermit, breakerstore.FinishOutcome) (breakerstore.FinishResult, error)
	Abort(context.Context, breakerstore.AttemptPermit) error
}

// AttemptRuntimeFeedbackStore 是真实上游 429/403 反馈所需的最小全局运行态契约。
// 与 AttemptPermitStore 分开，避免仅测试 permit 生命周期的替身被迫实现无关能力。
type AttemptRuntimeFeedbackStore interface {
	SetChannel429Cooldown(context.Context, int64, int64, int64) (int64, error)
	PauseChannelModelPermission(context.Context, int64, int64, int64, int64, int64) error
}

// AttemptPermitRuntimeFactsReader 每张新 permit 都强读 PostgreSQL 当前 control revisions，既有 permit
// 的每次生命周期写入则单独强读 ready integrity epoch。
type AttemptPermitRuntimeFactsReader interface {
	Integrity(context.Context) (runtimefacts.Integrity, error)
	Admission(context.Context) (runtimefacts.AdmissionRevisions, error)
	Routing(context.Context) (runtimefacts.RoutingRevisions, error)
}

type AttemptPermitManagerOptions struct {
	Logger           *zap.Logger
	Metrics          AttemptPermitMetricsRecorder
	OperationTimeout time.Duration
}

// AttemptPermitMetricsRecorder is the bounded candidate-permit observability contract.
type AttemptPermitMetricsRecorder interface {
	IncBreakerPermitOperation(operation, result string)
	AddBreakerPermitActive(delta float64)
	IncBreakerIgnoredResult(scope, reason string)
	IncChannelConfigRevisionMismatch(operation string)
	IncOriginStatusRevisionMismatch(operation string)
}

// AttemptPermitManager 负责 AcquireAttempt 及 permit owner 的建立；协议 runner 不直接持有 Redis token。
type AttemptPermitManager struct {
	store                 AttemptPermitStore
	runtimeFeedbackStore  AttemptRuntimeFeedbackStore
	runtimeFeedbackPolicy channel429CooldownPolicy
	facts                 AttemptPermitRuntimeFactsReader
	logger                *zap.Logger
	metrics               AttemptPermitMetricsRecorder
	operationTimeout      time.Duration
	newPermitID           func() string
}

func NewAttemptPermitManager(store AttemptPermitStore, facts AttemptPermitRuntimeFactsReader, opts AttemptPermitManagerOptions) *AttemptPermitManager {
	if store == nil {
		panic("lifecycle: attempt permit store is required")
	}
	if facts == nil {
		panic("lifecycle: attempt permit runtime facts reader is required")
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.OperationTimeout <= 0 {
		opts.OperationTimeout = defaultAttemptPermitOperationTimeout
	}
	feedbackStore, _ := store.(AttemptRuntimeFeedbackStore)
	return &AttemptPermitManager{
		store:                store,
		runtimeFeedbackStore: feedbackStore,
		facts:                facts,
		logger:               opts.Logger,
		metrics:              opts.Metrics,
		operationTimeout:     opts.OperationTimeout,
		newPermitID:          uuid.NewString,
	}
}

// SetChannel429CooldownPolicy 原子替换 429 缺省冷却与 Retry-After 封顶，仅影响之后完成的 transport。
// 该策略可由系统设置热更新；实际 cooldown 状态始终只保存在 Redis。
func (m *AttemptPermitManager) SetChannel429CooldownPolicy(defaultCooldown, cap time.Duration) {
	if m == nil {
		return
	}
	m.runtimeFeedbackPolicy.Set(defaultCooldown, cap)
}

type AttemptPermitAcquireParams struct {
	Candidate            routing.ChatRouteCandidate
	UpstreamEndpoint    requestlog.UpstreamEndpoint
	RequestMode          breakerstore.RequestMode
	EstimatedInputTokens int64
}

// Acquire 独立强读 admission+routing revision，并要求二者属于同一 ready integrity epoch。
func (m *AttemptPermitManager) Acquire(ctx context.Context, params AttemptPermitAcquireParams) (breakerstore.AttemptAdmission, *AttemptPermitOwner, error) {
	admissionFacts, err := m.facts.Admission(ctx)
	if err != nil {
		m.recordPermitOperation("acquire", permitErrorResult(err))
		return breakerstore.AttemptAdmission{}, nil, err
	}
	routingFacts, err := m.facts.Routing(ctx)
	if err != nil {
		m.recordPermitOperation("acquire", permitErrorResult(err))
		return breakerstore.AttemptAdmission{}, nil, err
	}
	if admissionFacts.Integrity != routingFacts.Integrity {
		err := failure.New(
			failure.CodeGatewayRuntimeSyncRequired,
			failure.WithMessage("attempt runtime revisions do not share one integrity epoch"),
		)
		m.recordPermitOperation("acquire", permitErrorResult(err))
		return breakerstore.AttemptAdmission{}, nil, err
	}

	in := breakerstore.AcquireAttemptInput{
		PermitID:                  m.newPermitID(),
		IntegrityEpoch:            admissionFacts.Epoch,
		IntegrityRevision:         admissionFacts.Revision,
		OriginID:                params.Candidate.ProviderOriginID,
		ChannelID:                 params.Candidate.Channel.ID,
		OriginBaseURLRevision:   params.Candidate.ProviderOriginBaseURLRevision,
		OriginStatusRevision:    params.Candidate.ProviderOriginStatusRevision,
		ChannelConfigRevision:     params.Candidate.ChannelConfigRevision,
		ModelID:                   params.Candidate.ModelDBID,
		UpstreamEndpoint:         breakerEndpoint(params.UpstreamEndpoint),
		RequestMode:               params.RequestMode,
		ChannelRateRevision:       admissionFacts.ChannelRateLimits,
		GlobalConcurrencyRevision: admissionFacts.Concurrency,
		CircuitBreakerRevision:    routingFacts.CircuitBreaker,
		ChannelAdmissionRevision:  params.Candidate.ChannelAdmissionLimitsRevision,
		EnforceOriginControl:    true,
		EstimatedInputTokens:      params.EstimatedInputTokens,
	}
	if err := requestadmission.BindAttemptInput(ctx, &in); err != nil {
		m.recordPermitOperation("acquire", permitErrorResult(err))
		return breakerstore.AttemptAdmission{}, nil, err
	}
	in.AdmissionFingerprint = attemptAdmissionFingerprint(in)

	result, err := m.store.AcquireAttempt(ctx, in)
	if err != nil {
		err = normalizeAttemptStoreError(err)
		m.recordPermitOperation("acquire", permitErrorResult(err))
		return breakerstore.AttemptAdmission{}, nil, err
	}
	if result.Mode != breakerstore.AdmissionPermit {
		m.recordPermitOperation("acquire", string(result.Reason))
		m.recordAcquireRevisionMismatch(result.Reason)
		m.logger.Info("attempt permit denied",
			zap.Int64("origin_id", params.Candidate.ProviderOriginID),
			zap.Int64("channel_id", params.Candidate.Channel.ID),
			zap.String("reason", string(result.Reason)),
		)
		return result, nil, nil
	}
	if result.Permit == nil {
		err := failure.New(
			failure.CodeGatewayRuntimeSyncRequired,
			failure.WithMessage("attempt admission returned a missing permit"),
		)
		m.recordPermitOperation("acquire", permitErrorResult(err))
		return breakerstore.AttemptAdmission{}, nil, err
	}
	owner := newAttemptPermitOwnerWithFeedback(
		m.store,
		m.runtimeFeedbackStore,
		&m.runtimeFeedbackPolicy,
		m.facts,
		*result.Permit,
		m.logger,
		m.operationTimeout,
	)
	owner.metrics = m.metrics
	m.recordPermitOperation("acquire", "permit")
	if m.metrics != nil {
		m.metrics.AddBreakerPermitActive(1)
	}
	return result, owner, nil
}

func (m *AttemptPermitManager) recordPermitOperation(operation, result string) {
	if m != nil && m.metrics != nil {
		m.metrics.IncBreakerPermitOperation(operation, result)
	}
}

func (m *AttemptPermitManager) recordAcquireRevisionMismatch(reason breakerstore.DeniedReason) {
	if m == nil || m.metrics == nil {
		return
	}
	switch reason {
	case breakerstore.ReasonStaleConfigRevision:
		m.metrics.IncChannelConfigRevisionMismatch("acquire")
	case breakerstore.ReasonStaleStatusRevision:
		m.metrics.IncOriginStatusRevisionMismatch("acquire")
	}
}

func breakerEndpoint(operation requestlog.UpstreamEndpoint) breakerstore.UpstreamEndpoint {
	switch operation {
	case requestlog.UpstreamEndpointChatCompletions:
		return breakerstore.EndpointChatCompletions
	case requestlog.UpstreamEndpointResponses:
		return breakerstore.EndpointResponses
	case requestlog.UpstreamEndpointResponsesCompact:
		return breakerstore.EndpointResponsesCompact
	case requestlog.UpstreamEndpointMessages:
		return breakerstore.EndpointMessages
	default:
		return ""
	}
}

func attemptAdmissionFingerprint(in breakerstore.AcquireAttemptInput) string {
	payload := fmt.Sprintf(
		"%s|%s|%s|%d|%d|%d|%d|%d|%d|%s|%s|%d|%d|%d|%d|%d|%d",
		in.PermitID, in.RequestAdmissionID, in.IntegrityEpoch, in.IntegrityRevision,
		in.OriginID, in.ChannelID, in.OriginBaseURLRevision, in.OriginStatusRevision,
		in.ChannelConfigRevision, in.UpstreamEndpoint, in.RequestMode, in.ModelID,
		in.ChannelRateRevision, in.GlobalConcurrencyRevision, in.CircuitBreakerRevision,
		in.ChannelAdmissionRevision, in.EstimatedInputTokens,
	)
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", sum[:])
}

func normalizeAttemptStoreError(err error) error {
	if err == nil || failure.CodeOf(err) == failure.CodeGatewayBreakerStoreUnavailable {
		return err
	}
	if errors.Is(err, breakerstore.ErrStoreUnavailable) {
		return failure.Wrap(
			failure.CodeGatewayBreakerStoreUnavailable,
			err,
			failure.WithMessage("attempt admission store unavailable"),
		)
	}
	return err
}

// AttemptPermitOwner 唯一拥有 permit renewer 与 terminal API；停止 renewer 后才 Finish/Abort。
type AttemptPermitOwner struct {
	store                 AttemptPermitStore
	runtimeFeedbackStore  AttemptRuntimeFeedbackStore
	runtimeFeedbackPolicy *channel429CooldownPolicy
	facts                 AttemptPermitRuntimeFactsReader
	permit                breakerstore.AttemptPermit
	logger                *zap.Logger
	metrics               AttemptPermitMetricsRecorder
	operationTimeout      time.Duration

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}

	terminalOnce   sync.Once
	terminalResult breakerstore.FinishResult
	terminalErr    error
}

func newAttemptPermitOwner(
	store AttemptPermitStore,
	facts AttemptPermitRuntimeFactsReader,
	permit breakerstore.AttemptPermit,
	logger *zap.Logger,
	timeout time.Duration,
) *AttemptPermitOwner {
	return newAttemptPermitOwnerWithFeedback(store, nil, nil, facts, permit, logger, timeout)
}

func newAttemptPermitOwnerWithFeedback(
	store AttemptPermitStore,
	runtimeFeedbackStore AttemptRuntimeFeedbackStore,
	runtimeFeedbackPolicy *channel429CooldownPolicy,
	facts AttemptPermitRuntimeFactsReader,
	permit breakerstore.AttemptPermit,
	logger *zap.Logger,
	timeout time.Duration,
) *AttemptPermitOwner {
	o := &AttemptPermitOwner{
		store:                 store,
		runtimeFeedbackStore:  runtimeFeedbackStore,
		runtimeFeedbackPolicy: runtimeFeedbackPolicy,
		facts:                 facts,
		permit:                permit,
		logger:                logger,
		operationTimeout:      timeout,
		stop:                  make(chan struct{}),
		done:                  make(chan struct{}),
	}
	go o.renewLoop()
	return o
}

func (o *AttemptPermitOwner) Finish(ctx context.Context, outcome breakerstore.FinishOutcome) (breakerstore.FinishResult, error) {
	return o.finish(ctx, outcome, nil)
}

// FinishTransport 终结一次已开始的真实 transport，并在 permit Finish 可确认后反馈 429/403 运行态。
// Finish 与反馈共用 terminalOnce，因此重复调用不会重复释放资源或重复反馈。
func (o *AttemptPermitOwner) FinishTransport(
	ctx context.Context,
	outcome breakerstore.FinishOutcome,
	upstreamErr error,
) (breakerstore.FinishResult, error) {
	return o.finish(ctx, outcome, upstreamErr)
}

func (o *AttemptPermitOwner) finish(
	ctx context.Context,
	outcome breakerstore.FinishOutcome,
	upstreamErr error,
) (breakerstore.FinishResult, error) {
	o.terminalOnce.Do(func() {
		o.stopRenewer()
		if o.metrics != nil {
			defer o.metrics.AddBreakerPermitActive(-1)
		}
		for i := 0; i < attemptPermitTerminalTries; i++ {
			opCtx, cancel := o.operationContext(ctx)
			o.terminalErr = o.requireCurrentIntegrity(opCtx)
			if o.terminalErr == nil {
				o.terminalResult, o.terminalErr = o.store.Finish(opCtx, o.permit, outcome)
			}
			cancel()
			if o.terminalErr == nil {
				break
			}
			if !errors.Is(o.terminalErr, breakerstore.ErrStoreUnavailable) {
				break
			}
		}
		if o.terminalErr != nil {
			o.terminalErr = normalizeAttemptStoreError(o.terminalErr)
			o.recordPermitOperation("finish", permitErrorResult(o.terminalErr))
			return
		}
		o.recordFinishResult()
		o.terminalErr = o.recordRuntimeFeedback(ctx, upstreamErr)
	})
	return o.terminalResult, o.terminalErr
}

type channel429CooldownPolicy struct {
	mu              sync.RWMutex
	defaultCooldown time.Duration
	cap             time.Duration
}

func (p *channel429CooldownPolicy) Set(defaultCooldown, cap time.Duration) {
	if defaultCooldown < 0 {
		defaultCooldown = 0
	}
	if cap < 0 {
		cap = 0
	}
	p.mu.Lock()
	p.defaultCooldown = defaultCooldown
	p.cap = cap
	p.mu.Unlock()
}

func (p *channel429CooldownPolicy) Resolve(retryAfter time.Duration) time.Duration {
	if retryAfter < 0 {
		retryAfter = 0
	}
	p.mu.RLock()
	defaultCooldown := p.defaultCooldown
	cap := p.cap
	p.mu.RUnlock()

	cooldown := retryAfter
	if cooldown <= 0 {
		cooldown = defaultCooldown
	}
	if cap > 0 && cooldown > cap {
		cooldown = cap
	}
	return cooldown
}

func (o *AttemptPermitOwner) recordRuntimeFeedback(ctx context.Context, upstreamErr error) error {
	if o.runtimeFeedbackStore == nil || upstreamErr == nil {
		return nil
	}
	category, categoryOK := adapter.UpstreamCategoryOf(upstreamErr)
	metadata, metadataOK := adapter.UpstreamMetadataOf(upstreamErr)
	if !categoryOK || !metadataOK {
		return nil
	}

	var feedbackErr error
	switch {
	case category == adapter.UpstreamErrorRateLimit && metadata.StatusCode == 429:
		cooldown := metadata.RetryAfter
		if o.runtimeFeedbackPolicy != nil {
			cooldown = o.runtimeFeedbackPolicy.Resolve(metadata.RetryAfter)
		}
		durationMs := cooldown.Milliseconds()
		if durationMs <= 0 {
			return nil
		}
		sourceRetryAfterMs := metadata.RetryAfter.Milliseconds()
		if sourceRetryAfterMs < 0 {
			sourceRetryAfterMs = 0
		}
		opCtx, cancel := o.operationContext(ctx)
		_, feedbackErr = o.runtimeFeedbackStore.SetChannel429Cooldown(
			opCtx,
			o.permit.ChannelID,
			durationMs,
			sourceRetryAfterMs,
		)
		cancel()
	case category == adapter.UpstreamErrorPermission && metadata.StatusCode == 403:
		opCtx, cancel := o.operationContext(ctx)
		feedbackErr = o.runtimeFeedbackStore.PauseChannelModelPermission(
			opCtx,
			o.permit.ChannelID,
			o.permit.ModelID,
			o.permit.ChannelConfigRevision,
			o.permit.OriginBaseURLRevision,
			o.permit.OriginStatusRevision,
		)
		cancel()
	default:
		return nil
	}
	if feedbackErr == nil {
		return nil
	}
	return failure.Wrap(
		failure.CodeGatewayBreakerStoreUnavailable,
		errors.Join(ErrAttemptRuntimeFeedback, normalizeAttemptStoreError(feedbackErr), upstreamErr),
		failure.WithMessage("attempt runtime feedback store unavailable"),
	)
}

func (o *AttemptPermitOwner) Abort(ctx context.Context) error {
	o.terminalOnce.Do(func() {
		o.stopRenewer()
		if o.metrics != nil {
			defer o.metrics.AddBreakerPermitActive(-1)
		}
		for i := 0; i < attemptPermitTerminalTries; i++ {
			opCtx, cancel := o.operationContext(ctx)
			o.terminalErr = o.requireCurrentIntegrity(opCtx)
			if o.terminalErr == nil {
				o.terminalErr = o.store.Abort(opCtx, o.permit)
			}
			cancel()
			if o.terminalErr == nil {
				return
			}
			if !errors.Is(o.terminalErr, breakerstore.ErrStoreUnavailable) {
				break
			}
		}
		o.terminalErr = normalizeAttemptStoreError(o.terminalErr)
		result := "aborted"
		if o.terminalErr != nil {
			result = permitErrorResult(o.terminalErr)
		}
		o.recordPermitOperation("abort", result)
	})
	return o.terminalErr
}

// abortAttemptPermitOnExit is deferred immediately after a permit owner reaches a runner.
// It closes panic and early-return gaps before the normal transport terminal path is installed.
// AttemptPermitOwner is first-terminal-wins, so an already finished or aborted permit is unchanged.
func abortAttemptPermitOnExit(ctx context.Context, owner *AttemptPermitOwner) {
	if owner != nil {
		_ = owner.Abort(ctx)
	}
}

func (o *AttemptPermitOwner) renewLoop() {
	defer close(o.done)
	interval := time.Duration(o.permit.RenewMs) * time.Millisecond
	if interval <= 0 {
		interval = time.Duration(o.permit.PermitTTLMs) * time.Millisecond / 3
	}
	if interval < minimumAttemptPermitRenewInterval {
		interval = minimumAttemptPermitRenewInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-o.stop:
			return
		case <-ticker.C:
			opCtx, cancel := context.WithTimeout(context.Background(), o.operationTimeout)
			err := o.renew(opCtx)
			cancel()
			result := "renewed"
			if err != nil {
				result = permitErrorResult(err)
			}
			o.recordPermitOperation("renew", result)
			if err != nil {
				o.logger.Warn("attempt permit renew failed", zap.Error(normalizeAttemptStoreError(err)))
			}
		}
	}
}

func (o *AttemptPermitOwner) recordPermitOperation(operation, result string) {
	if o != nil && o.metrics != nil {
		o.metrics.IncBreakerPermitOperation(operation, result)
	}
}

func (o *AttemptPermitOwner) recordFinishResult() {
	if o == nil || o.metrics == nil {
		return
	}
	result := "mixed"
	if o.terminalResult.OriginDisposition == o.terminalResult.ChannelDisposition {
		result = string(o.terminalResult.OriginDisposition)
	} else if o.terminalResult.OriginDisposition == breakerstore.DispositionApplied ||
		o.terminalResult.ChannelDisposition == breakerstore.DispositionApplied {
		result = "applied"
	}
	o.metrics.IncBreakerPermitOperation("finish", result)
	o.recordFinishDisposition("origin", o.terminalResult.OriginDisposition)
	o.recordFinishDisposition("channel", o.terminalResult.ChannelDisposition)
}

func (o *AttemptPermitOwner) recordFinishDisposition(scope string, disposition breakerstore.Disposition) {
	if disposition == breakerstore.DispositionApplied || disposition == breakerstore.DispositionNotApplicable || disposition == "" {
		return
	}
	o.metrics.IncBreakerIgnoredResult(scope, string(disposition))
	switch disposition {
	case breakerstore.DispositionStaleConfigRev:
		o.metrics.IncChannelConfigRevisionMismatch("finish")
	case breakerstore.DispositionStaleStatusRev:
		o.metrics.IncOriginStatusRevisionMismatch("finish")
	}
	o.logger.Warn("attempt permit result ignored",
		zap.Int64("origin_id", o.permit.OriginID),
		zap.Int64("channel_id", o.permit.ChannelID),
		zap.String("scope", scope),
		zap.String("disposition", string(disposition)),
	)
}

func permitErrorResult(err error) string {
	switch failure.CodeOf(err) {
	case failure.CodeGatewayRuntimeStateLost:
		return "runtime_state_lost"
	case failure.CodeGatewayRuntimeSyncRequired:
		return "runtime_sync_required"
	case failure.CodeGatewayBreakerPermitConflict:
		return "conflict"
	case failure.CodeGatewayBreakerStoreUnavailable, failure.CodeDependencyRedisUnavailable:
		return "store_unavailable"
	case failure.CodeDependencyPostgresUnavailable:
		return "postgres_unavailable"
	default:
		return "error"
	}
}

func (o *AttemptPermitOwner) renew(ctx context.Context) error {
	if err := o.requireCurrentIntegrity(ctx); err != nil {
		return err
	}
	return o.store.Renew(ctx, o.permit)
}

func (o *AttemptPermitOwner) requireCurrentIntegrity(ctx context.Context) error {
	integrity, err := o.facts.Integrity(ctx)
	if err != nil {
		return err
	}
	if integrity.Epoch == o.permit.IntegrityEpoch && integrity.Revision == o.permit.IntegrityRevision {
		return nil
	}
	return failure.Wrap(
		failure.CodeGatewayRuntimeSyncRequired,
		breakerstore.ErrStaleIntegrityEpoch,
		failure.WithMessage("attempt permit integrity epoch is stale"),
		failure.WithField("permit_integrity_epoch", o.permit.IntegrityEpoch),
		failure.WithField("permit_integrity_revision", o.permit.IntegrityRevision),
		failure.WithField("runtime_integrity_epoch", integrity.Epoch),
		failure.WithField("runtime_integrity_revision", integrity.Revision),
	)
}

func (o *AttemptPermitOwner) stopRenewer() {
	o.stopOnce.Do(func() { close(o.stop) })
	<-o.done
}

func (o *AttemptPermitOwner) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), o.operationTimeout)
}

// nonStreamFinishOutcome 使用稳定 adapter 分类生成保守 breaker attribution。
func nonStreamFinishOutcome(success AttemptSuccess, timing AttemptTimingFacts, err error) breakerstore.FinishOutcome {
	out := breakerstore.FinishOutcome{
		OriginOutcome: breakerstore.OutcomeIgnored,
		ChannelOutcome:  breakerstore.OutcomeIgnored,
	}
	if err == nil {
		out.OriginOutcome = breakerstore.OutcomeEligibleSuccess
		out.ChannelOutcome = breakerstore.OutcomeEligibleSuccess
		actual := billableTPMTokens(success.Facts.Usage)
		out.ChannelTPMActual = &actual
		return out
	}
	if nonStreamChannelFailureEligible(err) {
		out.ChannelOutcome = breakerstore.OutcomeEligibleFailure
	}
	applyOriginFailureAttribution(&out, timing, false, err)
	return out
}

// applyOriginFailureAttribution 把无需聚合的 Origin 故障直接归因，并把三类歧义故障交给
// BreakerStore.Finish 在 Redis 内按 distinct Channel + model 证据门槛原子判定。
func applyOriginFailureAttribution(
	out *breakerstore.FinishOutcome,
	timing AttemptTimingFacts,
	stream bool,
	err error,
) {
	if out == nil || err == nil {
		return
	}
	category, categoryOK := adapter.UpstreamCategoryOf(err)
	if errors.Is(err, context.Canceled) || (categoryOK && category == adapter.UpstreamErrorCanceled) {
		return
	}
	metadata, metadataOK := adapter.UpstreamMetadataOf(err)
	statusCode := 0
	if metadataOK {
		statusCode = metadata.StatusCode
	}
	code := failure.CodeOf(err)

	if code == failure.CodeAdapterReadStreamFailed || code == failure.CodeAdapterStreamIdleTimeout {
		if category == adapter.UpstreamErrorTimeout || (!categoryOK && timeoutError(err)) {
			if stream && timing.FirstTokenMs() == nil {
				out.OriginEvidence = breakerstore.OriginEvidenceFirstTokenTimeout
			} else {
				out.OriginEvidence = breakerstore.OriginEvidenceBodyReadTimeout
			}
			return
		}
		if category == adapter.UpstreamErrorServer || (!categoryOK && code == failure.CodeAdapterReadStreamFailed) {
			// EOF、连接重置和代理截断属于 Origin 连接故障，不需要跨样本聚合。
			out.OriginOutcome = breakerstore.OutcomeEligibleFailure
		}
		return
	}

	if category == adapter.UpstreamErrorServer {
		switch statusCode {
		case 500:
			out.OriginEvidence = breakerstore.OriginEvidenceHTTP500
		case 502, 503, 504:
			out.OriginOutcome = breakerstore.OutcomeEligibleFailure
		case 0:
			out.OriginOutcome = breakerstore.OutcomeEligibleFailure
		}
		return
	}
	if category == adapter.UpstreamErrorTimeout && statusCode == 0 {
		// 发送/握手/响应头阶段超时尚未进入 body 读取，直接归因到 Origin。
		out.OriginOutcome = breakerstore.OutcomeEligibleFailure
	}
}

func timeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

// invokeNonStreamAttempt 把 transport timing 与 permit terminal 绑定为一个不可遗漏的调用边界。
func (r *AttemptRunner) invokeNonStreamAttempt(
	ctx context.Context,
	candidate routing.ChatRouteCandidate,
	attempt requestlog.AttemptRecord,
	owner *AttemptPermitOwner,
	invoke NonStreamInvoke,
) (success AttemptSuccess, err error) {
	observer := NewAttemptTimingObserver(false)
	attemptCtx := adapter.WithAttemptTimingObserver(ctx, observer)
	var panicValue any
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				panicValue = recovered
			}
		}()
		success, err = invoke(attemptCtx, candidate)
	}()
	adapter.MarkTransportCompleted(attemptCtx)
	facts := observer.Snapshot()
	r.lifecycle.RecordAttemptTiming(ctx, attempt, facts)
	outcomeErr := err
	if panicValue != nil {
		outcomeErr = errAttemptInvokePanic
	}
	finishOutcome := nonStreamFinishOutcome(success, facts, outcomeErr)

	if owner != nil {
		if facts.UpstreamStartedAt == nil {
			if abortErr := owner.Abort(ctx); abortErr != nil {
				r.logRouting(ctx, "attempt permit abort result unknown",
					zap.Int64("channel_id", candidate.Channel.ID),
					zap.Error(abortErr),
				)
			}
		} else {
			finishResult, finishErr := owner.FinishTransport(
				ctx,
				finishOutcome,
				outcomeErr,
			)
			if finishErr != nil {
				if errors.Is(finishErr, ErrAttemptRuntimeFeedback) {
					r.lifecycle.RecordAttemptBreakerDisposition(
						ctx,
						attempt,
						string(finishResult.OriginDisposition),
						string(finishResult.ChannelDisposition),
					)
					r.logRouting(ctx, "attempt runtime feedback failed",
						zap.Int64("channel_id", candidate.Channel.ID),
						zap.Error(finishErr),
					)
					err = finishErr
				} else {
					r.lifecycle.RecordAttemptBreakerDisposition(
						ctx,
						attempt,
						string(breakerstore.DispositionResultUnknown),
						string(breakerstore.DispositionResultUnknown),
					)
					r.logRouting(ctx, "attempt permit finish result unknown",
						zap.Int64("channel_id", candidate.Channel.ID),
						zap.Error(finishErr),
					)
					if err != nil {
						err = errors.Join(errAttemptPermitFinish, finishErr, err)
					}
				}
			} else {
				r.lifecycle.RecordAttemptBreakerDisposition(
					ctx,
					attempt,
					string(finishResult.OriginDisposition),
					string(finishResult.ChannelDisposition),
				)
			}
		}
	}
	r.lifecycle.RecordAttemptRuntimeMetrics(candidate, attempt.UpstreamEndpoint, false, facts, finishOutcome, outcomeErr)
	if panicValue != nil {
		panic(panicValue)
	}
	return success, err
}

func nonStreamChannelFailureEligible(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if meta, ok := adapter.UpstreamMetadataOf(err); ok && meta.StatusCode >= 400 && meta.StatusCode < 500 {
		return false
	}
	if category, ok := adapter.UpstreamCategoryOf(err); ok {
		switch category {
		case adapter.UpstreamErrorServer, adapter.UpstreamErrorTimeout:
			return true
		case adapter.UpstreamErrorUnknown:
			// A 2xx response that cannot satisfy the protocol contract is attributable to the channel.
			return protocolFailureCode(failure.CodeOf(err))
		default:
			return false
		}
	}
	return protocolFailureCode(failure.CodeOf(err))
}

func protocolFailureCode(code failure.Code) bool {
	switch code {
	case failure.CodeAdapterDecodeResponseFailed,
		failure.CodeAdapterInvalidResponse,
		failure.CodeAdapterReadStreamFailed,
		failure.CodeAdapterResponseTooLarge:
		return true
	default:
		return false
	}
}

type attemptDenialSummary struct {
	seen            bool
	capacityOnly    bool
	rateLimited     bool
	concurrencyOnly bool
}

func (s *attemptDenialSummary) Record(reason breakerstore.DeniedReason) {
	s.seen = true
	switch reason {
	case breakerstore.ReasonRateLimited:
		s.rateLimited = true
	case breakerstore.ReasonConcurrencyLimited:
		s.concurrencyOnly = true
	default:
		s.capacityOnly = false
	}
}

func (s attemptDenialSummary) FinalError() error {
	if s.capacityOnly {
		if s.rateLimited {
			return failure.New(
				failure.CodeGatewayChannelRateLimited,
				failure.WithMessage("all candidate channels are rate limited"),
			)
		}
		if s.concurrencyOnly {
			return failure.New(
				failure.CodeGatewayChannelConcurrencyLimited,
				failure.WithMessage("all candidate channels are at concurrency limit"),
			)
		}
	}
	return failure.Wrap(
		failure.CodeRoutingNoAvailableChannel,
		routing.ErrNoAvailableChannel,
		failure.WithMessage(routing.ErrNoAvailableChannel.Error()),
	)
}

func attemptDeniedSkipReason(reason breakerstore.DeniedReason) string {
	switch reason {
	case breakerstore.ReasonOpen, breakerstore.ReasonHalfOpenBusy:
		return "breaker"
	case breakerstore.ReasonRateLimited:
		return "channel_rate_limit"
	case breakerstore.ReasonConcurrencyLimited:
		return "channel_concurrency"
	case breakerstore.ReasonModelPermissionPaused:
		return "model_permission"
	default:
		return string(reason)
	}
}
