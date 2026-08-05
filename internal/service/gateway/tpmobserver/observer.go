// Package tpmobserver owns the minute-accurate TPM observation model (§8).
//
// 它只描述 Gateway 已经观察到的输入与输出 token，永远不参与请求准入、候选评分或计费。
// 热路径只往有界内存队列里放一条记录；聚合、分词换算和 Redis 写入全部在后台完成。
package tpmobserver

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
)

const (
	defaultQueueCapacity      = 8192
	defaultCorrectionCapacity = 1024
	defaultFlushInterval      = time.Second
	defaultFlushTimeout       = 3 * time.Second
	// maxBatchEntries 限制单次 Redis 批次的 key 数量，避免一个脚本长时间占住 Redis。
	maxBatchEntries = 512
	// maxTrackedScopes 限制同时跟踪权重的主体数量，防止 Finalize 丢失时无界增长。
	maxTrackedScopes = 20000
	// shutdownFlushTimeout 是进程退出前最后一次尽力 flush 的上限。
	shutdownFlushTimeout = 2 * time.Second
)

// Store 是观测器需要的最小 breakerstore 能力。
type Store interface {
	RecordTPMObservations(ctx context.Context, operationID string, entries []breakerstore.TPMObservationEntry) (bool, error)
	CorrectTPMObservations(ctx context.Context, scope string, entries []breakerstore.TPMObservationEntry) (breakerstore.TPMCorrectionResult, error)
}

// MetricsRecorder 是观测器的有界可观测契约。标签里绝不允许出现 request/attempt ID。
type MetricsRecorder interface {
	IncTPMObservationFlushFailure()
	IncTPMObservationDropped(reason string)
	SetTPMObservationQueueDepth(depth float64)
	AddTPMObservationProvisionalTokens(tokens float64)
	IncTPMObservationMissingUsage()
	IncTPMObservationFinalCorrection(result string)
	AddTPMObservationExpiredCorrection(buckets float64)
}

// Scope 标识一个被跟踪的观测主体。
// Route 维度按客户请求跟踪，Channel 维度按单个 attempt 跟踪；Key 同时作为跨进程幂等键的一部分。
type Scope struct {
	Kind breakerstore.TPMObservationKind
	ID   int64
	Key  string
}

func (s Scope) valid() bool {
	if s.ID <= 0 || s.Key == "" {
		return false
	}
	return s.Kind == breakerstore.TPMScopeRoute || s.Kind == breakerstore.TPMScopeChannel
}

func (s Scope) correctionScope() string {
	return string(s.Kind) + ":" + s.Key
}

func (s Scope) trackingKey() string {
	return string(s.Kind) + ":" + strconv.FormatInt(s.ID, 10) + ":" + s.Key
}

type observation struct {
	scope breakerstore.TPMObservationScope
	delta breakerstore.TPMObservationDelta
}

type correction struct {
	scope   string
	entries []breakerstore.TPMObservationEntry
	expired int64
}

type trackedScope struct {
	weights     *scopeWeights
	lastTouched int64 // 分钟号，用于回收从未 Finalize 的主体
}

// Options 配置观测器。零值使用内置默认。
type Options struct {
	Logger        *zap.Logger
	Metrics       MetricsRecorder
	FlushInterval time.Duration
	FlushTimeout  time.Duration
	QueueCapacity int
	Now           func() time.Time
}

// Observer 是进程内的 TPM 观测聚合器。所有方法对 nil 接收者安全。
type Observer struct {
	store         Store
	logger        *zap.Logger
	metrics       MetricsRecorder
	flushInterval time.Duration
	flushTimeout  time.Duration
	now           func() time.Time

	queue       chan observation
	corrections chan correction
	done        chan struct{}

	mu      sync.Mutex
	tracked map[string]*trackedScope

	instanceID string
	sequence   uint64

	// pending 是上一次 flush 结果未知的批次。它必须用同一个 operation id 重试，
	// 否则「写成功但响应丢失」的批次会在重试时被重复累加。
	pending *pendingBatch
}

