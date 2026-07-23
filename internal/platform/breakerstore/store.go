package breakerstore

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// ErrStoreUnavailable 表示 Redis/BreakerStore 基础设施故障；调用方必须 fail-closed（P4-D08）：
// 终止整次 fallback、不调用上游、返回安全 503。
var ErrStoreUnavailable = errors.New("breakerstore: infrastructure unavailable")

var (
	// ErrRuntimeStateLost 表示 Redis marker 不存在、损坏或不是 ready；生命周期脚本保证零写入。
	ErrRuntimeStateLost = errors.New("breakerstore: runtime state lost")
	// ErrStaleIntegrityEpoch 表示 expected epoch 与 marker 或服务端 token/permit 不一致；脚本保证零写入。
	ErrStaleIntegrityEpoch = errors.New("breakerstore: stale integrity epoch")
	// ErrRuntimeSyncRequired 表示服务端运行态记录损坏，无法安全续租或收口。
	ErrRuntimeSyncRequired = errors.New("breakerstore: runtime synchronization required")
)

// Store 是 P4 Redis 全局熔断的运行时实现。协议无关：三个生成执行面共用。
type Store struct {
	client   redis.Cmdable
	keys     keyBuilder
	observer OperationObserver
	fault    runtimeFaultLatchState

	gate            *redis.Script
	finish          *redis.Script
	abort           *redis.Script
	renew           *redis.Script
	reset           *redis.Script
	snapshot        *redis.Script
	snapshotMany    *redis.Script
	setCooldown     *redis.Script
	cooldownRemain  *redis.Script
	pausePermission *redis.Script
	clearPermission *redis.Script

	controlPrepare          *redis.Script
	controlCommit           *redis.Script
	controlAbort            *redis.Script
	controlRead             *redis.Script
	controlRestore          *redis.Script
	controlRecoverCommitted *redis.Script
	controlRecoverAborted   *redis.Script

	acquireRequest *redis.Script
	reserveRequest *redis.Script
	renewRequest   *redis.Script
	finishRequest  *redis.Script

	epochRead      *redis.Script
	epochPrepare   *redis.Script
	epochCommit    *redis.Script
	runtimeReady   *redis.Script
	faultProof     *redis.Script
	faultClear     *redis.Script
	faultDelete    *redis.Script
	faultBegin     *redis.Script
	serverIdentity *redis.Script

	epInitControl    *redis.Script
	epRestoreControl *redis.Script
	epPrepareStatus  *redis.Script
	epCommitStatus   *redis.Script
	epAbortStatus    *redis.Script
	epPrepareBaseURL *redis.Script
	epCommitBaseURL  *redis.Script
	epAbortBaseURL   *redis.Script
}

// NewStore 创建 BreakerStore。keyNamespace 为空回退 "unio"。
func NewStore(client redis.Cmdable, keyNamespace string, observers ...OperationObserver) *Store {
	if client == nil {
		panic("breakerstore: redis client is required")
	}
	if len(observers) > 1 {
		panic("breakerstore: at most one operation observer is supported")
	}
	var observer OperationObserver
	if len(observers) == 1 {
		observer = observers[0]
	}
	return &Store{
		client:          client,
		keys:            newKeyBuilder(keyNamespace),
		observer:        observer,
		fault:           newRuntimeFaultLatchState(),
		gate:            redis.NewScript(luaGateAndAcquire),
		finish:          redis.NewScript(luaFinish),
		abort:           redis.NewScript(luaAbort),
		renew:           redis.NewScript(luaRenew),
		reset:           redis.NewScript(luaReset),
		snapshot:        redis.NewScript(luaSnapshot),
		snapshotMany:    redis.NewScript(luaSnapshotMany),
		setCooldown:     redis.NewScript(luaSetCooldown),
		cooldownRemain:  redis.NewScript(luaCooldownRemaining),
		pausePermission: redis.NewScript(luaPausePermission),
		clearPermission: redis.NewScript(luaClearPermission),

		controlPrepare:          redis.NewScript(luaControlPrepare),
		controlCommit:           redis.NewScript(luaControlCommit),
		controlAbort:            redis.NewScript(luaControlAbort),
		controlRead:             redis.NewScript(luaControlRead),
		controlRestore:          redis.NewScript(luaControlRestoreMissing),
		controlRecoverCommitted: redis.NewScript(luaControlRecoverCommitted),
		controlRecoverAborted:   redis.NewScript(luaControlRecoverAborted),

		acquireRequest: redis.NewScript(luaAcquireRequestAdmission),
		reserveRequest: redis.NewScript(luaReserveRequestTokens),
		renewRequest:   redis.NewScript(luaRenewRequestAdmission),
		finishRequest:  redis.NewScript(luaFinishRequestAdmission),

		epochRead:      redis.NewScript(luaStateIntegrityRead),
		epochPrepare:   redis.NewScript(luaEpochPrepare),
		epochCommit:    redis.NewScript(luaEpochCommit),
		runtimeReady:   redis.NewScript(luaRuntimeReadiness),
		faultProof:     redis.NewScript(luaRuntimeFaultClearProof),
		faultClear:     redis.NewScript(luaRuntimeFaultClearCommit),
		faultDelete:    redis.NewScript(luaRuntimeFaultLatchDelete),
		faultBegin:     redis.NewScript(luaBeginRuntimeReconciliation),
		serverIdentity: redis.NewScript(luaRedisServerIdentity),

		epInitControl:    redis.NewScript(luaInitEndpointControl),
		epRestoreControl: redis.NewScript(luaRestoreMissingEndpointControl),
		epPrepareStatus:  redis.NewScript(luaPrepareEndpointStatus),
		epCommitStatus:   redis.NewScript(luaCommitEndpointStatus),
		epAbortStatus:    redis.NewScript(luaAbortEndpointStatus),
		epPrepareBaseURL: redis.NewScript(luaPrepareEndpointBaseURL),
		epCommitBaseURL:  redis.NewScript(luaCommitEndpointBaseURL),
		epAbortBaseURL:   redis.NewScript(luaAbortEndpointBaseURL),
	}
}

// VerifySingleNodeDeployment 校验当前 Redis 不是 Cluster（P4-D19：只支持单 Redis / 主从 / Sentinel）。
// 多 key 原子 Lua 不能拆步降级；检测到 Cluster 时拒绝就绪。启动时调用；失败即 readiness=false。
func (s *Store) VerifySingleNodeDeployment(ctx context.Context) (err error) {
	done := s.beginOperation(ctx, operationVerifyDeployment)
	defer func() { done(operationResultSuccess, err) }()

	info, err := s.client.Info(ctx, "cluster").Result()
	if err != nil {
		return storeUnavailable(err, "breakerstore verify deployment")
	}
	if containsSeg(info, "cluster_enabled:1") {
		return failure.New(
			failure.CodeConfigUnsupported,
			failure.WithMessage("P4 does not support Redis Cluster; use single Redis, primary/replica or Sentinel (P4-D19)"),
		)
	}
	identity, err := s.readRedisServerIdentity(ctx)
	if err != nil {
		return err
	}
	majorRaw, _, _ := strings.Cut(identity.version, ".")
	major, parseErr := strconv.Atoi(majorRaw)
	if parseErr != nil || major < 7 {
		return failure.New(
			failure.CodeConfigUnsupported,
			failure.WithMessage("P4 requires Redis 7 or newer for atomic server identity admission checks"),
		)
	}
	return nil
}

type redisServerIdentity struct {
	runID   string
	version string
}

func (s *Store) readRedisServerIdentity(ctx context.Context) (redisServerIdentity, error) {
	res, err := s.serverIdentity.Run(ctx, s.client, nil).Result()
	if err != nil {
		return redisServerIdentity{}, storeUnavailable(err, "breakerstore read Redis server identity")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) != 2 {
		return redisServerIdentity{}, storeUnavailable(errors.New("unexpected Redis server identity reply"), "breakerstore read Redis server identity")
	}
	runID, runOK := redisString(arr[0])
	version, versionOK := redisString(arr[1])
	if !runOK || !versionOK || runID == "" || version == "" {
		return redisServerIdentity{}, storeUnavailable(errors.New("invalid Redis server identity reply"), "breakerstore read Redis server identity")
	}
	return redisServerIdentity{runID: runID, version: version}, nil
}

// Ping 探测 Redis 可用性（readiness 用）。
func (s *Store) Ping(ctx context.Context) (err error) {
	done := s.beginOperation(ctx, operationPing)
	defer func() { done(operationResultSuccess, err) }()

	if err := s.client.Ping(ctx).Err(); err != nil {
		return storeUnavailable(err, "breakerstore ping")
	}
	return nil
}