type pendingBatch struct {
	operationID string
	entries     []breakerstore.TPMObservationEntry
	attempts    int
}

// New 创建观测器。store 为 nil 时返回 nil，调用方全部退化为 no-op。
func New(store Store, opts Options) *Observer {
	if store == nil {
		return nil
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = defaultFlushInterval
	}
	if opts.FlushTimeout <= 0 {
		opts.FlushTimeout = defaultFlushTimeout
	}
	if opts.QueueCapacity <= 0 {
		opts.QueueCapacity = defaultQueueCapacity
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Observer{
		store:         store,
		logger:        opts.Logger,
		metrics:       opts.Metrics,
		flushInterval: opts.FlushInterval,
		flushTimeout:  opts.FlushTimeout,
		now:           opts.Now,
		queue:         make(chan observation, opts.QueueCapacity),
		corrections:   make(chan correction, defaultCorrectionCapacity),
		done:          make(chan struct{}),
		tracked:       make(map[string]*trackedScope),
		instanceID:    uuid.NewString(),
	}
}

// Input 记录一次输入观测，并开始跟踪该主体的分钟权重。
// 同一主体重复调用只有第一次生效：fallback 时 Route 不重复记输入。
func (o *Observer) Input(scope Scope, at time.Time, tokens int64) {
	if o == nil || !scope.valid() || tokens < 0 {
		return
	}
	minute := breakerstore.TPMObservationMinute(at)
	first := o.withWeights(scope, minute, func(w *scopeWeights) bool {
		if w.inputRecorded {
			return false
		}
		w.addInput(minute, tokens)
		return true
	})
	if !first {
		return
	}
	o.enqueue(observation{
		scope: breakerstore.TPMObservationScope{Kind: scope.Kind, ID: scope.ID, Minute: minute},
		delta: breakerstore.TPMObservationDelta{
			InputTokens:       tokens,
			ProvisionalTokens: tokens,
			ObservedAttempts:  1,
		},
	})
	o.addProvisional(tokens)
}

// Output 记录一个已观察到的输出增量。tokens 必须来自与 partial settlement 同一次分词，
// 避免 TPM 观测与计费出现两套输出口径。
func (o *Observer) Output(scope Scope, at time.Time, tokens int64) {
	if o == nil || !scope.valid() || tokens <= 0 {
		return
	}
	minute := breakerstore.TPMObservationMinute(at)
	o.withWeights(scope, minute, func(w *scopeWeights) bool {
		w.addOutput(minute, tokens)
		return true
	})
	o.enqueue(observation{
		scope: breakerstore.TPMObservationScope{Kind: scope.Kind, ID: scope.ID, Minute: minute},
		delta: breakerstore.TPMObservationDelta{
			OutputTokens:      tokens,
			ProvisionalTokens: tokens,
		},
	})
	o.addProvisional(tokens)
}

// Finalize 用本次结算认可的 usage 收口该主体的观测，并释放它的权重表。
//
// reliable=false 表示上游已到达但没有可靠 usage：保留已观察到的估算与 provisional，
// 只在响应完成分钟增加 missing_usage_count，绝不用不可靠 usage 去修正桶。
func (o *Observer) Finalize(scope Scope, at time.Time, facts usage.Facts, reliable bool) {
	if o == nil || !scope.valid() {
		return
	}
	minute := breakerstore.TPMObservationMinute(at)
	tracked := o.takeWeights(scope)

	if !reliable {
		o.enqueue(observation{
			scope: breakerstore.TPMObservationScope{Kind: scope.Kind, ID: scope.ID, Minute: minute},
			delta: breakerstore.TPMObservationDelta{MissingUsageCount: 1},
		})
		o.incMissingUsage()
		return
	}

	actualInput, inputOK := facts.ObservedInputTokens()
	actualOutput, outputOK := facts.ObservedOutputTokens()
	if !inputOK || !outputOK {
		o.enqueue(observation{
			scope: breakerstore.TPMObservationScope{Kind: scope.Kind, ID: scope.ID, Minute: minute},
			delta: breakerstore.TPMObservationDelta{MissingUsageCount: 1},
		})
		o.incMissingUsage()
		return
	}
	if tracked == nil {
		// 没有本进程写出的 provisional 观测（例如 recovery worker 重放）：无从按权重修正，
		// 也不应该凭空补一个分钟桶。Redis marker 仍然保护着原进程的那次修正。
		return
	}

	tracked.weights.noteCompletion(minute)
	oldest := minute - breakerstore.TPMCorrectionLookbackMinutes()
	entries, expired := tracked.weights.corrections(scope.Kind, scope.ID, actualInput, actualOutput, oldest)
	if len(entries) == 0 && expired == 0 {
		return
	}
	select {
	case o.corrections <- correction{scope: scope.correctionScope(), entries: entries, expired: expired}:
	default:
		o.incDropped("correction_queue_full")
	}
}

// Run 驱动每秒 flush 与最终修正，直到 ctx 结束。退出前尽力 flush 一次。
func (o *Observer) Run(ctx context.Context) {
	if o == nil {
		return
	}
	defer close(o.done)
	ticker := time.NewTicker(o.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			o.finalFlush()
			return
		case job := <-o.corrections:
			o.applyCorrection(ctx, job)
		case <-ticker.C:
			o.flushOnce(ctx)
			o.evictStaleScopes()
		}
	}
}

// Wait 阻塞到 Run 退出（含最后一次 flush）。Run 未启动时立即返回。
func (o *Observer) Wait(ctx context.Context) {
	if o == nil {
		return
	}
	select {
	case <-o.done:
	case <-ctx.Done():
	}
}

func (o *Observer) enqueue(ev observation) {
	select {
	case o.queue <- ev:
	default:
		// 队列满：丢弃观测并计指标。观测永远不允许阻塞客户响应。
		o.incDropped("queue_full")
	}
}

func (o *Observer) withWeights(scope Scope, minute int64, mutate func(*scopeWeights) bool) bool {
	key := scope.trackingKey()
	o.mu.Lock()
	defer o.mu.Unlock()
	entry, ok := o.tracked[key]
	if !ok {
		if len(o.tracked) >= maxTrackedScopes {
			// 跟踪表已满：仍然记录观测，只是这次不再具备最终修正能力。
			o.incDropped("tracking_full")
			return true
		}
		entry = &trackedScope{weights: newScopeWeights()}
		o.tracked[key] = entry
	}
	entry.lastTouched = minute
	return mutate(entry.weights)
}

func (o *Observer) takeWeights(scope Scope) *trackedScope {
	key := scope.trackingKey()
	o.mu.Lock()
	defer o.mu.Unlock()
	entry, ok := o.tracked[key]
	if !ok {
		return nil
	}
	delete(o.tracked, key)
	return entry
}

// evictStaleScopes 回收从未 Finalize 的主体。超过回溯窗口后它们已经无法再修正任何桶。
func (o *Observer) evictStaleScopes() {
	cutoff := breakerstore.TPMObservationMinute(o.now()) - breakerstore.TPMCorrectionLookbackMinutes()
	o.mu.Lock()
	defer o.mu.Unlock()
	for key, entry := range o.tracked {
		if entry.lastTouched < cutoff {
			delete(o.tracked, key)
		}
	}
}