// AcquireAttemptInput 是候选级原子准入的身份、expected revision、模式和 token 估算输入。
// 限额、breaker 参数及 permit 生命周期只从 Redis committed active controls 读取。
type AcquireAttemptInput struct {
	PermitID             string
	AdmissionFingerprint string
	RequestAdmissionID   string
	IntegrityEpoch       string
	IntegrityRevision    int64

	EndpointID int64
	ChannelID  int64

	EndpointBaseURLRevision int64
	EndpointStatusRevision  int64
	ChannelConfigRevision   int64

	ModelID           int64
	UpstreamOperation UpstreamOperation
	RequestMode       RequestMode

	ChannelRateRevision       int64
	GlobalConcurrencyRevision int64
	CircuitBreakerRevision    int64
	ChannelAdmissionRevision  int64

	// EnforceEndpointControl=true 时，校验 Endpoint control 存在、effective_status=enabled、无 pending、
	// 且 permit 冻结的 base_url/status revision 与当前一致（§5.3.2 围栏准入分界）。
	EnforceEndpointControl bool

	EstimatedInputTokens int64
}

// AcquireAttempt 一次 Redis Lua 原子取得 Endpoint/Channel breaker 门禁、half-open 租约、Channel 并发租约
// 与服务端 AttemptPermit。业务拒绝零资源变化并可 fallback；基础设施错误返回 ErrStoreUnavailable。
func (s *Store) AcquireAttempt(ctx context.Context, in AcquireAttemptInput) (admission AttemptAdmission, err error) {
	done := s.beginOperation(ctx, operationAcquireAttempt)
	defer func() { done(attemptAdmissionOperationResult(admission), err) }()

	if err := validateAcquireAttemptInput(in); err != nil {
		return AttemptAdmission{}, err
	}
	if s.localRuntimeInfrastructureFault(ctx) {
		return AttemptAdmission{Mode: AdmissionDenied, Reason: ReasonBreakerStoreUnavailable}, nil
	}
	now := time.Now()
	concKey := s.keys.channel(in.ChannelID) + ":conc"
	keys := []string{
		s.keys.endpoint(in.EndpointID),
		s.keys.channel(in.ChannelID),
		concKey,
		s.keys.permit(in.PermitID),
		s.keys.channel429Cooldown(in.ChannelID),
		s.keys.channelModelPermission(in.ChannelID, in.ModelID),
		s.keys.admissionChannel(in.ChannelID),
		s.keys.channelRPMBucket(in.ChannelID, minuteBucket(now)),
		s.keys.channelRPDBucket(in.ChannelID, dayBucket(now)),
		s.keys.channelTPMBucket(in.ChannelID, minuteBucket(now)),
		s.keys.admissionChannelRate(),
		s.keys.admissionGlobalConcurrency(),
		s.keys.runtimeControlSetting("gateway.circuit_breaker"),
		s.keys.stateIntegrityMarker(),
		s.keys.admissionRequest(in.RequestAdmissionID),
		s.keys.runtimeInfrastructureFault(),
		s.keys.runtimeReconciliationProof(),
	}
	enforceEndpoint := 0
	if in.EnforceEndpointControl {
		enforceEndpoint = 1
	}
	argv := []interface{}{
		in.PermitID,
		in.AdmissionFingerprint,
		in.RequestAdmissionID,
		strconv.FormatInt(in.EndpointID, 10),
		strconv.FormatInt(in.ChannelID, 10),
		strconv.FormatInt(in.EndpointBaseURLRevision, 10),
		strconv.FormatInt(in.EndpointStatusRevision, 10),
		strconv.FormatInt(in.ChannelConfigRevision, 10),
		strconv.FormatInt(in.ModelID, 10),
		string(in.UpstreamOperation),
		string(in.RequestMode),
		strconv.FormatInt(in.ChannelAdmissionRevision, 10),
		strconv.FormatInt(in.ChannelRateRevision, 10),
		strconv.FormatInt(in.GlobalConcurrencyRevision, 10),
		strconv.FormatInt(in.CircuitBreakerRevision, 10),
		strconv.FormatInt(in.EstimatedInputTokens, 10),
		in.IntegrityEpoch,
		strconv.FormatInt(in.IntegrityRevision, 10),
		enforceEndpoint,
	}

	res, err := s.gate.Run(ctx, s.client, keys, argv...).Result()
	if err != nil {
		return AttemptAdmission{}, storeUnavailable(err, "breakerstore acquire attempt")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return AttemptAdmission{}, storeUnavailable(errors.New("unexpected acquire reply"), "breakerstore acquire attempt")
	}
	code, _ := arr[0].(string)
	if code == runtimeRedisInstanceChanged {
		s.ensureRuntimeInfrastructureFault(ctx)
		return AttemptAdmission{Mode: AdmissionDenied, Reason: ReasonBreakerStoreUnavailable}, nil
	}
	switch code {
	case "denied":
		reason := DeniedReason("")
		if len(arr) > 1 {
			r, _ := arr[1].(string)
			reason = DeniedReason(r)
		}
		if reason == DeniedReason(runtimeRedisInstanceChanged) {
			s.ensureRuntimeInfrastructureFault(ctx)
			reason = ReasonBreakerStoreUnavailable
		}
		return AttemptAdmission{Mode: AdmissionDenied, Reason: reason}, nil
	case "conflict":
		return AttemptAdmission{}, failure.New(failure.CodeGatewayBreakerPermitConflict, failure.WithMessage("attempt permit fingerprint conflict"))
	case "permit", "idempotent":
		permit := s.permitFromAcquire(in, arr)
		return AttemptAdmission{Mode: AdmissionPermit, Permit: permit}, nil
	default:
		return AttemptAdmission{}, storeUnavailable(errors.New("unknown acquire code: "+code), "breakerstore acquire attempt")
	}
}

func (s *Store) permitFromAcquire(in AcquireAttemptInput, arr []interface{}) *AttemptPermit {
	toI64 := func(v interface{}) int64 {
		switch t := v.(type) {
		case int64:
			return t
		case string:
			n, _ := strconv.ParseInt(t, 10, 64)
			return n
		}
		return 0
	}
	toBool := func(v interface{}) bool {
		return toI64(v) == 1
	}
	p := &AttemptPermit{
		PermitID:                in.PermitID,
		RequestAdmissionID:      in.RequestAdmissionID,
		IntegrityEpoch:          in.IntegrityEpoch,
		IntegrityRevision:       in.IntegrityRevision,
		EndpointID:              in.EndpointID,
		ChannelID:               in.ChannelID,
		EndpointBaseURLRevision: in.EndpointBaseURLRevision,
		EndpointStatusRevision:  in.EndpointStatusRevision,
		ChannelConfigRevision:   in.ChannelConfigRevision,
		ModelID:                 in.ModelID,
		UpstreamOperation:       in.UpstreamOperation,
		RequestMode:             in.RequestMode,
	}
	// arr = {code, ep_gen, ch_gen, ep_probe, ch_probe, lease_until, acquired_at, permit_ttl, renew, terminal_ttl}
	if len(arr) >= 7 {
		p.EndpointStateGeneration = toI64(arr[1])
		p.ChannelStateGeneration = toI64(arr[2])
		p.EndpointHalfOpenProbe = toBool(arr[3])
		p.ChannelHalfOpenProbe = toBool(arr[4])
		p.LeaseUntilMs = toI64(arr[5])
		p.AcquiredAtMs = toI64(arr[6])
	}
	if len(arr) >= 10 {
		p.PermitTTLMs = toI64(arr[7])
		p.RenewMs = toI64(arr[8])
		p.TerminalTTLMs = toI64(arr[9])
	}
	return p
}

func (s *Store) attemptLifecycleKeys(permit AttemptPermit) []string {
	return []string{
		s.keys.stateIntegrityMarker(),
		s.keys.permit(permit.PermitID),
		s.keys.endpoint(permit.EndpointID),
		s.keys.channel(permit.ChannelID),
		s.keys.channel(permit.ChannelID) + ":conc",
	}
}

func (s *Store) endpointEvidenceKeys(endpointID int64, category EndpointEvidenceCategory) []string {
	return []string{
		s.keys.endpointEvidenceChannels(endpointID, string(category)),
		s.keys.endpointEvidenceModels(endpointID, string(category)),
	}
}

func (s *Store) allEndpointEvidenceKeys(endpointID int64) []string {
	keys := make([]string, 0, 6)
	for _, category := range []EndpointEvidenceCategory{
		EndpointEvidenceHTTP500,
		EndpointEvidenceFirstTokenTimeout,
		EndpointEvidenceBodyReadTimeout,
	} {
		keys = append(keys, s.endpointEvidenceKeys(endpointID, category)...)
	}
	return keys
}