func (o *Observer) flushOnce(ctx context.Context) {
	batch := o.pending
	o.pending = nil
	if batch == nil {
		entries := o.drain()
		if len(entries) == 0 {
			o.setQueueDepth()
			return
		}
		o.sequence++
		batch = &pendingBatch{
			operationID: o.instanceID + ":" + strconv.FormatUint(o.sequence, 10),
			entries:     entries,
		}
	}

	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), o.flushTimeout)
	_, err := o.store.RecordTPMObservations(flushCtx, batch.operationID, batch.entries)
	cancel()
	if err != nil {
		batch.attempts++
		o.incFlushFailure()
		if batch.attempts >= 3 {
			o.incDropped("flush_retries_exhausted")
			logging.Warn(o.logger, "observability", "tpm", "tpm observation batch dropped after repeated flush failures",
				zap.Int("entry_count", len(batch.entries)), zap.Int("attempts", batch.attempts))
		} else {
			// 结果未知：用同一个 operation id 重试，脚本保证不会重复累加。
			o.pending = batch
		}
	}
	o.setQueueDepth()
}

func (o *Observer) finalFlush() {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownFlushTimeout)
	defer cancel()
	for {
		select {
		case job := <-o.corrections:
			o.applyCorrection(ctx, job)
			continue
		default:
		}
		break
	}
	o.flushOnce(ctx)
}

func (o *Observer) drain() []breakerstore.TPMObservationEntry {
	aggregate := make(map[breakerstore.TPMObservationScope]breakerstore.TPMObservationDelta)
	for len(aggregate) < maxBatchEntries {
		select {
		case ev := <-o.queue:
			delta := aggregate[ev.scope]
			delta.InputTokens += ev.delta.InputTokens
			delta.OutputTokens += ev.delta.OutputTokens
			delta.ProvisionalTokens += ev.delta.ProvisionalTokens
			delta.ObservedAttempts += ev.delta.ObservedAttempts
			delta.MissingUsageCount += ev.delta.MissingUsageCount
			aggregate[ev.scope] = delta
		default:
			return entriesFromAggregate(aggregate)
		}
	}
	return entriesFromAggregate(aggregate)
}

func entriesFromAggregate(
	aggregate map[breakerstore.TPMObservationScope]breakerstore.TPMObservationDelta,
) []breakerstore.TPMObservationEntry {
	entries := make([]breakerstore.TPMObservationEntry, 0, len(aggregate))
	for scope, delta := range aggregate {
		if delta.Empty() {
			continue
		}
		entries = append(entries, breakerstore.TPMObservationEntry{Scope: scope, Delta: delta})
	}
	return entries
}

func (o *Observer) applyCorrection(ctx context.Context, job correction) {
	if job.expired > 0 {
		o.addExpiredCorrection(job.expired)
	}
	if len(job.entries) == 0 {
		return
	}
	correctCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), o.flushTimeout)
	result, err := o.store.CorrectTPMObservations(correctCtx, job.scope, job.entries)
	cancel()
	if err != nil {
		o.incFinalCorrection("error")
		return
	}
	if result.Duplicate {
		o.incFinalCorrection("duplicate")
		return
	}
	o.incFinalCorrection("applied")
	if result.Expired > 0 {
		o.addExpiredCorrection(result.Expired)
	}
}

func (o *Observer) incFlushFailure() {
	if o.metrics != nil {
		o.metrics.IncTPMObservationFlushFailure()
	}
}

func (o *Observer) incDropped(reason string) {
	if o.metrics != nil {
		o.metrics.IncTPMObservationDropped(reason)
	}
}

func (o *Observer) setQueueDepth() {
	if o.metrics != nil {
		o.metrics.SetTPMObservationQueueDepth(float64(len(o.queue)))
	}
}

func (o *Observer) addProvisional(tokens int64) {
	if o.metrics != nil && tokens > 0 {
		o.metrics.AddTPMObservationProvisionalTokens(float64(tokens))
	}
}

func (o *Observer) incMissingUsage() {
	if o.metrics != nil {
		o.metrics.IncTPMObservationMissingUsage()
	}
}

func (o *Observer) incFinalCorrection(result string) {
	if o.metrics != nil {
		o.metrics.IncTPMObservationFinalCorrection(result)
	}
}

func (o *Observer) addExpiredCorrection(buckets int64) {
	if o.metrics != nil && buckets > 0 {
		o.metrics.AddTPMObservationExpiredCorrection(float64(buckets))
	}
}