func attemptLifecycleArgs(permit AttemptPermit) []interface{} {
	boolArg := func(value bool) int {
		if value {
			return 1
		}
		return 0
	}
	return []interface{}{
		permit.PermitID,
		permit.IntegrityEpoch,
		strconv.FormatInt(permit.IntegrityRevision, 10),
		permit.RequestAdmissionID,
		strconv.FormatInt(permit.EndpointID, 10),
		strconv.FormatInt(permit.ChannelID, 10),
		strconv.FormatInt(permit.EndpointBaseURLRevision, 10),
		strconv.FormatInt(permit.EndpointStatusRevision, 10),
		strconv.FormatInt(permit.ChannelConfigRevision, 10),
		strconv.FormatInt(permit.ModelID, 10),
		string(permit.UpstreamOperation),
		string(permit.RequestMode),
		strconv.FormatInt(permit.EndpointStateGeneration, 10),
		strconv.FormatInt(permit.ChannelStateGeneration, 10),
		boolArg(permit.EndpointHalfOpenProbe),
		boolArg(permit.ChannelHalfOpenProbe),
	}
}

// Finish 是唯一把真实上游结果写入 breaker 的入口（已进入真实 transport 的 attempt）。
func (s *Store) Finish(ctx context.Context, permit AttemptPermit, outcome FinishOutcome) (result FinishResult, err error) {
	done := s.beginOperation(ctx, operationFinishAttempt)
	defer func() { done(finishAttemptOperationResult(result), err) }()

	if err := validateFinishInput(permit, outcome); err != nil {
		return FinishResult{}, err
	}
	firstToken := ""
	if permit.RequestMode == ModeStream && outcome.FirstTokenMs != nil {
		firstToken = strconv.FormatInt(*outcome.FirstTokenMs, 10)
	}
	tpmActual := ""
	if outcome.ChannelTPMActual != nil {
		tpmActual = strconv.FormatInt(*outcome.ChannelTPMActual, 10)
	}
	keys := append(s.attemptLifecycleKeys(permit),
		s.keys.runtimeControlSetting("gateway.circuit_breaker"),
		s.keys.runtimeControlSetting("gateway.routing_balance"),
	)
	keys = append(keys, s.endpointEvidenceKeys(permit.EndpointID, outcome.EndpointEvidence)...)
	argv := append(attemptLifecycleArgs(permit),
		string(outcome.EndpointOutcome),
		string(outcome.ChannelOutcome),
		firstToken,
		tpmActual,
		string(outcome.EndpointEvidence),
	)

	res, err := s.finish.Run(ctx, s.client, keys, argv...).Result()
	if err != nil {
		return FinishResult{}, storeUnavailable(err, "breakerstore finish")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 2 {
		return FinishResult{}, storeUnavailable(errors.New("unexpected finish reply"), "breakerstore finish")
	}
	epDisp, _ := arr[0].(string)
	chDisp, _ := arr[1].(string)
	return FinishResult{EndpointDisposition: Disposition(epDisp), ChannelDisposition: Disposition(chDisp)}, nil
}

// Abort 用于已获准但未进入真实 transport 的路径：释放资源，不计 breaker 结果。
func (s *Store) Abort(ctx context.Context, permit AttemptPermit) (err error) {
	done := s.beginOperation(ctx, operationAbortAttempt)
	defer func() { done(operationResultSuccess, err) }()

	if err := validateAttemptPermit(permit); err != nil {
		return err
	}
	res, err := s.abort.Run(ctx, s.client, s.attemptLifecycleKeys(permit), attemptLifecycleArgs(permit)...).Result()
	if err != nil {
		return storeUnavailable(err, "breakerstore abort")
	}
	return parseAttemptLifecycleReply(res, "breakerstore abort", "aborted")
}

// Renew 延长仍 active 的 permit、并发租约与仍匹配的 half-open 租约。
func (s *Store) Renew(ctx context.Context, permit AttemptPermit) (err error) {
	done := s.beginOperation(ctx, operationRenewAttempt)
	defer func() { done(operationResultSuccess, err) }()

	if err := validateAttemptPermit(permit); err != nil {
		return err
	}
	res, err := s.renew.Run(ctx, s.client, s.attemptLifecycleKeys(permit), attemptLifecycleArgs(permit)...).Result()
	if err != nil {
		return storeUnavailable(err, "breakerstore renew")
	}
	return parseAttemptLifecycleReply(res, "breakerstore renew", "renewed", "expired")
}

func parseAttemptLifecycleReply(res interface{}, operation string, success ...string) error {
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return storeUnavailable(errors.New("unexpected attempt lifecycle reply"), operation)
	}
	code, _ := arr[0].(string)
	for _, allowed := range success {
		if code == allowed {
			return nil
		}
	}
	// Preserve idempotent terminal and expired/missing lease behavior while exposing integrity failures.
	switch code {
	case "terminal_conflict", "unknown_permit":
		return nil
	case "runtime_state_lost":
		return failure.Wrap(
			failure.CodeGatewayRuntimeStateLost,
			ErrRuntimeStateLost,
			failure.WithMessage(operation+" rejected because runtime state is not ready"),
		)
	case "stale_integrity_epoch":
		return failure.Wrap(
			failure.CodeGatewayRuntimeSyncRequired,
			ErrStaleIntegrityEpoch,
			failure.WithMessage(operation+" rejected by integrity epoch fence"),
		)
	case "runtime_sync_required":
		return failure.Wrap(
			failure.CodeGatewayRuntimeSyncRequired,
			ErrRuntimeSyncRequired,
			failure.WithMessage(operation+" rejected because the permit record is invalid"),
		)
	case "conflict":
		return failure.New(
			failure.CodeGatewayBreakerPermitConflict,
			failure.WithMessage(operation+" rejected because the permit identity conflicts"),
		)
	default:
		return storeUnavailable(errors.New("unknown attempt lifecycle outcome: "+code), operation)
	}
}

// SetChannel429Cooldown 登记/延长某 Channel 的全局 429 冷却（§2.4.1）。durationMs 通常来自上游
// Retry-After（缺失时用系统默认），并已由调用方截断到系统上限。返回冷却截止时间（Redis 逻辑毫秒）。
func (s *Store) SetChannel429Cooldown(ctx context.Context, channelID, durationMs, sourceRetryAfterMs int64) (untilMs int64, err error) {
	done := s.beginOperation(ctx, operationSet429Cooldown)
	defer func() { done(operationResultSuccess, err) }()

	if durationMs < 0 {
		durationMs = 0
	}
	res, err := s.setCooldown.Run(ctx, s.client, []string{s.keys.channel429Cooldown(channelID)},
		strconv.FormatInt(durationMs, 10), strconv.FormatInt(sourceRetryAfterMs, 10)).Result()
	if err != nil {
		return 0, storeUnavailable(err, "breakerstore set 429 cooldown")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return 0, storeUnavailable(errors.New("unexpected cooldown reply"), "breakerstore set 429 cooldown")
	}
	until, _ := arr[0].(int64)
	return until, nil
}

// Channel429CooldownRemainingMs 只读返回某 Channel 的 429 冷却剩余毫秒（0=无冷却/已到期）。
func (s *Store) Channel429CooldownRemainingMs(ctx context.Context, channelID int64) (remainingMs int64, err error) {
	done := s.beginOperation(ctx, operationRead429Cooldown)
	defer func() {
		result := operationResultIdle
		if remainingMs > 0 {
			result = operationResultActive
		}
		done(result, err)
	}()

	res, err := s.cooldownRemain.Run(ctx, s.client, []string{s.keys.channel429Cooldown(channelID)}).Result()
	if err != nil {
		return 0, storeUnavailable(err, "breakerstore 429 cooldown remaining")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return 0, storeUnavailable(errors.New("unexpected cooldown reply"), "breakerstore 429 cooldown remaining")
	}
	rem, _ := arr[0].(int64)
	return rem, nil
}

// PauseChannelModelPermission 登记一次 (channel_id, model_id) 403 权限暂停，固化观察到的三类 revision（§2.4.2）。
// 只暂停该绑定，不影响同 Channel 其它模型，也不翻整个 Channel 的 credential_valid；迟到的旧 revision 原子 no-op。
func (s *Store) PauseChannelModelPermission(ctx context.Context, channelID, modelID, configRev, baseURLRev, statusRev int64) (err error) {
	result := operationResultPaused
	done := s.beginOperation(ctx, operationPausePermission)
	defer func() { done(result, err) }()

	if channelID <= 0 || modelID <= 0 || configRev <= 0 || baseURLRev <= 0 || statusRev <= 0 {
		return configInvalid("channel-model permission identity is invalid")
	}
	res, err := s.pausePermission.Run(ctx, s.client,
		[]string{s.keys.channelModelPermission(channelID, modelID), s.keys.permissionRecheckQueue()},
		strconv.FormatInt(configRev, 10), strconv.FormatInt(baseURLRev, 10),
		strconv.FormatInt(statusRev, 10), strconv.FormatInt(channelID, 10), strconv.FormatInt(modelID, 10)).Result()
	if err != nil {
		return storeUnavailable(err, "breakerstore pause channel-model permission")
	}
	if arr, ok := res.([]interface{}); ok && len(arr) > 0 {
		if code, ok := redisString(arr[0]); ok && code == "stale" {
			result = "stale"
		}
	}
	return nil
}

// ClearChannelModelPermission 仅在暂停记录的三类 revision 仍与 expected 一致时 CAS 清除暂停（复检通过，§2.4.4）。
// 返回 true 表示已清除；false 表示 stale/absent（不改变当前状态）。
func (s *Store) ClearChannelModelPermission(ctx context.Context, channelID, modelID, configRev, baseURLRev, statusRev int64) (cleared bool, err error) {
	result := operationResultIgnored
	done := s.beginOperation(ctx, operationClearPermission)
	defer func() { done(result, err) }()

	if channelID <= 0 || modelID <= 0 || configRev <= 0 || baseURLRev <= 0 || statusRev <= 0 {
		return false, configInvalid("channel-model permission identity is invalid")
	}
	res, err := s.clearPermission.Run(ctx, s.client,
		[]string{s.keys.channelModelPermission(channelID, modelID), s.keys.permissionRecheckQueue()},
		strconv.FormatInt(configRev, 10), strconv.FormatInt(baseURLRev, 10), strconv.FormatInt(statusRev, 10)).Result()
	if err != nil {
		return false, storeUnavailable(err, "breakerstore clear channel-model permission")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return false, storeUnavailable(errors.New("unexpected clear permission reply"), "breakerstore clear channel-model permission")
	}
	code, _ := arr[0].(string)
	if code == string(PermissionRecheckCleared) {
		result = string(PermissionRecheckCleared)
	} else if code == "stale" || code == string(PermissionRecheckAbsent) {
		result = code
	}
	return code == "cleared", nil
}

// Reset 显式复位某作用域运行态（推进 generation，恢复 closed/no-sample）；旧 permit 随后 no-op。
func (s *Store) Reset(ctx context.Context, scope Scope, id int64) (generation int64, err error) {
	done := s.beginOperation(ctx, operationReset)
	defer func() { done(operationResultSuccess, err) }()

	if id <= 0 {
		return 0, configInvalid("breaker scope id must be positive")
	}
	var keys []string
	switch scope {
	case ScopeChannel:
		keys = []string{s.keys.channel(id)}
	case ScopeEndpoint:
		keys = append([]string{s.keys.endpoint(id)}, s.allEndpointEvidenceKeys(id)...)
	default:
		return 0, failure.New(failure.CodeConfigInvalid, failure.WithMessage("unknown breaker scope"))
	}
	res, err := s.reset.Run(ctx, s.client, keys).Result()
	if err != nil {
		return 0, storeUnavailable(err, "breakerstore reset")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return 0, storeUnavailable(errors.New("unexpected reset reply"), "breakerstore reset")
	}
	gen, _ := arr[0].(int64)
	return gen, nil
}

// Snapshot 只读返回某作用域当前运行态（不推进状态机）。
func (s *Store) Snapshot(ctx context.Context, scope Scope, id int64) (snapshot ScopeSnapshot, err error) {
	done := s.beginOperation(ctx, operationSnapshot)
	defer func() {
		result := string(PermissionRecheckAbsent)
		if snapshot.Exists {
			result = operationResultPresent
		}
		done(result, err)
	}()

	if id <= 0 {
		return ScopeSnapshot{}, configInvalid("breaker scope id must be positive")
	}
	var key string
	switch scope {
	case ScopeChannel:
		key = s.keys.channel(id)
	case ScopeEndpoint:
		key = s.keys.endpoint(id)
	default:
		return ScopeSnapshot{}, failure.New(failure.CodeConfigInvalid, failure.WithMessage("unknown breaker scope"))
	}
	res, err := s.snapshot.Run(ctx, s.client, []string{key}).Result()
	if err != nil {
		return ScopeSnapshot{}, storeUnavailable(err, "breakerstore snapshot")
	}
	snap, err := parseSnapshotRow(scope, id, res)
	if err != nil {
		return ScopeSnapshot{}, storeUnavailable(err, "breakerstore snapshot")
	}
	return snap, nil
}

// SnapshotMany 在同一次 Redis Lua 执行中校验完整性、四项 control 与候选运行态。
// Lua 只读；任一 runtime-sync、key 类型或 Redis 基础设施错误都会使整批失败。
func (s *Store) SnapshotMany(ctx context.Context, in SnapshotManyInput) (result SnapshotManyResult, err error) {
	done := s.beginOperation(ctx, operationSnapshotMany)
	defer func() { done(operationResultSuccess, err) }()

	if len(in.Candidates) > maxSnapshotCandidates {
		return SnapshotManyResult{}, configInvalid("snapshot candidate batch exceeds the maximum")
	}
	if len(in.Candidates) == 0 {
		return SnapshotManyResult{Candidates: []CandidateSnapshot{}}, nil
	}
	if err := validateSnapshotManyInput(in); err != nil {
		return SnapshotManyResult{}, err
	}
	if s.localRuntimeInfrastructureFault(ctx) {
		return SnapshotManyResult{}, snapshotManyRejected(string(ReasonBreakerStoreUnavailable))
	}
	keys := []string{
		s.keys.stateIntegrityMarker(),
		s.keys.admissionChannelRate(),
		s.keys.admissionGlobalConcurrency(),
		s.keys.runtimeControlSetting("gateway.circuit_breaker"),
		s.keys.runtimeControlSetting("gateway.routing_balance"),
	}
	argv := []interface{}{
		strconv.Itoa(len(in.Candidates)),
		strconv.FormatInt(in.ModelID, 10),
		in.IntegrityEpoch,
		strconv.FormatInt(in.IntegrityRevision, 10),
		strconv.FormatInt(in.ChannelRateRevision, 10),
		strconv.FormatInt(in.GlobalConcurrencyRevision, 10),
		strconv.FormatInt(in.CircuitBreakerRevision, 10),
		strconv.FormatInt(in.RoutingBalanceRevision, 10),
	}
	for _, candidate := range in.Candidates {
		if err := validateSnapshotCandidate(candidate); err != nil {
			return SnapshotManyResult{}, err
		}
		keys = append(keys,
			s.keys.endpoint(candidate.EndpointID),
			s.keys.channel(candidate.ChannelID),
			s.keys.channel(candidate.ChannelID)+":conc",
			s.keys.channel429Cooldown(candidate.ChannelID),
			s.keys.channelModelPermission(candidate.ChannelID, in.ModelID),
			s.keys.admissionChannel(candidate.ChannelID),
		)
		argv = append(argv,
			strconv.FormatInt(candidate.EndpointID, 10),
			strconv.FormatInt(candidate.ChannelID, 10),
			strconv.FormatInt(candidate.EndpointBaseURLRevision, 10),
			strconv.FormatInt(candidate.EndpointStatusRevision, 10),
			strconv.FormatInt(candidate.ChannelConfigRevision, 10),
			strconv.FormatInt(candidate.ChannelAdmissionRevision, 10),
			s.keys.channelRPMBucketPrefix(candidate.ChannelID),
			s.keys.channelRPDBucketPrefix(candidate.ChannelID),
			s.keys.channelTPMBucketPrefix(candidate.ChannelID),
		)
	}
	keys = append(keys, s.keys.runtimeInfrastructureFault(), s.keys.runtimeReconciliationProof())
	res, err := s.snapshotMany.Run(ctx, s.client, keys, argv...).Result()
	if err != nil {
		return SnapshotManyResult{}, storeUnavailable(err, "breakerstore snapshot many: "+err.Error())
	}
	reply, ok := res.([]interface{})
	if !ok || len(reply) < 2 {
		return SnapshotManyResult{}, storeUnavailable(errors.New("unexpected snapshot many reply"), "breakerstore snapshot many")
	}
	code, _ := redisString(reply[0])
	if code == "error" {
		reason, _ := redisString(reply[1])
		if reason == runtimeRedisInstanceChanged {
			s.ensureRuntimeInfrastructureFault(ctx)
			reason = string(ReasonBreakerStoreUnavailable)
		}
		return SnapshotManyResult{}, snapshotManyRejected(reason)
	}
	if code != "ok" || len(reply) != 10 {
		return SnapshotManyResult{}, storeUnavailable(errors.New("unknown snapshot many reply"), "breakerstore snapshot many")
	}
	return parseSnapshotManyReply(in, reply)
}

func storeUnavailable(err error, msg string) error {
	return failure.Wrap(
		failure.CodeDependencyRedisUnavailable,
		errors.Join(ErrStoreUnavailable, err),
		failure.WithMessage(msg),
	)
}
